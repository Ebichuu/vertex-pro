package qbexecutor

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

type fakeSender struct {
	mu       sync.Mutex
	calls    int
	response qbresponse.RawResponse
	err      error
	block    <-chan struct{}
}

func (sender *fakeSender) Send(ctx context.Context, _ *http.Request) (qbresponse.RawResponse, error) {
	sender.mu.Lock()
	sender.calls++
	sender.mu.Unlock()
	if sender.block != nil {
		select {
		case <-sender.block:
		case <-ctx.Done():
			return qbresponse.RawResponse{}, ctx.Err()
		}
	}
	return sender.response, sender.err
}

func (sender *fakeSender) Calls() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.calls
}

func TestSetKeepsInstanceSlotsIndependent(t *testing.T) {
	blocked := make(chan struct{})
	senderA := &fakeSender{block: blocked, response: qbresponse.RawResponse{StatusCode: http.StatusNoContent}}
	senderB := &fakeSender{response: qbresponse.RawResponse{StatusCode: http.StatusNoContent}}
	set := New()
	config := Config{MaxInFlight: 1, Timeout: time.Second, FailureThreshold: 3, Cooldown: time.Second}
	if err := set.Register("qb-a", senderA, config); err != nil {
		t.Fatal(err)
	}
	if err := set.Register("qb-b", senderB, config); err != nil {
		t.Fatal(err)
	}

	aDone := make(chan error, 1)
	go func() {
		_, err := set.Dispatch(context.Background(), "qb-a", testInput(t, "attempt-a"))
		aDone <- err
	}()
	waitForCalls(t, senderA)

	if _, err := set.Dispatch(context.Background(), "qb-b", testInput(t, "attempt-b")); err != nil {
		t.Fatal(err)
	}
	if senderB.Calls() != 1 {
		t.Fatalf("qb-b calls=%d, want 1 while qb-a is blocked", senderB.Calls())
	}
	close(blocked)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if snapshot, err := set.Snapshot("qb-a"); err != nil || snapshot.TotalDispatches != 1 || snapshot.InFlight != 0 {
		t.Fatalf("qb-a snapshot=%+v err=%v", snapshot, err)
	}
}

func TestSetOpensOnlyFailingInstanceCircuit(t *testing.T) {
	senderA := &fakeSender{err: errors.New("timeout")}
	senderB := &fakeSender{response: qbresponse.RawResponse{StatusCode: http.StatusNoContent}}
	set := New()
	if err := set.Register("qb-a", senderA, Config{FailureThreshold: 1, Cooldown: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := set.Register("qb-b", senderB, Config{FailureThreshold: 1, Cooldown: time.Minute}); err != nil {
		t.Fatal(err)
	}

	result, err := set.Dispatch(context.Background(), "qb-a", testInput(t, "attempt-a"))
	if err != nil {
		t.Fatal(err)
	}
	if result.SideEffectFact != "POSSIBLE_EFFECT" || result.Response.ResponseFormat != "transport_error" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := set.Dispatch(context.Background(), "qb-a", testInput(t, "attempt-a-2")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("error=%v, want ErrCircuitOpen", err)
	}
	if senderA.Calls() != 1 {
		t.Fatalf("qb-a calls=%d, want 1", senderA.Calls())
	}
	if _, err := set.Dispatch(context.Background(), "qb-b", testInput(t, "attempt-b")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := set.Snapshot("qb-a")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Open || snapshot.FailedDispatches != 1 || snapshot.TotalDispatches != 1 {
		t.Fatalf("qb-a snapshot=%+v", snapshot)
	}
}

func TestSetTimeoutDoesNotReplayOrHidePossibleEffect(t *testing.T) {
	block := make(chan struct{})
	sender := &fakeSender{block: block}
	set := New()
	if err := set.Register("qb-a", sender, Config{Timeout: 10 * time.Millisecond, FailureThreshold: 2, Cooldown: time.Second}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := set.Dispatch(context.Background(), "qb-a", testInput(t, "attempt-a"))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("dispatch elapsed=%s, timeout boundary was not enforced", elapsed)
	}
	if result.SideEffectFact != "POSSIBLE_EFFECT" || result.DispatchState != "STARTED" {
		t.Fatalf("result=%+v", result)
	}
	if sender.Calls() != 1 {
		t.Fatalf("calls=%d, want 1", sender.Calls())
	}
	close(block)
}

func TestSetValidatesRegistrationAndUnknownInstances(t *testing.T) {
	set := New()
	if _, err := set.Dispatch(context.Background(), "qb-a", dispatchworker.Request{}); !errors.Is(err, ErrInstanceNotRegistered) {
		t.Fatalf("unknown error=%v", err)
	}
	if err := set.Register("qb-a", &fakeSender{}, Config{}); err != nil {
		t.Fatal(err)
	}
	if err := set.Register("qb-a", &fakeSender{}, Config{}); err == nil {
		t.Fatal("duplicate registration error=nil")
	}
	if _, err := set.Snapshot("missing"); !errors.Is(err, ErrInstanceNotRegistered) {
		t.Fatalf("missing snapshot error=%v", err)
	}
}

func testInput(t *testing.T, attemptID string) dispatchworker.Request {
	t.Helper()
	request, err := qbadd.BuildRequest(qbprofile.Profile{AddStartField: "paused"}, qbadd.TorrentArtifact{Filename: "sample.torrent", Content: []byte("torrent")}, qbadd.AddParams{})
	if err != nil {
		t.Fatal(err)
	}
	return dispatchworker.Request{AttemptID: attemptID, SubmitIntentID: "intent-" + attemptID, WaiterID: "waiter-" + attemptID, APIVersion: "2.8.0", HTTPRequest: request, AttemptState: "ADD_SENT", AttemptDispatchState: "STARTED", AttemptSideEffectFact: "POSSIBLE_EFFECT"}
}

func waitForCalls(t *testing.T, sender *fakeSender) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for sender.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sender.Calls() == 0 {
		t.Fatal("sender was not called")
	}
}
