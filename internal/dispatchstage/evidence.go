package dispatchstage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/alreadypresent"
	"github.com/vertex-pro/vertex-pro/internal/capacityprojection"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// Evidence is collected through the configured qui proxy immediately before
// placement. Record identifies the exact complete snapshot in the local store.
type Evidence struct {
	Snapshot    syncmerge.Snapshot
	Record      snapshot.Record
	Preferences map[string]any
	CollectedAt time.Time
}

type capacityFact struct {
	ID                             string
	AllocationEpoch, EffectiveFree int64
}

type snapshotStateReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Validate the evidence independently of capacity: positive identity evidence
// remains meaningful even when another task's remaining bytes are unknown.
func (stage *Stage) validateSnapshot(ctx context.Context, reader snapshotStateReader, evidence Evidence) error {
	storage := stage.config.Storage
	now := stage.config.Now().UTC()
	if !stage.config.ProfileExpiresAt.IsZero() && !now.Before(stage.config.ProfileExpiresAt) {
		return errors.New("profile verification expired")
	}
	if !evidence.Record.Complete || evidence.Record.InstanceID != storage.InstanceID || evidence.Record.Digest == "" || evidence.Record.Revision == 0 || evidence.Record.Generation == 0 || evidence.Snapshot.Torrents == nil {
		return errors.New("complete instance snapshot required")
	}
	if evidence.CollectedAt.IsZero() || now.Before(evidence.CollectedAt) || now.Sub(evidence.CollectedAt) > 30*time.Second {
		return errors.New("placement snapshot is stale")
	}
	var state, digest string
	var generation, revision int64
	if err := reader.QueryRowContext(ctx, `SELECT sync_state,sync_generation,snapshot_revision,snapshot_digest FROM sync_instance_state WHERE instance_id=?`, storage.InstanceID).Scan(&state, &generation, &revision, &digest); err != nil {
		return err
	}
	if state != "READY" || uint64(generation) != evidence.Record.Generation || uint64(revision) != evidence.Record.Revision || digest != evidence.Record.Digest {
		return errors.New("sync changed before placement")
	}
	var disabled int
	if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM instance_metadata WHERE instance_id=? AND enabled=0`, storage.InstanceID).Scan(&disabled); err != nil {
		return err
	}
	if disabled > 0 {
		return errors.New("instance disabled before placement")
	}
	return nil
}

func (stage *Stage) prepareCapacity(ctx context.Context, evidence Evidence) (capacityFact, error) {
	storage := stage.config.Storage
	now := stage.config.Now().UTC()
	if err := stage.validateSnapshot(ctx, stage.database, evidence); err != nil {
		return capacityFact{}, err
	}
	defaultPath, _ := evidence.Preferences["save_path"].(string)
	tempEnabled, known := evidence.Preferences["temp_path_enabled"].(bool)
	if defaultPath == "" || !known || tempEnabled {
		return capacityFact{}, errors.New("default filesystem or temporary path is not verified")
	}
	// qB reports free_space_on_disk for its exact default save path. For a
	// different managed path, require independent device evidence; a common
	// path prefix alone does not establish a common filesystem.
	if strings.TrimRight(storage.Path, "/\\") != strings.TrimRight(defaultPath, "/\\") {
		target := storageverify.StorageTarget{ID: storage.StorageTargetID, ManagedRoot: storage.Path, QBDefaultSavePath: defaultPath, FilesystemKey: storage.Verification.FilesystemKey, ConfigurationRevision: storage.Verification.ConfigurationRevision}
		if result := storageverify.Validate(target, storage.Verification, now, 24*time.Hour); result.State != storageverify.StateVerified {
			return capacityFact{}, errors.New("storage path needs current device evidence")
		}
	}
	physical, valid := byteCount(evidence.Snapshot.ServerState["free_space_on_disk"])
	if !valid || storage.SafetyMarginBytes < 0 {
		return capacityFact{}, errors.New("physical free space is unknown")
	}
	result := capacityFact{ID: "epoch-" + storage.StorageTargetID, AllocationEpoch: 1}
	err := db.RunImmediate(ctx, stage.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		if err := stage.validateSnapshot(ctx, tx, evidence); err != nil {
			return err
		}
		generation, revision, digest := evidence.Record.Generation, evidence.Record.Revision, evidence.Record.Digest
		if _, err := tx.ExecContext(ctx, `INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES(?,?,?,'VERIFIED',?,?) ON CONFLICT(storage_target_id) DO UPDATE SET storage_verification_state='VERIFIED',updated_at=excluded.updated_at WHERE instance_id=excluded.instance_id AND path=excluded.path`, storage.StorageTargetID, storage.InstanceID, storage.Path, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		var savedInstance, savedPath string
		if err := tx.QueryRowContext(ctx, `SELECT instance_id,path FROM storage_targets WHERE storage_target_id=?`, storage.StorageTargetID).Scan(&savedInstance, &savedPath); err != nil {
			return err
		}
		if savedInstance != storage.InstanceID || savedPath != storage.Path {
			return errors.New("storage target identity changed")
		}
		if err := stage.reconcileCompleted(ctx, tx, evidence, now); err != nil {
			return err
		}
		entries := []capacityprojection.LedgerEntry{{Key: "safety-margin", Kind: capacityprojection.KindSafetyMargin, Bytes: storage.SafetyMarginBytes}}
		owned := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT r.reservation_id,r.reserved_bytes,a.identity_key FROM capacity_reservations r JOIN placement_attempts a ON a.attempt_id=r.attempt_id WHERE a.instance_id=? AND r.reservation_state IN ('ACTIVE','REFLECTING')`, storage.InstanceID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, identity string
			var bytes int64
			if err := rows.Scan(&id, &bytes, &identity); err != nil {
				rows.Close()
				return err
			}
			owned[strings.ToLower(identity)] = true
			entries = append(entries, capacityprojection.LedgerEntry{Key: "reservation:" + id, Kind: capacityprojection.KindReservation, Bytes: bytes})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for id, torrent := range evidence.Snapshot.Torrents {
			if owned[strings.ToLower(id)] {
				continue
			}
			if torrent.AmountLeftState != "KNOWN" || torrent.AmountLeft == nil {
				return errors.New("unmanaged torrent liability is unknown")
			}
			entries = append(entries, capacityprojection.LedgerEntry{Key: "unmanaged:" + id, Kind: capacityprojection.KindUnmanagedLiability, Bytes: *torrent.AmountLeft})
		}
		projection := capacityprojection.Project(capacityprojection.Input{PhysicalFreeBytes: physical, Entries: entries})
		if projection.State != capacityprojection.StateTrusted || projection.EffectiveFreeBytes == nil {
			return errors.New("capacity projection is uncertain")
		}
		result.EffectiveFree = *projection.EffectiveFreeBytes
		var epochState string
		err = tx.QueryRowContext(ctx, `SELECT capacity_epoch_id,allocation_epoch,capacity_state FROM capacity_epochs WHERE storage_target_id=? ORDER BY allocation_epoch DESC LIMIT 1`, storage.StorageTargetID).Scan(&result.ID, &result.AllocationEpoch, &epochState)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && epochState != "TRUSTED" {
			return errors.New("capacity requires reconciliation")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO capacity_epochs(capacity_epoch_id,storage_target_id,allocation_epoch,capacity_state,physical_free_bytes,effective_free_bytes,snapshot_generation,snapshot_revision,snapshot_digest,created_at) VALUES(?,?,?,'TRUSTED',?,?,?,?,?,?) ON CONFLICT(capacity_epoch_id) DO UPDATE SET physical_free_bytes=excluded.physical_free_bytes,effective_free_bytes=excluded.effective_free_bytes,snapshot_generation=excluded.snapshot_generation,snapshot_revision=excluded.snapshot_revision,snapshot_digest=excluded.snapshot_digest,created_at=excluded.created_at`, result.ID, storage.StorageTargetID, result.AllocationEpoch, physical, result.EffectiveFree, generation, revision, digest, now.Format(time.RFC3339Nano))
		return err
	})
	return result, err
}

func byteCount(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, v >= 0
	case int:
		return int64(v), v >= 0
	case float64:
		if v < 0 || v >= float64(math.MaxInt64) || v != math.Trunc(v) {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil && n >= 0
	default:
		return 0, false
	}
}

func (stage *Stage) recordAlreadyPresent(ctx context.Context, job jobRow, e Evidence, c capacityFact) error {
	if err := stage.prepareJob(job)(ctx, stage.database); err != nil {
		return err
	}
	s := stage.config.Storage
	now := stage.config.Now().UTC()
	torrent := e.Snapshot.Torrents[job.identityKey]
	if torrent == nil || torrent.AmountLeftState != "KNOWN" || torrent.AmountLeft == nil || *torrent.AmountLeft < 0 {
		return errors.New("existing torrent liability is unknown")
	}
	request := reservation.Request{JobID: job.jobID, AttemptID: "attempt-" + job.jobID, SubmitIntentID: "intent-" + job.jobID, ReservationID: "reservation-" + job.jobID, IdentityKey: job.identityKey, InstanceID: s.InstanceID, StorageTargetID: s.StorageTargetID, CapacityEpochID: c.ID, AllocationEpoch: c.AllocationEpoch, CapacityRevision: int64(e.Record.Revision), SnapshotGeneration: int64(e.Record.Generation), SnapshotRevision: int64(e.Record.Revision), SnapshotDigest: e.Record.Digest, RequestFingerprint: "fp-" + job.jobID, TargetFingerprint: "target-" + s.StorageTargetID, PreAddIdentityFact: reservation.IdentityPresent, PreAddQBTorrentID: torrent.ID, FinalCandidateRecheckRevision: int64(e.Record.Revision), MaxInflight: 1, Now: now.Format(time.RFC3339Nano)}
	request.Clock = stage.config.Now
	if _, err := reservation.ReserveAtomic(ctx, stage.database, request); err != nil {
		return err
	}
	_, err := alreadypresent.Record(ctx, stage.database, alreadypresent.Request{BindingID: fmt.Sprintf("binding-%s-%s", s.StorageTargetID, torrent.ID), AttemptID: request.AttemptID, SubmitIntentID: request.SubmitIntentID, ReservationID: request.ReservationID, JobID: job.jobID, StorageTargetID: s.StorageTargetID, InstanceID: s.InstanceID, IdentityKey: job.identityKey, QBTorrentID: torrent.ID, AmountLeftKnown: true, AmountLeft: *torrent.AmountLeft, UncertaintyDebitKind: alreadypresent.DebitKindNone, Now: now})
	return err
}
