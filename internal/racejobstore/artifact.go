package racejobstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func artifactDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func saveArtifact(ctx context.Context, tx *db.ImmediateTx, jobID string, body []byte, now string) error {
	if len(body) == 0 || int64(len(body)) > torrentdownload.DefaultMaxArtifactBytes {
		return errors.New("invalid job torrent artifact size")
	}
	digest := artifactDigest(body)
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_torrent_artifacts(job_id,body,sha256,created_at) VALUES(?,?,?,?) ON CONFLICT(job_id) DO NOTHING`, jobID, body, digest, now); err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT sha256 FROM job_torrent_artifacts WHERE job_id=?`, jobID).Scan(&stored); err != nil {
		return err
	}
	if stored != digest {
		return errors.New("immutable job torrent artifact conflict")
	}
	return nil
}

// LoadArtifact returns only verified bytes. Corruption must not trigger a network
// fallback which could silently replace a job's frozen torrent.
func LoadArtifact(ctx context.Context, database *sql.DB, jobID string) ([]byte, bool, error) {
	var body []byte
	var digest string
	err := database.QueryRowContext(ctx, `SELECT body,sha256 FROM job_torrent_artifacts WHERE job_id=?`, jobID).Scan(&body, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(body) == 0 || int64(len(body)) > torrentdownload.DefaultMaxArtifactBytes || artifactDigest(body) != digest {
		return nil, true, errors.New("job torrent artifact integrity failure")
	}
	return body, true, nil
}

// SaveLegacyArtifact freezes the first verified download for pre-migration jobs.
func SaveLegacyArtifact(ctx context.Context, database *sql.DB, jobID string, body []byte, now time.Time) error {
	return db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
		return saveArtifact(ctx, tx, jobID, body, now.UTC().Format(time.RFC3339Nano))
	})
}
