package dispatchstage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

// Coordinator serializes selection and dispatch for all configured instances.
// The reservation transaction remains the final authority across restarts.
type Coordinator struct {
	mu     sync.Mutex
	stages []*Stage
}

func Connect(stages ...*Stage) {
	c := &Coordinator{stages: stages}
	for _, s := range stages {
		s.coordinator = c
	}
}

func (c *Coordinator) dispatch(ctx context.Context, origin *Stage, job jobRow) (*dispatchorchestrator.Result, error) {
	var raw string
	var size int64
	if err := origin.database.QueryRowContext(ctx, `SELECT aggregation_json,content_size FROM job_rule_snapshots WHERE job_id=?`, job.jobID).Scan(&raw, &size); err != nil {
		return nil, err
	}
	var decision ruleaggregate.Result
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return nil, err
	}
	if decision.Winner.TargetMode != "INSTANCE_GROUP" {
		return origin.dispatchJob(ctx, job)
	}
	var membersRaw string
	if err := origin.database.QueryRowContext(ctx, `SELECT members_json FROM instance_groups WHERE group_id=?`, decision.Winner.TargetGroupID).Scan(&membersRaw); err != nil {
		return nil, fmt.Errorf("group unavailable")
	}
	var members []string
	if err := json.Unmarshal([]byte(membersRaw), &members); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, id := range members {
		allowed[id] = true
	}
	input := placement.DecisionInput{IdentityKey: job.identityKey, TargetMode: placement.TargetInstanceGroup, RequiredBytes: size, Weights: placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1_000_000, CurrentDownloadLoadPenaltyWeight: 1_000_000, RecentAddLatencyPenaltyWeight: 1}}
	byID := map[string]*Stage{}
	var existing *Stage
	for _, s := range c.stages {
		if expired, err := reservation.ExpireParticipation(ctx, origin.database, job.jobID, origin.config.Now); err != nil || expired {
			return nil, err
		}
		id := s.config.Storage.InstanceID
		if !allowed[id] || s.config.Profile.ProfileVerificationState != "VERIFIED" {
			continue
		}
		if _, err := s.actionParams(decision); err != nil {
			continue
		}
		collect := s.config.CollectForSelection
		if collect == nil {
			collect = s.config.Collect
		}
		e, release, err := collect(ctx)
		if expired, expiryErr := reservation.ExpireParticipation(ctx, origin.database, job.jobID, origin.config.Now); expiryErr != nil || expired {
			if release != nil {
				release()
			}
			return nil, expiryErr
		}
		if err == nil {
			// Presence survives an uncertain capacity projection. A known existing
			// identity must not be sent to another member merely because its host
			// has an unrelated task with unknown remaining bytes.
			if snapshotErr := s.validateSnapshot(ctx, s.database, e); snapshotErr != nil {
				err = snapshotErr
			} else if _, present := e.Snapshot.Torrents[job.identityKey]; present {
				existing = s
			}
		}
		if err == nil {
			var cap capacityFact
			cap, err = s.prepareCapacity(ctx, e)
			if err == nil {
				candidate := s.placementInput(job.identityKey, size, cap.EffectiveFree).Candidates[0]
				physical, valid := byteCount(e.Snapshot.ServerState["free_space_on_disk"])
				if valid && physical > 0 {
					candidate.FreeRatioPPM = int64(float64(cap.EffectiveFree) / float64(physical) * 1_000_000)
					if candidate.FreeRatioPPM < 0 {
						candidate.FreeRatioPPM = 0
					}
				}
				for _, t := range e.Snapshot.Torrents {
					if t.AmountLeft != nil && *t.AmountLeft > 0 {
						candidate.CurrentDownloadLoad++
					}
				}
				err = s.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement_attempts WHERE instance_id=? AND attempt_state IN ('RESERVED','ADD_SENT','AWAITING_CONFIRMATION','UNKNOWN','EXPIRED_UNKNOWN')`, id).Scan(&candidate.Inflight)
				if err == nil {
					input.Candidates = append(input.Candidates, candidate)
					byID[id] = s
				}
			}
		}
		if release != nil {
			release()
		}
	}
	if expired, err := reservation.ExpireParticipation(ctx, origin.database, job.jobID, origin.config.Now); err != nil || expired {
		return nil, err
	}
	if existing != nil {
		return existing.dispatchJob(ctx, job)
	}
	selection, err := placement.Decide(input)
	if err != nil {
		return nil, err
	}
	if selection.Selected == nil {
		_, err = origin.database.ExecContext(ctx, `UPDATE race_jobs SET job_state='WAITING_CAPACITY',placement_state='WAITING_CAPACITY',placement_wait_reason='GROUP_NO_ELIGIBLE_TARGET' WHERE job_id=? AND current_attempt_id IS NULL AND job_state IN ('DISCOVERED','READY','WAITING_CAPACITY')`, job.jobID)
		return nil, err
	}
	return byID[selection.Selected.Candidate.InstanceID].dispatchJob(ctx, job)
}

// SetPolicyProvisioner must be called before starting workers.
func (s *Stage) SetPolicyProvisioner(fn func(context.Context, ruleaggregate.Match) (string, error)) {
	s.config.EnsurePolicy = fn
}

// SetSelectionCollector must be called before starting workers.
func (s *Stage) SetSelectionCollector(fn func(context.Context) (Evidence, func(), error)) {
	s.config.CollectForSelection = fn
}
