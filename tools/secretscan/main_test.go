package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, root string) {
	t.Helper()
	for _, arguments := range [][]string{
		{"add", "-A"},
		{"-c", "user.email=scan@example", "-c", "user.name=scan", "commit", "-m", "snapshot"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil && !strings.Contains(string(output), "nothing to commit") {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	commitAll(t, root)
	return root
}

func runScan(t *testing.T, root, allowlist, binary string) report {
	t.Helper()
	arguments := []string{"-root", root}
	if allowlist != "" {
		arguments = append(arguments, "-allowlist", allowlist)
	}
	if binary != "" {
		arguments = append(arguments, "-binary", binary)
	}
	result, err := run(arguments, &bytes.Buffer{})
	if err != nil && result.Status != "FAIL" {
		t.Fatalf("scan failed unexpectedly: %v", err)
	}
	return result
}

// TestScanDetectsLeaksAndHonorsAllowlist 验证三条主线：真实泄漏被规则命中、
// 清单内的刻意假值被豁免并记录、清单外的真泄漏不会被豁免且删掉后 PASS。
func TestScanDetectsLeaksAndHonorsAllowlist(t *testing.T) {
	root := initRepo(t)
	allowlist := filepath.Join(root, "tools", "secretscan", "allowlist.json")
	leakPath := filepath.Join(root, "leak.go")
	write(t, leakPath, "package main\n\nconst pass = \"https://tracker.example/announce?passkey=0123456789abcdef0123456789abcdef\"\n")
	write(t, filepath.Join(root, "internal", "fake_test.go"), "package main\n\nvar fake = \"control-key\"\nvar deliberate = \"passkey=0123456789abcdef0123456789abcdef\"\n")
	write(t, filepath.Join(root, "clean.go"), "package main\n\nconst note = \"https://tracker.example/announce?pid=user-7\"\n")
	write(t, allowlist, `[
		{"path": "internal/fake_test.go", "match": "0123456789abcdef0123456789abcdef", "note": "deliberate corpus fake"}
	]`)
	commitAll(t, root)

	result := runScan(t, root, allowlist, "")
	if result.Status != "FAIL" {
		t.Fatalf("leaky repo must fail: %+v", result)
	}
	if result.Allowed != 1 {
		t.Fatalf("allowlisted fake not recorded: %+v", result)
	}
	blocking := 0
	for _, item := range result.Findings {
		if item.Allowed {
			continue
		}
		blocking++
		if item.Path != "leak.go" || item.Rule != "passkey_in_url" {
			t.Fatalf("unexpected blocking finding: %+v", item)
		}
	}
	if blocking != 1 {
		t.Fatalf("blocking findings = %d, want 1 (leak.go only)", blocking)
	}

	// 移除真泄漏后：豁免项照常记录，整体 PASS。
	if err := os.Remove(leakPath); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root)
	clean := runScan(t, root, allowlist, "")
	if clean.Status != "PASS" || clean.Allowed != 1 {
		t.Fatalf("clean repo = %+v", clean)
	}
}

func TestScanBinaryModeFindsEmbeddedKeys(t *testing.T) {
	root := initRepo(t)
	binary := filepath.Join(root, "vertex-pro")
	write(t, binary, "binary\x00data\x00ghp_0123456789abcdefghijklmnopqrstuvwxyz\x00tail")
	commitAll(t, root)
	result := runScan(t, root, "", binary)
	if result.Status != "FAIL" {
		t.Fatalf("embedded provider token not found: %+v", result)
	}
}
