package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/observability"
)

// ErrorCode 是 contracts/error_codes.yaml 中冻结的错误码。
type ErrorCode string

// httpStatus 与 retryable 标志必须与 error_codes.yaml 保持一致，由
// TestErrorCodesMatchContract 守护。
type contractRule struct {
	status    int
	retryable bool
}

var contractRules = map[ErrorCode]contractRule{
	"INVALID_REQUEST":              {http.StatusBadRequest, false},
	"VALIDATION_FAILED":            {http.StatusUnprocessableEntity, false},
	"UNAUTHENTICATED":              {http.StatusUnauthorized, false},
	"FORBIDDEN":                    {http.StatusForbidden, false},
	"NOT_FOUND":                    {http.StatusNotFound, false},
	"REVISION_CONFLICT":            {http.StatusConflict, true},
	"IDEMPOTENCY_KEY_REUSED":       {http.StatusConflict, false},
	"CURSOR_EXPIRED":               {http.StatusConflict, true},
	"JOB_STATE_CONFLICT":           {http.StatusConflict, false},
	"IDENTITY_ACTIVE_CHAIN_EXISTS": {http.StatusConflict, false},
	"PROFILE_UNVERIFIED":           {http.StatusConflict, true},
	"INSTANCE_UNAVAILABLE":         {http.StatusConflict, true},
	"SNAPSHOT_STALE":               {http.StatusConflict, true},
	"STORAGE_UNVERIFIED":           {http.StatusConflict, true},
	"CAPACITY_UNCERTAIN":           {http.StatusConflict, true},
	"INSUFFICIENT_EFFECTIVE_FREE":  {http.StatusConflict, true},
	"QUI_UNREACHABLE":              {http.StatusBadGateway, true},
	"QB_PROXY_UNREACHABLE":         {http.StatusBadGateway, true},
	"RATE_LIMITED":                 {http.StatusTooManyRequests, true},
	"INTERNAL_ERROR":               {http.StatusInternalServerError, false},
}

// ErrorEnvelope 是 OpenAPI ErrorEnvelope 的服务端实现：
// { error: { code, message, request_id, retryable, details? } }。
// message 仅用于本地化展示，客户端只允许按 code 分支；details 禁止携带
// 堆栈、SQL、Secret、Proxy URL 或 announce URL。
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Middleware 组合阶段 I 的基础 HTTP 中间件：日志 -> 超时 -> body 上限 ->
// panic recovery -> request ID。
type Middleware struct {
	Logger          *slog.Logger
	Timeout         time.Duration
	MaxBodyBytes    int64
	RequestIDHeader string
}

// Handler 返回串联后的中间件链：request ID 必须最外层，保证日志、
// panic envelope 与后续中间件都能取到关联标识。
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return m.requestID(m.log(m.recoverPanics(m.limitBody(m.timeout(next)))))
}

func (m *Middleware) requestID(next http.Handler) http.Handler {
	header := m.RequestIDHeader
	if header == "" {
		header = "X-Request-ID"
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		id := strings.TrimSpace(request.Header.Get(header))
		if id == "" {
			id = observability.NewID()
		}
		response.Header().Set(header, id)
		next.ServeHTTP(response, request.WithContext(observability.WithRequestID(request.Context(), id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (m *Middleware) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if m.Logger == nil {
			next.ServeHTTP(response, request)
			return
		}
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(recorder, request)
		m.Logger.InfoContext(request.Context(), "http request",
			append(observability.IDs(request.Context()),
				"method", request.Method, "path", request.URL.Path,
				"status", recorder.status, "duration_ms", time.Since(started).Milliseconds())...)
	})
}

func (m *Middleware) timeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if m.Timeout <= 0 {
			next.ServeHTTP(response, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), m.Timeout)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			next.ServeHTTP(response, request.WithContext(ctx))
		}()
		select {
		case <-done:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				WriteError(response, request, "INTERNAL_ERROR", "request timed out")
			}
		}
	})
}

func (m *Middleware) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if m.MaxBodyBytes > 0 && request.Body != nil {
			request.Body = http.MaxBytesReader(response, request.Body, m.MaxBodyBytes)
		}
		next.ServeHTTP(response, request)
	})
}

// bodyLimitExceeded 区分 http.MaxBytesReader 触发的超限读取错误。
func bodyLimitExceeded(err error) bool {
	if err == nil {
		return false
	}
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

func (m *Middleware) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if m.Logger != nil {
				m.Logger.ErrorContext(request.Context(), "panic recovered",
					append(observability.IDs(request.Context()), "panic", fmt.Sprint(recovered))...)
			}
			WriteError(response, request, "INTERNAL_ERROR", "internal error")
		}()
		next.ServeHTTP(response, request)
	})
}

// DecodeJSON 在 size limit 内解码请求体；body 超限与非法 JSON 映射为合同错误码。
func DecodeJSON(response http.ResponseWriter, request *http.Request, target any, maxBytes int64) error {
	return decodeJSON(response, request, target, maxBytes, false)
}

// DecodeStrictJSON additionally rejects fields absent from a closed request contract.
func DecodeStrictJSON(response http.ResponseWriter, request *http.Request, target any, maxBytes int64) error {
	return decodeJSON(response, request, target, maxBytes, true)
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any, maxBytes int64, strict bool) error {
	if maxBytes > 0 {
		request.Body = http.MaxBytesReader(response, request.Body, maxBytes)
	}
	decoder := json.NewDecoder(request.Body)
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		if bodyLimitExceeded(err) {
			return &TypedError{Code: "INVALID_REQUEST", Message: "request body exceeds limit"}
		}
		return &TypedError{Code: "INVALID_REQUEST", Message: "request body is not valid JSON"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &TypedError{Code: "INVALID_REQUEST", Message: "request body must contain one JSON value"}
	}
	return nil
}

// TypedError 让 handler 用合同错误码而非字符串决定响应。
type TypedError struct {
	Code    ErrorCode
	Message string
	Details map[string]any
}

func (e *TypedError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WriteError 以合同 envelope 输出错误；未知 code 视为合同违规，回退 INTERNAL_ERROR。
func WriteError(response http.ResponseWriter, request *http.Request, code ErrorCode, message string) {
	rule, ok := contractRules[code]
	if !ok {
		code = "INTERNAL_ERROR"
		rule = contractRules[code]
	}
	WriteErrorEnvelope(response, rule.status, ErrorEnvelope{Error: ErrorBody{
		Code: code, Message: message, RequestID: observability.RequestID(request.Context()), Retryable: rule.retryable,
	}})
}

// WriteErrorEnvelope 输出完整 envelope 并带上 status。
func WriteErrorEnvelope(response http.ResponseWriter, status int, envelope ErrorEnvelope) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(envelope)
}

// WriteJSON 输出成功响应。
func WriteJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

// ContractErrorCodes 返回合同错误码（供合同守护测试比对 YAML）。
func ContractErrorCodes() []string {
	codes := make([]string, 0, len(contractRules))
	for code := range contractRules {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)
	return codes
}
