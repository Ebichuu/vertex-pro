package snapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Store struct {
	database *sql.DB
	root     string
}

type Record struct {
	InstanceID string
	Generation uint64
	Revision   uint64
	RID        uint64
	Digest     string
	Path       string
	Complete   bool
	CreatedAt  string
}

func New(database *sql.DB, root string) (Store, error) {
	if database == nil {
		return Store{}, errors.New("database is required")
	}
	if strings.TrimSpace(root) == "" {
		return Store{}, errors.New("snapshot root is required")
	}
	return Store{database: database, root: filepath.Clean(root)}, nil
}

func (store Store) Save(ctx context.Context, instanceID string, generation, revision uint64, snapshot syncmerge.Snapshot, complete bool) (Record, error) {
	if err := validateCoordinates(instanceID, generation, revision); err != nil {
		return Record{}, err
	}
	if snapshot.Torrents == nil {
		return Record{}, errors.New("snapshot torrents are required")
	}
	if !complete {
		return Record{}, errors.New("only complete snapshots may be persisted")
	}
	var currentGeneration, currentRevision int64
	if err := store.database.QueryRowContext(ctx, "SELECT sync_generation, snapshot_revision FROM sync_instance_state WHERE instance_id = ?", instanceID).Scan(&currentGeneration, &currentRevision); err != nil {
		return Record{}, fmt.Errorf("read sync instance state: %w", err)
	}
	if currentGeneration > int64(generation) || (currentGeneration == int64(generation) && currentRevision >= int64(revision)) {
		return Record{}, errors.New("snapshot coordinates are not newer than current state")
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Record{}, fmt.Errorf("marshal snapshot: %w", err)
	}
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])
	relativePath := path.Join(instanceID, strconv.FormatUint(generation, 10)+"-"+strconv.FormatUint(revision, 10)+"-"+digest+".json")
	absPath := filepath.Join(store.root, relativePath)
	if err := writeAtomically(absPath, payload); err != nil {
		return Record{}, err
	}

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var stateGeneration, stateRevision int64
	if err := transaction.QueryRowContext(ctx, "SELECT sync_generation, snapshot_revision FROM sync_instance_state WHERE instance_id = ?", instanceID).Scan(&stateGeneration, &stateRevision); err != nil {
		return Record{}, fmt.Errorf("read sync instance state: %w", err)
	}
	if stateGeneration > int64(generation) || (stateGeneration == int64(generation) && stateRevision >= int64(revision)) {
		return Record{}, fmt.Errorf("snapshot coordinates are not newer than current state")
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, instanceID, generation, revision, snapshot.RID, digest, relativePath, boolInt(complete), createdAt); err != nil {
		return Record{}, fmt.Errorf("insert snapshot reference: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE sync_instance_state SET sync_generation = ?, rid = ?, snapshot_revision = ?, snapshot_digest = ?, updated_at = ? WHERE instance_id = ?`, generation, snapshot.RID, revision, digest, createdAt, instanceID); err != nil {
		return Record{}, fmt.Errorf("update sync instance state: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit snapshot transaction: %w", err)
	}
	committed = true
	return Record{InstanceID: instanceID, Generation: generation, Revision: revision, RID: snapshot.RID, Digest: digest, Path: relativePath, Complete: complete, CreatedAt: createdAt}, nil
}

func (store Store) Load(ctx context.Context, instanceID string, generation, revision uint64) (syncmerge.Snapshot, Record, error) {
	if err := validateCoordinates(instanceID, generation, revision); err != nil {
		return syncmerge.Snapshot{}, Record{}, err
	}
	var record Record
	var complete int
	if err := store.database.QueryRowContext(ctx, `SELECT instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at FROM sync_snapshots WHERE instance_id = ? AND sync_generation = ? AND snapshot_revision = ?`, instanceID, generation, revision).Scan(&record.InstanceID, &record.Generation, &record.Revision, &record.RID, &record.Digest, &record.Path, &complete, &record.CreatedAt); err != nil {
		return syncmerge.Snapshot{}, Record{}, fmt.Errorf("read snapshot reference: %w", err)
	}
	record.Complete = complete == 1
	absolutePath, err := store.resolvePath(record.Path)
	if err != nil {
		return syncmerge.Snapshot{}, Record{}, err
	}
	payload, err := os.ReadFile(absolutePath)
	if err != nil {
		return syncmerge.Snapshot{}, Record{}, fmt.Errorf("read snapshot file: %w", err)
	}
	digestBytes := sha256.Sum256(payload)
	if got := hex.EncodeToString(digestBytes[:]); got != record.Digest {
		return syncmerge.Snapshot{}, Record{}, fmt.Errorf("snapshot digest mismatch: database has %s, file has %s", record.Digest, got)
	}
	var snapshot syncmerge.Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return syncmerge.Snapshot{}, Record{}, fmt.Errorf("decode snapshot file: %w", err)
	}
	if snapshot.RID != record.RID {
		return syncmerge.Snapshot{}, Record{}, errors.New("snapshot RID does not match database reference")
	}
	return snapshot, record, nil
}

func (store Store) LoadLatest(ctx context.Context, instanceID string) (syncmerge.Snapshot, Record, error) {
	if !instanceIDPattern.MatchString(instanceID) {
		return syncmerge.Snapshot{}, Record{}, errors.New("invalid instance ID")
	}
	var generation, revision uint64
	if err := store.database.QueryRowContext(ctx, `SELECT sync_generation, snapshot_revision FROM sync_instance_state WHERE instance_id = ?`, instanceID).Scan(&generation, &revision); err != nil {
		return syncmerge.Snapshot{}, Record{}, fmt.Errorf("read latest snapshot coordinates: %w", err)
	}
	return store.Load(ctx, instanceID, generation, revision)
}

func (store Store) resolvePath(relativePath string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(relativePath, "\\", "/"))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid snapshot path")
	}
	absolute := filepath.Join(store.root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(store.root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("snapshot path escapes root")
	}
	return absolute, nil
}
func validateCoordinates(instanceID string, generation, revision uint64) error {
	if !instanceIDPattern.MatchString(instanceID) {
		return errors.New("invalid instance ID")
	}
	if generation > uint64(^uint64(0)>>1) || revision > uint64(^uint64(0)>>1) {
		return errors.New("snapshot coordinates exceed SQLite integer range")
	}
	return nil
}

func writeAtomically(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".tmp-snapshot-*")
	if err != nil {
		return fmt.Errorf("create snapshot temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set snapshot permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write snapshot temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("fsync snapshot temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close snapshot temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("rename snapshot file: %w", err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open snapshot directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("fsync snapshot directory: %w", err)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
