package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestLoggerRedactsSecretCorpus 是 OBS-01 的 Secret corpus 扫描：一批
// 现实中会出现于日志记录的凭据形态逐条通过 JSON logger，断言原始 Secret
// 绝不出现在输出里；同时验证非 Secret 内容不被误伤、关联字段齐全。
func TestLoggerRedactsSecretCorpus(t *testing.T) {
	corpus := []struct {
		name     string
		key      string
		value    string
		leaks    []string // 这些原文绝不允许出现在输出中
		expected []string // 输出必须包含的脱敏/保留内容
	}{
		{
			name: "announce passkey query", key: "tracker_url",
			value:    "https://tracker.example/announce?passkey=ABC123SECRET&pid=user-7",
			leaks:    []string{"ABC123SECRET"},
			expected: []string{"passkey=<redacted>", "pid=user-7", "https://tracker.example/announce"},
		},
		{
			name: "api key query", key: "endpoint",
			value:    "https://qui.example/api?api_key=QUIKEY42&format=json",
			leaks:    []string{"QUIKEY42"},
			expected: []string{"api_key=<redacted>", "format=json"},
		},
		{
			name: "access token query", key: "oauth_url",
			value:    "https://sso.example/token?access_token=eyJhbGciOi9TOKEN",
			leaks:    []string{"eyJhbGciOi9TOKEN"},
			expected: []string{"access_token=<redacted>"},
		},
		{
			name: "url userinfo credentials", key: "upstream",
			value:    "https://admin:s3cret-password@tracker.example/files",
			leaks:    []string{"s3cret-password", "admin:"},
			expected: []string{"https://<redacted>@tracker.example/files"},
		},
		{
			name: "udp announce with passkey", key: "announce",
			value:    "udp://tracker.example:6969/announce?passkey=UDPPASS9",
			leaks:    []string{"UDPPASS9"},
			expected: []string{"passkey=<redacted>"},
		},
	}
	for _, item := range corpus {
		t.Run(item.name, func(t *testing.T) {
			var buffer bytes.Buffer
			logger := NewJSONLogger(&buffer)
			logger.InfoContext(context.Background(), "corpus probe", slog.String(item.key, item.value))
			output := buffer.String()
			for _, leak := range item.leaks {
				if strings.Contains(output, leak) {
					t.Fatalf("secret %q leaked into log output: %s", leak, output)
				}
			}
			for _, want := range item.expected {
				if !strings.Contains(output, want) {
					t.Fatalf("expected %q in log output: %s", want, output)
				}
			}
		})
	}
}

// TestLoggerRedactsSensitiveAttributeKeys 遍历敏感 KEY 片段：无论值是什么，
// 这些键的属性值必须整体替换为 <redacted>（含 x_api_key 等 header 形态）。
func TestLoggerRedactsSensitiveAttributeKeys(t *testing.T) {
	sensitiveKeys := []string{
		"token", "session_token", "cookie", "set_cookie", "passkey", "password",
		"secret", "client_secret", "authorization", "x_api_key", "api_key",
		"proxy_key", "announce_url",
	}
	for _, key := range sensitiveKeys {
		var buffer bytes.Buffer
		logger := NewJSONLogger(&buffer)
		logger.InfoContext(context.Background(), "key corpus", slog.String(key, "RAW-SECRET-VALUE"))
		if strings.Contains(buffer.String(), "RAW-SECRET-VALUE") {
			t.Fatalf("sensitive key %q leaked its value: %s", key, buffer.String())
		}
		if !strings.Contains(buffer.String(), "<redacted>") {
			t.Fatalf("sensitive key %q was not redacted: %s", key, buffer.String())
		}
	}
}

// TestLoggerKeepsCorrelationFieldsAndBenignData 保证 RC8 关联字段齐全、
// 非 Secret 查询参数与普通 URL 不被误伤。
func TestLoggerKeepsCorrelationFieldsAndBenignData(t *testing.T) {
	var buffer bytes.Buffer
	logger := NewJSONLogger(&buffer)
	ctx := WithInstanceID(WithAttemptID(WithJobID(WithCommandID(WithRequestID(context.Background(), "req-1"), "cmd-1"), "job-1"), "attempt-1"), "qb-1")
	logger.InfoContext(ctx, "dispatch probe", slog.String("url", "https://tracker.example/announce?utm_source=rss&pid=ok"))
	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	for _, field := range []string{"request_id", "command_id", "job_id", "attempt_id", "instance_id"} {
		if record[field] == "" {
			t.Fatalf("correlation field %q missing: %s", field, buffer.String())
		}
	}
	rendered := buffer.String()
	for _, want := range []string{"utm_source=rss", "pid=ok", "https://tracker.example/announce"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("benign content %q was damaged: %s", want, rendered)
		}
	}
}
