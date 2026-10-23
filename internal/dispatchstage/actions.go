package dispatchstage

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
)

func (s *Stage) actionParams(d ruleaggregate.Result) (qbadd.AddParams, error) {
	p := qbadd.AddParams{SavePath: s.config.Storage.Path, Category: d.Winner.Category, Tags: d.Tags, StartInactive: d.Winner.AddParams["start_inactive"] == "true", SequentialDownload: d.Winner.AddParams["sequential_download"] == "true"}
	if len(d.AllowedProfiles) > 0 {
		found := false
		for _, id := range d.AllowedProfiles {
			if id == s.config.Profile.ID {
				found = true
			}
		}
		if !found {
			return p, fmt.Errorf("profile constraint excludes this instance")
		}
	}
	for _, required := range d.StorageLabels {
		found := false
		for _, label := range s.config.Storage.Labels {
			if label == required {
				found = true
			}
		}
		if !found {
			return p, fmt.Errorf("storage labels exclude this target")
		}
	}
	if d.Winner.SavePath != "" {
		p.SavePath = d.Winner.SavePath
	}
	if d.Winner.AddParams["save_path_policy"] == "CATEGORY_SUBDIRECTORY" {
		category := strings.ReplaceAll(d.Winner.Category, "\\", "/")
		if category == "" || strings.HasPrefix(category, "/") || strings.Contains(category, ":") {
			return p, fmt.Errorf("category subdirectory is invalid")
		}
		for _, part := range strings.Split(category, "/") {
			if part == ".." || part == "." || part == "" {
				return p, fmt.Errorf("category subdirectory is invalid")
			}
		}
		p.SavePath = path.Join(strings.ReplaceAll(p.SavePath, "\\", "/"), category)
	}
	if raw := d.Winner.AddParams["upload_limit_bytes"]; raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return p, fmt.Errorf("upload limit is invalid")
		}
		p.UploadLimitBytes = n
	}
	return p, nil
}

// A path prefix is not filesystem evidence: subdirectories may be mounts.
func (s *Stage) validateSavePath(savePath string, e Evidence) error {
	if strings.TrimRight(savePath, "/\\") == strings.TrimRight(s.config.Storage.Path, "/\\") {
		return nil
	}
	proof, ok := s.config.Storage.PathEvidence[savePath]
	if !ok {
		return fmt.Errorf("selected save path requires device evidence")
	}
	defaultPath, _ := e.Preferences["save_path"].(string)
	target := storageverify.StorageTarget{ID: s.config.Storage.StorageTargetID, ManagedRoot: savePath, QBDefaultSavePath: defaultPath, FilesystemKey: s.config.Storage.Verification.FilesystemKey, ConfigurationRevision: s.config.Storage.Verification.ConfigurationRevision}
	if storageverify.Validate(target, proof, s.config.Now(), 24*time.Hour).State != storageverify.StateVerified {
		return fmt.Errorf("selected save path device evidence is invalid")
	}
	return nil
}
