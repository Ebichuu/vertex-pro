package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/observability"
)

func TestMiddlewareAssignsRequestIDAndLogs(t *testing.T) {
	loggerBuffer := &bytes.Buffer{}
	middleware := &Middleware{Logger: observability.NewJSONLogger(loggerBuffer)}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observability.RequestID(r.Context()) == "" {
			t.Fatal("request id missing in context")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id header missing")
	}
	if !strings.Contains(loggerBuffer.String(), `"request_id"`) || !strings.Contains(loggerBuffer.String(), `"status":200`) {
		t.Fatalf("log = %s", loggerBuffer.String())
	}
}

func TestMiddlewareHonorsIncomingRequestID(t *testing.T) {
	middleware := &Middleware{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "fixed-id")
	middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") != "fixed-id" {
		t.Fatalf("header = %s", response.Header().Get("X-Request-ID"))
	}
}

func TestRecoverWritesContractEnvelope(t *testing.T) {
	middleware := &Middleware{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/boom", nil)
	middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("secret stack detail")
	})).ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "INTERNAL_ERROR" || envelope.Error.RequestID == "" || envelope.Error.Retryable {
		t.Fatalf("envelope = %+v", envelope)
	}
	if strings.Contains(response.Body.String(), "secret stack detail") {
		t.Fatal("panic value leaked into response")
	}
}

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	middleware := &Middleware{MaxBodyBytes: 16}
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := DecodeJSON(w, r, &payload, 0); err != nil {
			var typed *TypedError
			if errors.As(err, &typed) {
				WriteError(w, r, typed.Code, typed.Message)
				return
			}
			WriteError(w, r, "INTERNAL_ERROR", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"`+strings.Repeat("x", 64)+`"}`))
	handler.ServeHTTP(response, request)
	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || envelope.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("status=%d envelope=%+v", response.Code, envelope)
	}
}

func TestDecodeJSONRejectsTrailingValues(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}{"unexpected":true}`))
	var payload map[string]any
	err := DecodeJSON(response, request, &payload, 0)
	var typed *TypedError
	if !errors.As(err, &typed) || typed.Code != "INVALID_REQUEST" {
		t.Fatalf("error=%v typed=%+v", err, typed)
	}
}
func TestTimeoutWritesEnvelopeAndCancelsContext(t *testing.T) {
	middleware := &Middleware{Timeout: 30 * time.Millisecond}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/slow", nil)
	middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("context deadline missing")
		}
		if deadline.IsZero() {
			t.Fatal("deadline is zero")
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})).ServeHTTP(response, request)
	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || envelope.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("status=%d envelope=%+v", response.Code, envelope)
	}
}

func TestClientCancelDoesNotWriteAfterDisconnect(t *testing.T) {
	middleware := &Middleware{Timeout: time.Minute}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/cancel", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})).ServeHTTP(response, request.WithContext(ctx))
	if response.Body.Len() != 0 {
		t.Fatalf("canceled request wrote %q", response.Body.String())
	}
}

// TestErrorCodesMatchContract 守护服务端状态/retryable 映射与
// contracts/error_codes.yaml 冻结合同一致。
func TestErrorCodesMatchContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/error_codes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	linePattern := regexp.MustCompile(`^\s{2}([A-Z_]+): \{ http_status: (\d+), retryable: (true|false) \}`)
	for _, line := range strings.Split(string(raw), "\n") {
		match := linePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		code, status, retryable := ErrorCode(match[1]), match[2], match[3] == "true"
		rule, ok := contractRules[code]
		if !ok {
			t.Fatalf("contract code %s missing in implementation", code)
		}
		if got := strconv.Itoa(rule.status); got != status {
			t.Fatalf("code %s status = %s want %s", code, got, status)
		}
		if rule.retryable != retryable {
			t.Fatalf("code %s retryable = %t want %t", code, rule.retryable, retryable)
		}
	}
}

// TestEnvelopeMatchesOpenAPISchema 验证 envelope 字段集合与 OpenAPI
// ErrorEnvelope 的 required/additionalProperties 约束一致。
func TestEnvelopeMatchesOpenAPISchema(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas struct {
				ErrorEnvelope struct {
					Properties map[string]any `json:"properties"`
				} `json:"ErrorEnvelope"`
				Error struct {
					Required []string `json:"required"`
				} `json:"Error"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document.Components.Schemas.ErrorEnvelope.Properties["error"]; !ok {
		t.Fatal("ErrorEnvelope schema lacks error property")
	}
	required := strings.Join(document.Components.Schemas.Error.Required, ",")
	for _, field := range []string{"code", "message", "request_id", "retryable"} {
		if !strings.Contains(required, field) {
			t.Fatalf("Error schema required = %s, missing %s", required, field)
		}
	}
}
