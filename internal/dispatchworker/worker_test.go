package dispatchworker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

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

func (s *fakeSender) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return qbresponse.RawResponse{}, ctx.Err()
		}
	}
	return s.response, s.err
}

func (s *fakeSender) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func testRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := qbadd.BuildRequest(qbprofile.Profile{AddStartField: "paused"}, qbadd.TorrentArtifact{Filename: "sample.torrent", Content: []byte("torrent")}, qbadd.AddParams{})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func testInput(t *testing.T, request *http.Request) Request {
	t.Helper()
	return Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.8.0", HTTPRequest: request, AttemptState: "RESERVED", AttemptDispatchState: "NOT_STARTED"}
}

func TestDispatchClassifiesResponseAndDoesNotConfirmHash(t *testing.T) {
	sender := &fakeSender{response: qbresponse.RawResponse{StatusCode: http.StatusOK, Body: []byte("Ok.")}}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Dispatch(context.Background(), testInput(t, testRequest(t)))
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchState != "STARTED" || result.SideEffectFact != "POSSIBLE_EFFECT" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Response.SuccessCount != 1 || result.Response.SideEffectFact == "HASH_SEEN" {
		t.Fatalf("unexpected response: %+v", result.Response)
	}
}

func TestDispatchNotSentCanBeRetried(t *testing.T) {
	sender := &fakeSender{err: &NotSentError{Err: io.ErrClosedPipe}}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	input := testInput(t, testRequest(t))
	result, err := worker.Dispatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchState != "NOT_STARTED" || result.SideEffectFact != "FAILED_NO_EFFECT" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := worker.Dispatch(context.Background(), testInput(t, testRequest(t))); err != nil {
		t.Fatal(err)
	}
	if sender.Calls() != 2 {
		t.Fatalf("calls = %d, want 2", sender.Calls())
	}
}

func TestDispatchTransportErrorBlocksReplay(t *testing.T) {
	sender := &fakeSender{err: errors.New("timeout")}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	input := testInput(t, testRequest(t))
	result, err := worker.Dispatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchState != "STARTED" || result.SideEffectFact != "POSSIBLE_EFFECT" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := worker.Dispatch(context.Background(), testInput(t, testRequest(t))); !errors.Is(err, ErrDispatchAlreadyAttempted) {
		t.Fatalf("error = %v, want already attempted", err)
	}
}

func TestDispatchRejectsInvalidRequestAndPriorEffect(t *testing.T) {
	sender := &fakeSender{}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input Request
	}{
		{name: "wrong endpoint", input: func() Request {
			input := testInput(t, testRequest(t))
			input.HTTPRequest.URL.Path = "/api/v2/app/version"
			return input
		}()},
		{name: "prior possible effect", input: func() Request {
			input := testInput(t, testRequest(t))
			input.AttemptSideEffectFact = "POSSIBLE_EFFECT"
			return input
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := worker.Dispatch(context.Background(), tc.input); !errors.Is(err, ErrInvalidDispatchRequest) {
				t.Fatalf("error = %v, want invalid request", err)
			}
		})
	}
}

func TestDispatchRejectsConcurrentAndDuplicateAttempt(t *testing.T) {
	block := make(chan struct{})
	sender := &fakeSender{block: block, response: qbresponse.RawResponse{StatusCode: http.StatusNoContent}}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, dispatchErr := worker.Dispatch(context.Background(), testInput(t, testRequest(t)))
		firstDone <- dispatchErr
	}()
	deadline := time.Now().Add(time.Second)
	for sender.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := worker.Dispatch(context.Background(), testInput(t, testRequest(t))); !errors.Is(err, ErrDispatchAlreadyAttempted) {
		t.Fatalf("concurrent error = %v", err)
	}
	close(block)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Dispatch(context.Background(), testInput(t, testRequest(t))); !errors.Is(err, ErrDispatchAlreadyAttempted) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestDispatchHonorsCancellationBeforeSend(t *testing.T) {
	sender := &fakeSender{response: qbresponse.RawResponse{StatusCode: http.StatusOK}}
	worker, err := New(sender)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := worker.Dispatch(ctx, testInput(t, testRequest(t))); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	if sender.Calls() != 0 {
		t.Fatalf("calls = %d, want 0", sender.Calls())
	}
}
