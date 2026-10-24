package dispatchstage

import (
	"context"
	"errors"
	"time"
)

const pendingLimit = 32
const unknownPauseThreshold = 2
const senderConcurrency = 2

// Pending confirmations retain their budget, but do not hold HTTP slots. A
// burst of unknown results or three consecutive proven send failures pauses
// only this target. This gate never releases or redispatches uncertain work.
func (s *Stage) sendAdmission(ctx context.Context, reader snapshotStateReader) error {
	var unknown, failures, recent int
	err := reader.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM placement_attempts WHERE instance_id=? AND attempt_state IN ('UNKNOWN','EXPIRED_UNKNOWN')),
 COALESCE(SUM(CASE WHEN attempt_state='ADD_FAILED' AND side_effect_fact='FAILED_NO_EFFECT' THEN 1 ELSE 0 END),0),
 COALESCE(MAX(julianday(response_fact_at))>julianday(?),0)
 FROM (SELECT a.attempt_state,a.side_effect_fact,s.response_fact_at FROM placement_attempts a JOIN submit_intents s ON s.attempt_id=a.attempt_id WHERE a.instance_id=? AND s.response_fact_at IS NOT NULL ORDER BY s.response_fact_at DESC,a.attempt_id DESC LIMIT 3)`, s.config.Storage.InstanceID, s.config.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), s.config.Storage.InstanceID).Scan(&unknown, &failures, &recent)
	if err != nil {
		return err
	}
	if unknown >= unknownPauseThreshold {
		return errors.New("UNKNOWN_LIMIT_REACHED")
	}
	if failures == 3 && recent == 1 {
		return errors.New("INSTANCE_SEND_CIRCUIT_OPEN")
	}
	return nil
}
