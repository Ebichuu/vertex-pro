package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestIDsAddsAllCorrelationFields(t *testing.T) {
	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-1")
	ctx = WithCommandID(ctx, "cmd-1")
	ctx = WithJobID(ctx, "job-1")
	ctx = WithAttemptID(ctx, "attempt-1")
	ctx = WithInstanceID(ctx, "instance-1")

	attributes := IDs(ctx)
	if len(attributes) != 5 {
		t.Fatalf("IDs() returned %d values, want 5", len(attributes))
	}

	var output bytes.Buffer
	logger := NewJSONLogger(&output)
	logger.InfoContext(ctx, "correlated event", attributes...)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log output: %v", err)
	}
	for key, want := range map[string]string{
		"request_id": "req-1", "command_id": "cmd-1", "job_id": "job-1", "attempt_id": "attempt-1", "instance_id": "instance-1",
	} {
		if got, ok := record[key].(string); !ok || got != want {
			t.Errorf("%s = %v, want %q", key, record[key], want)
		}
	}
}

func TestJSONLoggerRedactsSecretsAndURLQuery(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONLogger(&output)
	logger.Info("request completed",
		slog.String("token", "token-value-123"),
		slog.String("cookie", "session-cookie-456"),
		slog.String("announce_url", "https://tracker.example/announce?passkey=private-passkey&token=private-token"),
		slog.String("details", "https://example.test/path?api_key=private-api-key"),
	)

	logOutput := output.String()
	for _, secret := range []string{"token-value-123", "session-cookie-456", "private-passkey", "private-token", "private-api-key"} {
		if strings.Contains(logOutput, secret) {
			t.Errorf("log output contains secret %q: %s", secret, logOutput)
		}
	}
	if count := strings.Count(logOutput, redactedValue); count < 4 {
		t.Errorf("redaction count = %d, want at least 4: %s", count, logOutput)
	}
}

func TestRedactStringPreservesNonSecretQueryValues(t *testing.T) {
	got := RedactString("https://example.test/path?name=demo&token=private&mode=full")
	if strings.Contains(got, "private") {
		t.Fatalf("RedactString leaked secret: %s", got)
	}
	if !strings.Contains(got, "name=demo") || !strings.Contains(got, "mode=full") {
		t.Fatalf("RedactString removed non-secret values: %s", got)
	}
}
