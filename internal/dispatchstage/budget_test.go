package dispatchstage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/reservation"
)

func TestCapacityRefreshAndReleasePreserveCurrentDebts(t *testing.T) {
	f, s, _ := durableFixture(t)
	allocateQueue(t, s)
	e := f.evidence("qb-1", 1, 1<<30)
	for range 3 {
		if err := s.RefreshCapacity(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		var free int64
		if err := f.database.QueryRow(`SELECT effective_free_bytes FROM capacity_epochs`).Scan(&free); err != nil || free != (1<<30)-42 {
			t.Fatalf("double charge or lost reservation: free=%d err=%v", free, err)
		}
	}
	if _, err := reservation.ExpireParticipation(context.Background(), f.database, "job-1", func() time.Time { return f.now.Add(time.Hour) }); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCapacity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var free int64
	if err := f.database.QueryRow(`SELECT effective_free_bytes FROM capacity_epochs`).Scan(&free); err != nil || free != 1<<30 {
		t.Fatalf("release refresh=%d err=%v", free, err)
	}
}

func TestUnknownThresholdBlocksOnlyItsInstanceAndMalformedHeadDoesNotBlock(t *testing.T) {
	f, s, calls := durableFixture(t)
	cloneQueuedJob(t, f, "job-2", "qb-1")
	allocateQueue(t, s)
	if _, err := f.database.Exec(`UPDATE dispatch_queue SET params_json='{}' WHERE job_id='job-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendNext(context.Background()); err == nil {
		t.Fatal("corrupt queued request accepted")
	}
	if _, err := s.SendNext(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("bad head blocked healthy queued request: %v calls=%d", err, calls.Load())
	}
	// Model one recovered UNKNOWN; ordinary pending work can still be admitted.
	if _, err := f.database.Exec(`UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE job_id='job-2'; UPDATE race_jobs SET job_state='UNKNOWN' WHERE job_id='job-2'`); err != nil {
		t.Fatal(err)
	}
	if err := s.sendAdmission(context.Background(), f.database); err != nil {
		t.Fatalf("one unknown consumed the ordinary queue: %v", err)
	}
	if _, err := f.database.Exec(`UPDATE placement_attempts SET attempt_state='UNKNOWN',dispatch_state='STARTED',side_effect_fact='POSSIBLE_EFFECT' WHERE job_id='job-1'; UPDATE race_jobs SET job_state='UNKNOWN' WHERE job_id='job-1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.sendAdmission(context.Background(), f.database); err == nil || !strings.Contains(err.Error(), "UNKNOWN_LIMIT_REACHED") {
		t.Fatalf("unknown threshold=%v", err)
	}
	b, _ := f.stage(t, "qb-2", f.evidence("qb-2", 1, 1<<30), f.evidence("qb-2", 2, 1<<30))
	if err := b.sendAdmission(context.Background(), f.database); err != nil {
		t.Fatalf("A stopped B: %v", err)
	}
}

func TestRepeatedSendFailuresPauseOnlyTargetUntilCooldown(t *testing.T) {
	f, s, _ := durableFixture(t)
	cloneQueuedJob(t, f, "job-2", "qb-1")
	cloneQueuedJob(t, f, "job-3", "qb-1")
	allocateQueue(t, s)
	if _, err := f.database.Exec(`UPDATE placement_attempts SET attempt_state='ADD_FAILED',side_effect_fact='FAILED_NO_EFFECT'; UPDATE submit_intents SET response_fact_at=?`, f.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := s.sendAdmission(context.Background(), f.database); err == nil || !strings.Contains(err.Error(), "INSTANCE_SEND_CIRCUIT_OPEN") {
		t.Fatalf("consecutive failure gate=%v", err)
	}
	b, _ := f.stage(t, "qb-2", f.evidence("qb-2", 1, 1<<30), f.evidence("qb-2", 2, 1<<30))
	if err := b.sendAdmission(context.Background(), f.database); err != nil {
		t.Fatalf("A failure paused B: %v", err)
	}
	s.config.Now = func() time.Time { return f.now.Add(time.Minute) }
	if err := s.sendAdmission(context.Background(), f.database); err != nil {
		t.Fatalf("gate did not cool down: %v", err)
	}
}

func TestQueueRetainsUnsentWorkWhenProfileExpiresOrInstanceIsDisabled(t *testing.T) {
	for _, reason := range []string{"profile-expired", "instance-disabled"} {
		t.Run(reason, func(t *testing.T) {
			f, s, calls := durableFixture(t)
			allocateQueue(t, s)
			if reason == "profile-expired" {
				s.SetProfileExpiry(f.now)
			} else if _, err := f.database.Exec(`INSERT INTO instance_metadata(instance_id,name,enabled,updated_at) VALUES('qb-1','disabled',0,'now')`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SendNext(context.Background()); err == nil {
				t.Fatal("invalid target accepted")
			}
			var queued, unsent int
			if err := f.database.QueryRow(`SELECT (SELECT count(*) FROM dispatch_queue),(SELECT count(*) FROM submit_intents WHERE dispatch_state='NOT_STARTED')`).Scan(&queued, &unsent); err != nil {
				t.Fatal(err)
			}
			if queued != 1 || unsent != 1 || calls.Load() != 0 {
				t.Fatalf("queued=%d unsent=%d calls=%d", queued, unsent, calls.Load())
			}
		})
	}
}
