package dispatchstage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

type queuedParams struct {
	Params   qbadd.AddParams
	Profile  qbprofile.Profile
	Storage  StorageConfig
	Decision ruleaggregate.Result
}

func (s *Stage) Wake() {
	for i := 0; i < senderConcurrency; i++ {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (s *Stage) Notifications() <-chan struct{} { return s.wake }

// Wake at the earliest persisted retry deadline, while retaining a one-second
// recovery poll if a notification is lost. Never poll faster just to wait on
// an already-active HTTP request.
func (s *Stage) RetryDelay(ctx context.Context) time.Duration {
	var at string
	err := s.database.QueryRowContext(ctx, `SELECT COALESCE(MIN(next_attempt_at),'') FROM dispatch_queue q JOIN placement_attempts a ON a.attempt_id=q.attempt_id WHERE q.instance_id=? AND a.attempt_state='RESERVED' AND a.dispatch_state='NOT_STARTED' AND next_attempt_at>?`, s.config.Storage.InstanceID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")).Scan(&at)
	if err != nil || at == "" {
		return time.Second
	}
	due, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Second
	}
	return max(time.Millisecond, min(time.Second, time.Until(due)))
}

// SendNext owns only one instance's HTTP request. A durable Begin arbitrates
// duplicate claimants, including another process, before any network effect.
func (s *Stage) SendNext(ctx context.Context) (worked bool, resultErr error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return false, nil
	}
	defer func() { <-s.slots }()
	s.queueMu.Lock()
	// Retry pacing uses wall time independently of the domain/evidence clock.
	rows, err := s.database.QueryContext(ctx, `SELECT q.attempt_id,q.job_id,q.params_json FROM dispatch_queue q JOIN placement_attempts a ON a.attempt_id=q.attempt_id WHERE q.instance_id=? AND a.attempt_state='RESERVED' AND a.dispatch_state='NOT_STARTED' AND q.next_attempt_at<=? ORDER BY q.queued_at,q.attempt_id LIMIT 32`, s.config.Storage.InstanceID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
	if err != nil {
		s.queueMu.Unlock()
		return false, err
	}
	var attempt, jobID, raw string
	for rows.Next() {
		var a, j, p string
		if err = rows.Scan(&a, &j, &p); err != nil {
			break
		}
		if !s.active[a] {
			attempt, jobID, raw = a, j, p
			break
		}
	}
	scanErr := rows.Err()
	rows.Close()
	if attempt != "" {
		s.active[attempt] = true
	}
	s.queueMu.Unlock()
	if err != nil || scanErr != nil {
		return false, errors.Join(err, scanErr)
	}
	if attempt == "" {
		return false, nil
	}
	defer func() {
		worked = true
		if resultErr != nil {
			_, _ = s.database.ExecContext(ctx, `UPDATE dispatch_queue SET next_attempt_at=? WHERE attempt_id=?`, time.Now().Add(100*time.Millisecond).UTC().Format("2006-01-02T15:04:05.000000000Z"), attempt)
		}
	}()
	defer func() { s.queueMu.Lock(); delete(s.active, attempt); s.queueMu.Unlock() }()
	if expired, err := reservation.ExpireParticipation(ctx, s.database, jobID, s.config.Now); err != nil || expired {
		return expired, err
	}
	enabled, err := s.ruleEnabled(ctx, jobID)
	if err != nil {
		return false, err
	}
	if !enabled {
		return true, s.cancelQueued(ctx, attempt, jobID, "RULE_DISABLED_BEFORE_SEND")
	}
	if err := s.sendAdmission(ctx, s.database); err != nil {
		return false, err
	}
	var frozen queuedParams
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return false, err
	}
	actual, _ := json.Marshal(queuedParams{Params: frozen.Params, Profile: s.config.Profile, Storage: s.config.Storage, Decision: frozen.Decision})
	expected, _ := json.Marshal(frozen)
	if string(actual) != string(expected) {
		return false, errors.New("QUEUED_TARGET_CONFIGURATION_CHANGED")
	}
	if frozen.Decision.Winner.SeedTimeSeconds > 0 {
		if s.config.EnsurePolicy == nil {
			return false, errors.New("POLICY_NOT_READY")
		}
		if _, err := s.config.EnsurePolicy(ctx, frozen.Decision.Winner); err != nil {
			return false, err
		}
	}
	body, found, err := racejobstore.LoadArtifact(ctx, s.database, jobID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("QUEUED_ARTIFACT_MISSING")
	}
	parsed, err := torrentparse.Parse(body, torrentparse.DefaultLimits())
	if err != nil {
		return false, err
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		return false, err
	}
	var hash, intent, waiter string
	var size int64
	if err := s.database.QueryRowContext(ctx, `SELECT a.identity_key,i.submit_intent_id,w.waiter_id,a.reserved_bytes FROM placement_attempts a JOIN submit_intents i ON i.attempt_id=a.attempt_id JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.attempt_id=?`, attempt).Scan(&hash, &intent, &waiter, &size); err != nil {
		return false, err
	}
	if hash != identity.V1 || size != parsed.TotalSize {
		return false, errors.New("QUEUED_ARTIFACT_CHANGED")
	}
	evidence, release, err := s.config.Collect(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return false, err
	}
	if err := s.validateSnapshot(ctx, s.database, evidence); err != nil {
		return false, err
	}
	if err := s.validateSavePath(frozen.Params.SavePath, evidence); err != nil {
		return false, err
	}
	if _, present := evidence.Snapshot.Torrents[hash]; present {
		return true, s.cancelQueued(ctx, attempt, jobID, "IDENTITY_PRESENT_BEFORE_SEND")
	}
	if _, err := s.prepareCapacity(ctx, evidence); err != nil {
		return false, err
	}
	request, err := qbadd.BuildRequest(s.config.Profile, qbadd.TorrentArtifact{Filename: parsed.Name + ".torrent", Content: body}, frozen.Params)
	if err != nil {
		return false, err
	}
	runner, err := dispatchflow.New(s.database, s.config.Sender, s.config.Now)
	if err != nil {
		return false, err
	}
	runner.SetReceiptRecovery(s.receipts)
	_, err = runner.Run(ctx, dispatchflow.Request{AttemptID: attempt, SubmitIntentID: intent, WaiterID: waiter, APIVersion: s.config.Profile.WebAPIVersion, HTTPRequest: request,
		Recheck: func(ctx context.Context, tx *db.ImmediateTx) error {
			if err := s.validateSnapshot(ctx, tx, evidence); err != nil {
				return err
			}
			if err := s.validateSavePath(frozen.Params.SavePath, evidence); err != nil {
				return err
			}
			var trusted int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM capacity_epochs WHERE storage_target_id=? AND capacity_state='TRUSTED' AND snapshot_generation=? AND snapshot_revision=? AND snapshot_digest=?`, s.config.Storage.StorageTargetID, evidence.Record.Generation, evidence.Record.Revision, evidence.Record.Digest).Scan(&trusted); err != nil {
				return err
			}
			if trusted != 1 {
				return errors.New("CAPACITY_CHANGED_BEFORE_SEND")
			}
			enabled, err := s.ruleEnabledWith(ctx, tx, jobID)
			if err != nil {
				return err
			}
			if !enabled {
				return errors.New("RULE_DISABLED_BEFORE_SEND")
			}
			if frozen.Decision.Winner.SeedTimeSeconds > 0 {
				if _, err := s.config.EnsurePolicy(ctx, frozen.Decision.Winner); err != nil {
					return err
				}
			}
			if frozen.Decision.Winner.TargetMode == "INSTANCE_GROUP" {
				var members string
				if err := tx.QueryRowContext(ctx, `SELECT members_json FROM instance_groups WHERE group_id=?`, frozen.Decision.Winner.TargetGroupID).Scan(&members); err != nil {
					return err
				}
				var ids []string
				if err := json.Unmarshal([]byte(members), &ids); err != nil {
					return err
				}
				allowed := false
				for _, id := range ids {
					allowed = allowed || id == s.config.Storage.InstanceID
				}
				if !allowed {
					return errors.New("GROUP_MEMBERSHIP_CHANGED")
				}
			}
			if err := s.sendAdmission(ctx, tx); err != nil {
				return err
			}
			// Absence must be based on the last validated pre-send snapshot,
			// not the snapshot from when this request entered the queue.
			if _, err := tx.ExecContext(ctx, `UPDATE submit_intents SET pre_add_sync_generation=?,pre_add_snapshot_revision=?,pre_add_snapshot_digest=? WHERE submit_intent_id=? AND dispatch_state='NOT_STARTED'`, evidence.Record.Generation, evidence.Record.Revision, evidence.Record.Digest, intent); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE race_jobs SET final_candidate_recheck_revision=?,final_candidate_recheck_at=? WHERE job_id=?`, evidence.Record.Revision, s.config.Now().UTC().Format(time.RFC3339Nano), jobID); err != nil {
				return err
			}
			now := s.config.Now().UTC()
			if _, err := tx.ExecContext(ctx, `UPDATE sync_waiters SET registered_generation=?,source_snapshot_revision=?,confirmation_deadline=?,reconciliation_deadline=? WHERE waiter_id=? AND waiter_state='ACTIVE'`, evidence.Record.Generation, evidence.Record.Revision, now.Add(s.config.ConfirmWindow).Format(time.RFC3339Nano), now.Add(s.config.ReconcileWindow).Format(time.RFC3339Nano), waiter); err != nil {
				return err
			}
			// Queue deletion and the irreversible STARTED marker commit together.
			_, err = tx.ExecContext(ctx, `DELETE FROM dispatch_queue WHERE attempt_id=?`, attempt)
			return err
		}})
	if errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		return true, nil
	}
	return err == nil, err
}

func (s *Stage) cancelQueued(ctx context.Context, attempt, job, reason string) error {
	return db.RunImmediate(ctx, s.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		var safe int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM placement_attempts a JOIN submit_intents i ON i.attempt_id=a.attempt_id JOIN capacity_reservations r ON r.attempt_id=a.attempt_id WHERE a.attempt_id=? AND a.job_id=? AND a.attempt_state='RESERVED' AND a.dispatch_state='NOT_STARTED' AND i.dispatch_state='NOT_STARTED' AND i.response_fact_at IS NULL AND r.reservation_state='ACTIVE' AND r.reflected_physical_bytes=0 AND NOT EXISTS(SELECT 1 FROM capacity_task_bindings b WHERE b.reservation_id=r.reservation_id)`, attempt, job).Scan(&safe); err != nil {
			return err
		}
		if safe != 1 {
			return errors.New("CANCEL_NO_EFFECT_NOT_PROVEN")
		}
		at := s.config.Now().UTC().Format(time.RFC3339Nano)
		statements := []struct {
			q    string
			args []any
		}{
			{`UPDATE placement_attempts SET attempt_state='ADD_FAILED',side_effect_fact='FAILED_NO_EFFECT',finished_at=? WHERE attempt_id=?`, []any{at, attempt}},
			{`UPDATE capacity_reservations SET reservation_state='RELEASED',release_reason='NO_EFFECT',released_unconsumed_bytes=reserved_bytes,released_at=?,revision=revision+1 WHERE attempt_id=?`, []any{at, attempt}},
			{`UPDATE race_jobs SET job_state='ADD_FAILED',current_attempt_id=NULL,placement_wait_reason=?,updated_at=? WHERE job_id=?`, []any{reason, at, job}},
			{`UPDATE sync_waiters SET waiter_state='CLOSED',closed_at=? WHERE attempt_id=?`, []any{at, attempt}},
			{`DELETE FROM dispatch_queue WHERE attempt_id=?`, []any{attempt}},
			{`INSERT INTO race_events(job_id,attempt_id,event_type,from_state,to_state,reason_code,safe_metadata_json,occurred_at) VALUES(?,?,'attempt.state.changed','RESERVED','ADD_FAILED',?,'{}',?)`, []any{job, attempt, reason, at}},
		}
		for _, v := range statements {
			if _, err := tx.ExecContext(ctx, v.q, v.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// Policies and legacy artifacts are prepared outside the coordinator and send
// locks. One unavailable policy/source cannot stall another instance's sender.
func (s *Stage) Warm(ctx context.Context) error {
	var failures []error
	if err := s.warmEnabledPolicies(ctx); err != nil {
		failures = append(failures, err)
	}
	jobs, err := s.listDiscoveredJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if enabled, err := s.ruleEnabled(ctx, job.jobID); err != nil || !enabled {
			continue
		}
		var raw string
		var size int64
		if err := s.database.QueryRowContext(ctx, `SELECT aggregation_json,content_size FROM job_rule_snapshots WHERE job_id=?`, job.jobID).Scan(&raw, &size); err != nil {
			failures = append(failures, err)
			continue
		}
		var decision ruleaggregate.Result
		if err := json.Unmarshal([]byte(raw), &decision); err != nil {
			failures = append(failures, err)
			continue
		}
		if !s.policyTargetAllowed(ctx, decision.Winner) {
			continue
		}
		if decision.Winner.SeedTimeSeconds > 0 && s.config.WarmPolicy != nil {
			if _, err := s.config.WarmPolicy(ctx, decision.Winner); err != nil {
				failures = append(failures, err)
			}
		}
		_, found, err := racejobstore.LoadArtifact(ctx, s.database, job.jobID)
		if err != nil || found {
			continue
		}
		source, err := s.loadSource(ctx, job.sourceID)
		if err != nil {
			continue
		}
		var credential, origin string
		if err = s.database.QueryRowContext(ctx, `SELECT credential_ref,base_url FROM sites WHERE site_id=?`, source.SiteID).Scan(&credential, &origin); err != nil {
			continue
		}
		artifact, err := s.config.Downloader.Download(ctx, torrentdownload.Request{URL: source.URL, CredentialRef: credential, CredentialOrigin: origin})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		parsed, err := torrentparse.Parse(artifact.Body, torrentparse.DefaultLimits())
		if err != nil {
			continue
		}
		identity, err := torrentidentity.FromTorrent(parsed)
		if err != nil || identity.V1 != job.identityKey || parsed.TotalSize != size {
			continue
		}
		if err := racejobstore.SaveLegacyArtifact(ctx, s.database, job.jobID, artifact.Body, s.config.Now()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Prewarm from current rule configuration even before the first RSS match.
func (s *Stage) warmEnabledPolicies(ctx context.Context) error {
	if s.config.WarmPolicy == nil {
		return nil
	}
	service, err := ruleservice.New(s.database, nil)
	if err != nil {
		return err
	}
	rules, err := service.List(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		m, err := ruleservice.MatchFor(rule, s.config.Storage.InstanceID)
		if err != nil || m.SeedTimeSeconds <= 0 {
			continue
		}
		if !s.policyTargetAllowed(ctx, m) {
			continue
		}
		if _, err := s.config.WarmPolicy(ctx, m); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Stage) policyTargetAllowed(ctx context.Context, m ruleaggregate.Match) bool {
	if m.TargetMode != "INSTANCE_GROUP" {
		return m.TargetMode == "FIXED_INSTANCE" && m.TargetInstanceID == s.config.Storage.InstanceID
	}
	var raw string
	if err := s.database.QueryRowContext(ctx, `SELECT members_json FROM instance_groups WHERE group_id=?`, m.TargetGroupID).Scan(&raw); err != nil {
		return false
	}
	var members []string
	if json.Unmarshal([]byte(raw), &members) != nil {
		return false
	}
	for _, id := range members {
		if id == s.config.Storage.InstanceID {
			return true
		}
	}
	return false
}
