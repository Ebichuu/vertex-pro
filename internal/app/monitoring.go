package app

import (
	"context"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/statsstore"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// RefreshMonitoring reads only qui's control API. Missing data leaves the last
// sample timestamp unchanged, so the API correctly reports stale rates.
func (runtime *Runtime) RefreshMonitoring(ctx context.Context, now time.Time, selectedSnapshot *syncmerge.Snapshot) error {
	if runtime.controlClient == nil {
		return nil
	}
	instances, err := runtime.controlClient.Instances(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	selectedFound := false
	for _, instance := range instances {
		if _, err := runtime.Database.ExecContext(ctx, `INSERT INTO instance_metadata(instance_id,name,enabled,updated_at) VALUES(?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,updated_at=excluded.updated_at`, instance.ID, instance.Name, instance.Enabled, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		// Instances without a configured proxy remain UNKNOWN: control-plane
		// connectivity does not establish a fresh torrent snapshot.
		if _, err := runtime.Database.ExecContext(ctx, `INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES(?,'OFFLINE',0,0,0,'',?) ON CONFLICT(instance_id) DO NOTHING`, instance.ID, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if instance.ID != runtime.instanceID {
			continue
		}
		selectedFound = true
		if !instance.Enabled {
			if err := updateSyncState(ctx, runtime.Database, runtime.instanceID, "OFFLINE", now); err != nil {
				return err
			}
			firstErr = fmt.Errorf("configured qui instance is disabled")
			continue
		}
		transfer, err := runtime.controlClient.TransferInfo(ctx, instance.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read instance transfer: %w", err)
			}
			continue
		}
		state := "OFFLINE"
		if err := runtime.Database.QueryRowContext(ctx, `SELECT sync_state FROM sync_instance_state WHERE instance_id=?`, instance.ID).Scan(&state); err != nil {
			return err
		}
		card := realtimecards.Card{InstanceID: instance.ID, SyncState: state, DownloadRate: transfer.DownloadSpeed, UploadRate: transfer.UploadSpeed, Source: "TRANSFER_INFO"}
		if instance.ID == runtime.instanceID && selectedSnapshot != nil {
			card.CountsKnown = true
			for _, torrent := range selectedSnapshot.Torrents {
				status, _ := torrent.Fields["state"].(string)
				if status == "downloading" || status == "forcedDL" || status == "stalledDL" || status == "metaDL" || status == "forcedMetaDL" {
					card.DownloadingCount++
				}
				if status == "uploading" || status == "stalledUP" || status == "queuedUP" || status == "forcedUP" {
					card.SeedingCount++
				}
			}
		}
		if err := statsstore.RecordCounters(ctx, runtime.Database, instance.ID, now, transfer.Downloaded, transfer.Uploaded, transfer.DownloadSpeed, transfer.UploadSpeed); err != nil {
			return err
		}
		runtime.realtime.Update(card, now)
	}
	if !selectedFound {
		if err := updateSyncState(ctx, runtime.Database, runtime.instanceID, "OFFLINE", now); err != nil {
			return err
		}
		return fmt.Errorf("configured qui instance is missing")
	}
	return firstErr
}
