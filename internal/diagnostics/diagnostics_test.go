package diagnostics

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestBuildRedactsConfigAndStampsDigest(t *testing.T) {
	database, err := db.Open(t.Context(), db.Config{Path: t.TempDir() + "/diag.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(t.Context(), database); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	report, err := Build("test-version", buildidentity.Identity{Revision: "abc123", Clean: true, Available: true}, map[string]any{
		"database_path":   "/data/vertex-pro.db",
		"tracker_url":     "https://tracker.example/announce?passkey=TOPSECRET",
		"api_key":         "RAW-KEY-VALUE",
		"qui_base_url":    "https://qui.example",
		"enable_dispatch": false,
	}, database, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"TOPSECRET", "RAW-KEY-VALUE"} {
		if bytes.Contains(payload, []byte(leak)) {
			t.Fatalf("secret %q leaked into bundle: %s", leak, payload)
		}
	}
	if report.Digest == "" || len(report.Digest) != len("sha256:")+64 {
		t.Fatalf("digest = %q", report.Digest)
	}
	for _, check := range report.Checks {
		if check.Name == "database" && !check.OK {
			t.Fatalf("migrated database check failed: %+v", check)
		}
	}
}

func TestBuildDigestIsDeterministicAndCoversContent(t *testing.T) {
	config := map[string]any{"database_path": "/data/vertex-pro.db"}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	first, err := Build("v", buildidentity.Identity{Available: false}, config, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build("v", buildidentity.Identity{Available: false}, config, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("digests differ for identical inputs: %s vs %s", first.Digest, second.Digest)
	}
	// 内容变化必须改变 digest（digest 覆盖除自身外的全部字段）。
	changed, err := Build("v2", buildidentity.Identity{Available: false}, config, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == first.Digest {
		t.Fatal("digest did not change with content")
	}
	// 数据库未配置时 database 检查如实报告，不伪造 OK。
	for _, check := range first.Checks {
		if check.Name == "database" && check.OK {
			t.Fatal("nil database must not report ok")
		}
	}
}
