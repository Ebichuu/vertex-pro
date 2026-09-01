package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretRefSerializesOnlyAsRedactedValue(t *testing.T) {
	ref := SecretRef{Provider: "env", Key: "QUI_API_KEY"}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var serialized string
	if err := json.Unmarshal(data, &serialized); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if serialized != "<redacted>" {
		t.Fatalf("serialized ref = %q, want redacted value", serialized)
	}
	if strings.Contains(string(data), ref.Provider) || strings.Contains(string(data), ref.Key) {
		t.Fatalf("serialized ref leaked provider or key: %s", data)
	}
	if ref.String() != "<redacted>" || ref.GoString() != "<redacted>" {
		t.Fatal("string representation leaked secret reference")
	}
}

func TestSecretRefLogsOnlyAsRedactedValue(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	ref := SecretRef{Provider: "env", Key: "QUI_API_KEY"}
	logger.Info("secret reference", slog.Any("secret_ref", ref))
	logOutput := output.String()
	if !strings.Contains(logOutput, "redacted") {
		t.Fatalf("log output missing redaction marker: %s", logOutput)
	}
	if strings.Contains(logOutput, ref.Provider) || strings.Contains(logOutput, ref.Key) {
		t.Fatalf("log output leaked provider or key: %s", logOutput)
	}
}

func TestEnvStoreResolvesSecretAndRejectsWrongProvider(t *testing.T) {
	t.Setenv("QUI_API_KEY", "secret-value")
	value, err := (EnvStore{}).Resolve(context.Background(), SecretRef{Provider: "env", Key: "QUI_API_KEY"})
	if err != nil || value != "secret-value" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
	if _, err := (EnvStore{}).Resolve(context.Background(), SecretRef{Provider: "file", Key: "QUI_API_KEY"}); err == nil {
		t.Fatal("wrong provider error = nil")
	}
}

func TestFileStoreResolvesTrimmedSecretAndRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	value, err := (FileStore{}).Resolve(context.Background(), SecretRef{Provider: "file", Key: path})
	if err != nil || value != "file-secret" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("empty secret file: %v", err)
	}
	if _, err := (FileStore{}).Resolve(context.Background(), SecretRef{Provider: "file", Key: path}); err == nil {
		t.Fatal("empty file error = nil")
	}
}

func TestSecretRefValidationRejectsUnsupportedAndIncompleteRefs(t *testing.T) {
	for _, ref := range []SecretRef{{}, {Provider: "env"}, {Provider: "unsupported", Key: "x"}} {
		if err := ref.Validate(); err == nil {
			t.Fatalf("Validate(%+v) error = nil", ref)
		}
	}
}
