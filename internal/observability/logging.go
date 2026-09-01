package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

type contextKey string

const (
	requestIDKey  contextKey = "request_id"
	commandIDKey  contextKey = "command_id"
	jobIDKey      contextKey = "job_id"
	attemptIDKey  contextKey = "attempt_id"
	instanceIDKey contextKey = "instance_id"
)

const redactedValue = "<redacted>"

var sensitiveQueryPattern = regexp.MustCompile(`(?i)([?&](?:token|access_token|api[_-]?key|passkey|password|secret|cookie|authorization|proxy[_-]?key|announce)[^=]*=)[^&#\s"]+`)

// NewJSONLogger creates the application logger with stable JSON output and secret redaction.
func NewJSONLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{ReplaceAttr: replaceAttr}))
}

// NewID returns a short random correlation identifier for request-scoped work.
func NewID() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(bytes[:])
}

// WithRequestID stores a request identifier in context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// WithCommandID stores a command identifier in context.
func WithCommandID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, commandIDKey, id)
}

// WithJobID stores a job identifier in context.
func WithJobID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, jobIDKey, id)
}

// WithAttemptID stores an attempt identifier in context.
func WithAttemptID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, attemptIDKey, id)
}

// WithInstanceID stores an instance identifier in context.
func WithInstanceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, instanceIDKey, id)
}

// RequestID returns the request identifier stored in context, if any.
func RequestID(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// IDs returns the defined correlation identifiers from context as slog attributes.
func IDs(ctx context.Context) []any {
	attributes := make([]any, 0, 10)
	for _, item := range []struct {
		key  contextKey
		name string
	}{
		{requestIDKey, "request_id"},
		{commandIDKey, "command_id"},
		{jobIDKey, "job_id"},
		{attemptIDKey, "attempt_id"},
		{instanceIDKey, "instance_id"},
	} {
		if value, ok := ctx.Value(item.key).(string); ok && value != "" {
			attributes = append(attributes, slog.String(item.name, value))
		}
	}
	return attributes
}

// RedactString removes known secret query parameters before a value reaches a log record.
func RedactString(value string) string {
	return sensitiveQueryPattern.ReplaceAllString(value, `${1}`+redactedValue)
}

func replaceAttr(_ []string, attribute slog.Attr) slog.Attr {
	key := strings.ToLower(attribute.Key)
	if isSensitiveKey(key) {
		return slog.String(attribute.Key, redactedValue)
	}
	if attribute.Value.Kind() == slog.KindString {
		return slog.String(attribute.Key, RedactString(attribute.Value.String()))
	}
	return attribute
}

func isSensitiveKey(key string) bool {
	for _, fragment := range []string{"token", "cookie", "passkey", "password", "secret", "authorization", "proxy_key", "proxy-key", "announce_url"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
