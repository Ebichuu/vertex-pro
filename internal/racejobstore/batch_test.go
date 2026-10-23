package racejobstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"sync"
	"testing"
	"time"
)

func TestBatchLimitIsAtomicAcrossCreatorsAndRevisions(t *testing.T) {
	database := storeDatabase(t)
	var workers sync.WaitGroup
	for i := 0; i < 30; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			store, _ := New(database)
			creator, _ := racejob.New(store, nil)
			raw, _ := json.Marshal(ruleaggregate.Result{Winner: ruleaggregate.Match{RuleID: "batch", RuleRevision: i + 1, MaxJobs: 10}})
			_, err := creator.Create(context.Background(), racejob.Input{SourceID: fmt.Sprint("source-", i), IdentityKey: fmt.Sprint("hash-", i), ParticipationDeadline: time.Now().Add(time.Hour), ObservationDeadline: time.Now().Add(2 * time.Hour), RuleSnapshot: raw, ContentSize: 1024})
			if err != nil && !errors.Is(err, racejob.ErrBatchLimit) {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	var jobs, snapshots int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM job_rule_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if jobs != 10 || snapshots != 10 {
		t.Fatalf("jobs=%d snapshots=%d", jobs, snapshots)
	}
}
