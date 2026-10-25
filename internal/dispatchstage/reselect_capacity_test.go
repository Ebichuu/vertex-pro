package dispatchstage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func setReselectCapacityEvidence(t *testing.T, f selectionFixture, s *Stage, now *time.Time, evidence Evidence) {
	t.Helper()
	if _, err := f.database.Exec(`UPDATE sync_instance_state SET snapshot_revision=?,rid=?,snapshot_digest=?,updated_at=? WHERE instance_id=?`, evidence.Record.Revision, evidence.Record.RID, evidence.Record.Digest, now.Format(time.RFC3339Nano), evidence.Record.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES(?,?,?,?,?,'fixture.json',1,?)`, evidence.Record.InstanceID, evidence.Record.Generation, evidence.Record.Revision, evidence.Record.RID, evidence.Record.Digest, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	s.config.Collect = func(context.Context) (Evidence, func(), error) {
		evidence.CollectedAt = *now
		return evidence, nil, nil
	}
	s.config.CollectForSelection = s.config.Collect
}

func TestReselectDoesNotChargeExistingReservationTwice(t *testing.T) {
	for _, physical := range []int64{42, 50} {
		t.Run(fmt.Sprint(physical), func(t *testing.T) {
			f, a, b, now := reselectFixture(t)
			setReselectCapacityEvidence(t, f, a, now, f.evidence("qb-1", 2, physical))
			runSelection(t, a)
			*now = now.Add(31 * time.Second)
			runSelection(t, b)
			checkReselectCapacityOwner(t, f, "attempt-job-1", "qb-1", 1)
			if since := reselectUnavailableSince(t, f); since.Valid {
				t.Fatalf("valid existing reservation marked unavailable: %s", since.String)
			}
			sender := &deadlineSender{}
			a.config.Sender = sender
			if worked, err := a.SendNext(context.Background()); !worked || err != nil || sender.calls != 1 {
				t.Fatalf("send=%v err=%v calls=%d", worked, err, sender.calls)
			}
		})
	}
}

func TestReselectCapacityRecoveryResetsUnavailableWindow(t *testing.T) {
	f, a, b, now := reselectFixture(t)
	setReselectCapacityEvidence(t, f, a, now, f.evidence("qb-1", 2, 41))
	runSelection(t, a)
	*now = now.Add(29 * time.Second)
	setReselectCapacityEvidence(t, f, a, now, f.evidence("qb-1", 3, 42))
	runSelection(t, b)
	if since := reselectUnavailableSince(t, f); since.Valid {
		t.Fatalf("capacity recovery retained unavailable clock: %s", since.String)
	}
	*now = now.Add(time.Second)
	setReselectCapacityEvidence(t, f, a, now, f.evidence("qb-1", 4, 41))
	runSelection(t, a)
	*now = now.Add(29 * time.Second)
	runSelection(t, b)
	checkReselectCapacityOwner(t, f, "attempt-job-1", "qb-1", 1)
	*now = now.Add(time.Second)
	runSelection(t, b)
	checkReselectCapacityOwner(t, f, "attempt-job-1-2", "qb-2", 2)
}

func checkReselectCapacityOwner(t *testing.T, f selectionFixture, attempt, instance string, attempts int) {
	t.Helper()
	var owner, target, state string
	var count int
	if err := f.database.QueryRow(`SELECT j.current_attempt_id,a.instance_id,j.job_state FROM race_jobs j JOIN placement_attempts a ON a.attempt_id=j.current_attempt_id WHERE j.job_id='job-1'`).Scan(&owner, &target, &state); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if owner != attempt || target != instance || state != "RESERVED" || count != attempts {
		t.Fatalf("owner=%s target=%s state=%s attempts=%d; want %s %s RESERVED %d", owner, target, state, count, attempt, instance, attempts)
	}
}

func reselectUnavailableSince(t *testing.T, f selectionFixture) sql.NullString {
	t.Helper()
	var since sql.NullString
	if err := f.database.QueryRow(`SELECT unavailable_since FROM dispatch_queue WHERE attempt_id='attempt-job-1'`).Scan(&since); err != nil {
		t.Fatal(err)
	}
	return since
}

func TestReselectCapacityLossMovesToHealthyTarget(t *testing.T) {
	f, a, b, now := reselectFixture(t)
	setReselectCapacityEvidence(t, f, a, now, f.evidence("qb-1", 2, 41))
	oldSender, newSender := &deadlineSender{}, &deadlineSender{}
	a.config.Sender, b.config.Sender = oldSender, newSender
	if worked, err := a.SendNext(context.Background()); !worked || err == nil || !strings.Contains(err.Error(), "capacity projection is uncertain") || oldSender.calls != 0 {
		t.Fatalf("underfunded sender worked=%v err=%v calls=%d", worked, err, oldSender.calls)
	}
	runSelection(t, a)
	if since := reselectUnavailableSince(t, f); !since.Valid {
		t.Fatal("capacity loss did not start the unavailable clock")
	}
	*now = now.Add(29 * time.Second)
	runSelection(t, b)
	checkReselectCapacityOwner(t, f, "attempt-job-1", "qb-1", 1)
	*now = now.Add(time.Second)
	runSelection(t, b)
	checkReselectCapacityOwner(t, f, "attempt-job-1-2", "qb-2", 2)
	var active, released, debt int64
	if err := f.database.QueryRow(`SELECT count(*) FILTER(WHERE reservation_state='ACTIVE'),count(*) FILTER(WHERE reservation_state='RELEASED' AND release_reason='NO_EFFECT'),sum(CASE WHEN reservation_state='ACTIVE' THEN reserved_bytes ELSE 0 END) FROM capacity_reservations`).Scan(&active, &released, &debt); err != nil {
		t.Fatal(err)
	}
	if active != 1 || released != 1 || debt != 42 {
		t.Fatalf("active=%d released=%d debt=%d", active, released, debt)
	}
	if worked, err := a.SendNext(context.Background()); worked || err != nil || oldSender.calls != 0 {
		t.Fatalf("old sender worked=%v err=%v calls=%d", worked, err, oldSender.calls)
	}
	if worked, err := b.SendNext(context.Background()); !worked || err != nil || newSender.calls != 1 {
		t.Fatalf("new sender worked=%v err=%v calls=%d", worked, err, newSender.calls)
	}
}
