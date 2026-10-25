package startuprecover

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecoverRejectsInvalidCurrentPointerWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name, mutation, violation string
	}{
		{"terminal_self", `UPDATE placement_attempts SET attempt_state='ADD_FAILED', side_effect_fact='FAILED_NO_EFFECT' WHERE attempt_id='attempt-1'`, "JOB_POINTER_NOT_CLEARED:job-1"},
		{"missing", `UPDATE race_jobs SET current_attempt_id='missing' WHERE job_id='job-1'`, "JOB_CURRENT_ATTEMPT_INVALID:job-1"},
		{"foreign_job", `UPDATE race_jobs SET current_attempt_id=NULL,job_state='READY' WHERE job_id='job-2'; UPDATE race_jobs SET current_attempt_id='attempt-2' WHERE job_id='job-1'`, "JOB_CURRENT_ATTEMPT_INVALID:job-1"},
		{"state_mismatch", `UPDATE race_jobs SET job_state='UNKNOWN' WHERE job_id='job-1'`, "JOB_CURRENT_ATTEMPT_INVALID:job-1"},
		{"identity_mismatch", `UPDATE race_jobs SET identity_key='different-identity' WHERE job_id='job-1'`, "JOB_CURRENT_ATTEMPT_INVALID:job-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openMigrated(t)
			seedChain(t, database, "1", "RESERVED", "NOT_STARTED", false, "")
			seedChain(t, database, "2", "RESERVED", "NOT_STARTED", false, "")
			// Simulate a damaged imported database. Production triggers and FK
			// enforcement remain unchanged; Recover must independently fail closed.
			if _, err := database.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER race_job_current_attempt_intent_update_guard; ` + test.mutation); err != nil {
				t.Fatal(err)
			}
			input := Input{Now: time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}
			result, err := Recover(context.Background(), database, input)
			if !errors.Is(err, ErrRecoveryFailed) || !strings.Contains(strings.Join(result.Violations, ","), test.violation) {
				t.Fatalf("violations=%v err=%v; want %s", result.Violations, err, test.violation)
			}
			var writes int
			if err := database.QueryRow(`SELECT (SELECT count(*) FROM sync_waiters)+(SELECT count(*) FROM race_events)`).Scan(&writes); err != nil {
				t.Fatal(err)
			}
			if writes != 0 {
				t.Fatalf("recovery wrote %d rows despite invalid pointer", writes)
			}
		})
	}
}
