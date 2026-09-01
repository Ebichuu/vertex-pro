package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const redactedValue = "<redacted>"

type SecretRef struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

func (ref SecretRef) Validate() error {
	if strings.TrimSpace(ref.Provider) == "" {
		return errors.New("secret provider is required")
	}
	if strings.TrimSpace(ref.Key) == "" {
		return errors.New("secret key is required")
	}
	if ref.Provider != "env" && ref.Provider != "file" {
		return fmt.Errorf("unsupported secret provider %q", ref.Provider)
	}
	return nil
}

func (ref SecretRef) String() string {
	return redactedValue
}

func (ref SecretRef) GoString() string {
	return redactedValue
}

func (ref SecretRef) LogValue() slog.Value {
	return slog.StringValue(redactedValue)
}

func (ref SecretRef) MarshalText() ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return []byte(redactedValue), nil
}

type Store interface {
	Resolve(context.Context, SecretRef) (string, error)
}

type EnvStore struct{}

func (EnvStore) Resolve(_ context.Context, ref SecretRef) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if ref.Provider != "env" {
		return "", fmt.Errorf("env store cannot resolve provider %q", ref.Provider)
	}
	value, ok := os.LookupEnv(ref.Key)
	if !ok || value == "" {
		return "", fmt.Errorf("secret environment variable %q is not set", ref.Key)
	}
	return value, nil
}

type FileStore struct{}

func (FileStore) Resolve(_ context.Context, ref SecretRef) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if ref.Provider != "file" {
		return "", fmt.Errorf("file store cannot resolve provider %q", ref.Provider)
	}
	contents, err := os.ReadFile(ref.Key)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	return value, nil
}

func Resolve(ctx context.Context, ref SecretRef) (string, error) {
	switch ref.Provider {
	case "env":
		return (EnvStore{}).Resolve(ctx, ref)
	case "file":
		return (FileStore{}).Resolve(ctx, ref)
	default:
		return "", fmt.Errorf("unsupported secret provider %q", ref.Provider)
	}
}

func (ref SecretRef) MarshalJSON() ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return []byte(`"<redacted>"`), nil
}
