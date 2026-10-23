package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/app"
	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
)

type runtimeStorage struct {
	InstanceID        string                            `json:"instance_id"`
	TargetID          string                            `json:"storage_target_id"`
	Path              string                            `json:"path"`
	SafetyMarginBytes int64                             `json:"safety_margin_bytes"`
	Verification      storageverify.Evidence            `json:"verification"`
	Labels            []string                          `json:"labels"`
	PathEvidence      map[string]storageverify.Evidence `json:"path_evidence"`
}

type runtimeInstance struct {
	ID           string         `json:"instance_id"`
	BaseURL      string         `json:"qui_url"`
	APIKeyFile   string         `json:"api_key_file"`
	ProxyKeyFile string         `json:"proxy_key_file"`
	BindingFile  string         `json:"profile_verification_binding_file"`
	Storage      runtimeStorage `json:"storage"`
}

type secretStore struct{}

func (secretStore) Resolve(ctx context.Context, ref secrets.SecretRef) (string, error) {
	return secrets.Resolve(ctx, ref)
}

func decodeRuntimeFile(path string, target any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("runtime configuration file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("runtime configuration has trailing data")
	}
	return nil
}

func (s runtimeStorage) config(instanceID string) (dispatchstage.StorageConfig, error) {
	if s.InstanceID != "" && s.InstanceID != instanceID {
		return dispatchstage.StorageConfig{}, errors.New("storage instance does not match proxy instance")
	}
	if strings.TrimSpace(s.TargetID) == "" || strings.TrimSpace(s.Path) == "" || s.SafetyMarginBytes < 0 {
		return dispatchstage.StorageConfig{}, errors.New("storage target, path and non-negative safety margin are required")
	}
	return dispatchstage.StorageConfig{InstanceID: instanceID, StorageTargetID: s.TargetID, Path: s.Path, Verification: s.Verification, Labels: s.Labels, PathEvidence: s.PathEvidence, SafetyMarginBytes: s.SafetyMarginBytes}, nil
}

func readRuntimeStorage(path, instanceID string) (dispatchstage.StorageConfig, error) {
	var file runtimeStorage
	if err := decodeRuntimeFile(path, &file); err != nil {
		return dispatchstage.StorageConfig{}, fmt.Errorf("read storage configuration: %w", err)
	}
	return file.config(instanceID)
}

func readRuntimeInstances(path, snapshotRoot string, syncInterval, workerInterval time.Duration, dispatch bool, hosts []string) ([]app.QuiConfig, error) {
	var file struct {
		Instances []runtimeInstance `json:"instances"`
	}
	if err := decodeRuntimeFile(path, &file); err != nil {
		return nil, fmt.Errorf("read instances configuration: %w", err)
	}
	if len(file.Instances) == 0 {
		return nil, errors.New("instances configuration is empty")
	}
	build := buildidentity.Current()
	seen := map[string]bool{}
	result := make([]app.QuiConfig, 0, len(file.Instances))
	for _, entry := range file.Instances {
		if strings.TrimSpace(entry.ID) == "" || seen[entry.ID] {
			return nil, errors.New("instance IDs must be non-empty and unique")
		}
		seen[entry.ID] = true
		if strings.TrimSpace(entry.BaseURL) == "" || entry.APIKeyFile == "" || entry.ProxyKeyFile == "" {
			return nil, errors.New("each instance needs qui_url and both secret file references")
		}
		config := app.QuiConfig{BaseURL: entry.BaseURL, InstanceID: entry.ID, APIKey: secrets.SecretRef{Provider: "file", Key: entry.APIKeyFile}, ProxyKey: secrets.SecretRef{Provider: "file", Key: entry.ProxyKeyFile}, Store: secretStore{}, SnapshotRoot: snapshotRoot, SyncInterval: syncInterval, HTTPClient: &http.Client{Timeout: 30 * time.Second}, VertexCommit: build.Revision, VertexWorktreeClean: build.Available && build.Clean, EnableDispatch: dispatch}
		if entry.BindingFile != "" {
			binding, err := readProfileVerificationBinding(entry.BindingFile)
			if err != nil {
				return nil, err
			}
			config.ProfileVerificationBinding = &binding
		}
		if dispatch {
			storage, err := entry.Storage.config(entry.ID)
			if err != nil {
				return nil, err
			}
			config.DispatchStage = &app.DispatchStageConfig{Enabled: true, Storage: storage, Interval: workerInterval, AllowedArtifactHosts: hosts}
		}
		result = append(result, config)
	}
	return result, nil
}
