package dispatchworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

const maxRequestBodyBytes = 32 << 20

var (
	ErrDispatchAlreadyAttempted = errors.New("dispatch already attempted for attempt")
	ErrInvalidDispatchRequest   = errors.New("invalid dispatch request")
)

type NotSentError struct{ Err error }

func (e *NotSentError) Error() string {
	if e == nil || e.Err == nil {
		return "request was not sent"
	}
	return fmt.Sprintf("request was not sent: %v", e.Err)
}

func (e *NotSentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Sender interface {
	Send(context.Context, *http.Request) (qbresponse.RawResponse, error)
}

type Request struct {
	AttemptID             string
	SubmitIntentID        string
	WaiterID              string
	APIVersion            string
	HTTPRequest           *http.Request
	AttemptState          string
	AttemptDispatchState  string
	AttemptSideEffectFact string
}

type Result struct {
	AttemptID      string
	DispatchState  string
	SideEffectFact string
	Response       qbresponse.Result
}

type Worker struct {
	sender Sender
	mu     sync.Mutex
	busy   map[string]struct{}
	used   map[string]struct{}
}

func New(sender Sender) (*Worker, error) {
	if sender == nil {
		return nil, errors.New("sender is required")
	}
	return &Worker{sender: sender, busy: make(map[string]struct{}), used: make(map[string]struct{})}, nil
}

// Validate performs all local request checks without invoking the sender. A
// caller that persists a durable dispatch marker must run this before that
// marker is committed, so malformed input cannot create an uncertain chain.
func Validate(input Request) error {
	if err := validateInput(input); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDispatchRequest, err)
	}
	if err := validateMultipartRequest(input.HTTPRequest); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDispatchRequest, err)
	}
	return nil
}

func (w *Worker) Dispatch(ctx context.Context, input Request) (Result, error) {
	if err := Validate(input); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		response, classifyErr := qbresponse.ClassifyNotSent(input.APIVersion, err)
		if classifyErr != nil {
			return Result{}, fmt.Errorf("classify canceled dispatch: %w", classifyErr)
		}
		return Result{AttemptID: input.AttemptID, DispatchState: response.DispatchState, SideEffectFact: response.SideEffectFact, Response: response}, err
	}

	w.mu.Lock()
	if _, ok := w.busy[input.AttemptID]; ok {
		w.mu.Unlock()
		return Result{}, ErrDispatchAlreadyAttempted
	}
	if _, ok := w.used[input.AttemptID]; ok {
		w.mu.Unlock()
		return Result{}, ErrDispatchAlreadyAttempted
	}
	w.busy[input.AttemptID] = struct{}{}
	w.mu.Unlock()

	raw, err := w.sender.Send(ctx, input.HTTPRequest)
	w.mu.Lock()
	delete(w.busy, input.AttemptID)
	w.used[input.AttemptID] = struct{}{}
	w.mu.Unlock()

	if err != nil && isNotSent(err) {
		response, classifyErr := qbresponse.ClassifyNotSent(input.APIVersion, err)
		if classifyErr != nil {
			return Result{}, fmt.Errorf("classify not-sent dispatch: %w", classifyErr)
		}
		return Result{AttemptID: input.AttemptID, DispatchState: response.DispatchState, SideEffectFact: response.SideEffectFact, Response: response}, nil
	}

	if strings.TrimSpace(raw.APIVersion) == "" {
		raw.APIVersion = input.APIVersion
	}
	if err != nil {
		raw.TransportErr = err
	}
	response, adaptErr := qbresponse.Adapt(raw)
	if adaptErr != nil {
		return Result{AttemptID: input.AttemptID, DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", Response: response}, fmt.Errorf("adapt dispatch response: %w", adaptErr)
	}
	return Result{AttemptID: input.AttemptID, DispatchState: response.DispatchState, SideEffectFact: response.SideEffectFact, Response: response}, nil
}

func validateInput(input Request) error {
	for name, value := range map[string]string{
		"attempt_id": input.AttemptID, "submit_intent_id": input.SubmitIntentID,
		"waiter_id": input.WaiterID, "api_version": input.APIVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if input.HTTPRequest == nil || input.HTTPRequest.URL == nil || !qbadd.IsAddEndpoint(input.HTTPRequest.URL) {
		return errors.New("request endpoint is not qB Add endpoint")
	}
	if input.AttemptState != "ADD_SENT" {
		return errors.New("attempt state is not durably marked ADD_SENT")
	}
	if input.AttemptDispatchState != "STARTED" {
		return errors.New("attempt dispatch state is not durably marked STARTED")
	}
	if input.AttemptSideEffectFact != "POSSIBLE_EFFECT" {
		return errors.New("attempt side-effect fact is not durably marked POSSIBLE_EFFECT")
	}
	return nil
}

func validateMultipartRequest(request *http.Request) error {
	contentType := request.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return errors.New("request body is not multipart/form-data")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(body) == 0 {
		return errors.New("request body is empty")
	}
	if len(body) > maxRequestBodyBytes {
		return errors.New("request body exceeds limit")
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	torrentParts := 0
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("parse multipart body: %w", nextErr)
		}
		if part.FormName() == "torrents" {
			torrentParts++
			content, readErr := io.ReadAll(io.LimitReader(part, maxRequestBodyBytes+1))
			if readErr != nil {
				return fmt.Errorf("read torrent part: %w", readErr)
			}
			if len(content) == 0 {
				return errors.New("torrent part is empty")
			}
		}
	}
	if torrentParts != 1 {
		return fmt.Errorf("expected exactly one torrent part, got %d", torrentParts)
	}
	return nil
}

func isNotSent(err error) bool {
	var target *NotSentError
	return errors.As(err, &target)
}
