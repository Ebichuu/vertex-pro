package dispatchflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

type Runner struct {
	database *sql.DB
	worker   *dispatchworker.Worker
	now      func() time.Time
}

type Request struct {
	AttemptID      string
	SubmitIntentID string
	WaiterID       string
	APIVersion     string
	HTTPRequest    *http.Request
}

type Result struct {
	Begin    dispatchcommit.BeginResult
	Dispatch dispatchworker.Result
	Commit   dispatchcommit.Result
}

func New(database *sql.DB, sender dispatchworker.Sender, now func() time.Time) (*Runner, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	worker, err := dispatchworker.New(sender)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Runner{database: database, worker: worker, now: now}, nil
}

// Run validates one Add request, atomically persists ADD_SENT/STARTED, performs
// exactly one network dispatch, and then atomically records the response fact.
// It never retries network I/O. A crash after Begin is therefore recovered as
// a held ADD_SENT chain rather than a retryable NOT_STARTED request.
func (runner *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if runner == nil || runner.database == nil || runner.worker == nil {
		return Result{}, errors.New("dispatch runner is not initialized")
	}
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	for name, value := range map[string]string{"attempt_id": request.AttemptID, "submit_intent_id": request.SubmitIntentID, "waiter_id": request.WaiterID, "api_version": request.APIVersion} {
		if strings.TrimSpace(value) == "" {
			return Result{}, fmt.Errorf("%s is required", name)
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	workerRequest := dispatchworker.Request{
		AttemptID:             request.AttemptID,
		SubmitIntentID:        request.SubmitIntentID,
		WaiterID:              request.WaiterID,
		APIVersion:            request.APIVersion,
		HTTPRequest:           request.HTTPRequest,
		AttemptState:          dispatchcommit.AttemptAddSent,
		AttemptDispatchState:  "STARTED",
		AttemptSideEffectFact: "POSSIBLE_EFFECT",
	}
	if err := dispatchworker.Validate(workerRequest); err != nil {
		return Result{}, err
	}

	begun, err := dispatchcommit.Begin(ctx, runner.database, dispatchcommit.BeginRequest{
		AttemptID:      request.AttemptID,
		SubmitIntentID: request.SubmitIntentID,
		WaiterID:       request.WaiterID,
		Now:            runner.now().UTC(),
		Clock:          runner.now,
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{Begin: begun}

	dispatched, dispatchErr := runner.worker.Dispatch(ctx, workerRequest)
	if dispatched.AttemptID == "" {
		dispatched = uncertainResult(request.AttemptID, request.APIVersion)
	}
	result.Dispatch = dispatched

	// A canceled network deadline is itself a dispatch fact. Persist it using a
	// short context detached from the network cancellation so the durable chain
	// cannot remain at ADD_SENT merely because the request context expired.
	commitContext, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCommit()
	committed, commitErr := dispatchcommit.Record(commitContext, runner.database, dispatchcommit.Request{
		AttemptID:      request.AttemptID,
		SubmitIntentID: request.SubmitIntentID,
		WaiterID:       request.WaiterID,
		Result:         dispatched,
		DispatchErr:    dispatchErr,
		Now:            runner.now().UTC(),
	})
	if commitErr != nil {
		return result, commitErr
	}
	result.Commit = committed
	if dispatchErr != nil {
		return result, dispatchErr
	}
	return result, nil
}

func uncertainResult(attemptID, apiVersion string) dispatchworker.Result {
	response := qbresponse.Result{
		APIVersion:     apiVersion,
		ResponseFormat: "transport_error",
		DispatchState:  "STARTED",
		SideEffectFact: "POSSIBLE_EFFECT",
	}
	return dispatchworker.Result{
		AttemptID:      attemptID,
		DispatchState:  response.DispatchState,
		SideEffectFact: response.SideEffectFact,
		Response:       response,
	}
}
