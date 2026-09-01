package capacityreconciler

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestReconcilePublishesNewTrustedEpoch(t *testing.T) {
	database := openReconcileDatabase(t)
	seedEpochFixture(t, database, false)
	result, err := Reconcile(context.Background(), database, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateTrusted || result.CapacityEpochID != "epoch-1-reconciled-2" || result.AllocationEpoch != 2 || result.EffectiveFreeBytes == nil || *result.EffectiveFreeBytes != 890 {
		t.Fatalf("result = %+v", result)
	}
	var oldState, newState string
	if err := database.QueryRow(`SELECT capacity_state FROM capacity_epochs WHERE capacity_epoch_id = 'epoch-1'`).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT capacity_state FROM capacity_epochs WHERE capacity_epoch_id = 'epoch-1-reconciled-2'`).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	if oldState != StateReconciling || newState != StateTrusted {
		t.Fatalf("old=%s new=%s", oldState, newState)
	}
}

func TestReconcileRemainsFailClosedUntilStable(t *testing.T) {
	database := openReconcileDatabase(t)
	seedEpochFixture(t, database, false)
	request := validRequest()
	request.StableSnapshotCount = 1
	result, err := Reconcile(context.Background(), database, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateReconciling || len(result.Reasons) != 1 || result.Reasons[0] != "STABLE_SNAPSHOT_COUNT_INSUFFICIENT" {
		t.Fatalf("result = %+v", result)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_epochs WHERE storage_target_id = 'target-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("epoch count = %d", count)
	}
}

func TestReconcileRejectsUnknownAndOvercommittedLedger(t *testing.T) {
	database := openReconcileDatabase(t)
	seedEpochFixture(t, database, true)
	result, err := Reconcile(context.Background(), database, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateReconciling || !strings.Contains(strings.Join(result.Reasons, ","), "UNKNOWN_AMOUNT_LEFT_PRESENT") {
		t.Fatalf("result = %+v", result)
	}

	database2 := openReconcileDatabase(t)
	seedEpochFixture(t, database2, false)
	request := validRequest()
	request.SafetyMarginBytes = 2000
	result, err = Reconcile(context.Background(), database2, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateUncertain || !strings.Contains(strings.Join(result.Reasons, ","), "PROJECTED_LIABILITY_EXCEEDS_PHYSICAL_FREE") {
		t.Fatalf("result = %+v", result)
	}
	var count int
	if err := database2.QueryRow(`SELECT COUNT(*) FROM capacity_epochs WHERE storage_target_id = 'target-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("epoch count = %d", count)
	}
}

func validRequest() Request {
	return Request{StorageTargetID: "target-1", CapacityEpochID: "epoch-1", PhysicalFreeBytes: 1000, SnapshotGeneration: 3, SnapshotRevision: 4, SnapshotDigest: "digest-3", StableSnapshotCount: 2, CompleteBaseline: true, OwnersUnique: true, LiabilitiesCovered: true, NoUnknownAmountLeft: true, NoUnquantifiedRisk: true, SafetyMarginBytes: 110, CreatedAt: "2026-08-25T02:00:00Z"}
}

func openReconcileDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "reconcile.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func seedEpochFixture(t *testing.T, database *sql.DB, unknownBinding bool) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1', 'qb-1', '/downloads', 'VERIFIED', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1', 'target-1', 1, 'RECONCILING', NULL, NULL, 2, 3, 'digest-2', ?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if unknownBinding {
		if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, amount_left_state, amount_left_error_code, uncertainty_debit_kind, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-1', 'target-1', 'torrent-1', 'UNMANAGED', 'UNKNOWN', 'MISSING_IN_FULL_SNAPSHOT', 'UNBOUNDED_AMOUNT_LEFT', 2, 3, 'digest-2', ?)`, now); err != nil {
			t.Fatal(err)
		}
	}
}
