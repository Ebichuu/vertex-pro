// Command secretscan implements the HARD-01 secret and privacy scan: it
// walks the tracked repository files (and optionally built binaries) looking
// for private key material, credential-shaped assignments, provider tokens,
// and passkey-bearing URLs, with entropy scoring and an explicit allowlist
// for deliberate test fakes. Findings fail the run; allowlisted matches are
// reported with their reason.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type allowEntry struct {
	Path  string `json:"path"`
	Match string `json:"match"`
	Note  string `json:"note"`
}

type finding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Rule      string `json:"rule"`
	Match     string `json:"match"`
	Allowed   bool   `json:"allowed"`
	AllowNote string `json:"allow_note,omitempty"`
}

type report struct {
	ScannedFiles int       `json:"scanned_files"`
	Findings     []finding `json:"findings"`
	Allowed      int       `json:"allowed_matches"`
	Status       string    `json:"status"`
}

type rule struct {
	name    string
	pattern *regexp.Regexp
	// minEntropy applies to capture group 1 when > 0.
	minEntropy float64
}

func rules() []rule {
	return []rule{
		{name: "private_key_block", pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
		{name: "passkey_in_url", pattern: regexp.MustCompile(`(?i)passkey=([A-Za-z0-9]{20,})`), minEntropy: 3.0},
		{name: "provider_token", pattern: regexp.MustCompile(`(ghp_[A-Za-z0-9]{36}|gho_[A-Za-z0-9]{36}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35})`)},
		{name: "credential_assignment", pattern: regexp.MustCompile(`(?i)\b(api[_-]?key|password|passwd|secret|authorization|token)["']?\s*[:=]\s*["']([A-Za-z0-9+/_-]{20,})["']`), minEntropy: 3.4},
	}
}

// entropy scores printable characters of a candidate on Shannon entropy;
// random keys score high, wordy test fakes score low.
func entropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, char := range value {
		counts[char]++
	}
	length := float64(len([]rune(value)))
	sum := 0.0
	for _, count := range counts {
		frequency := float64(count) / length
		sum -= frequency * math.Log2(frequency)
	}
	return sum
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(arguments []string) error {
	_, err := run(arguments, os.Stdout)
	return err
}

func run(arguments []string, stdout io.Writer) (report, error) {
	flags := flag.NewFlagSet("secretscan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root to scan")
	binary := flags.String("binary", "", "optional built binary to scan in binary mode")
	allowlistPath := flags.String("allowlist", "tools/secretscan/allowlist.json", "allowlist JSON of deliberate fakes")
	outPath := flags.String("out", "", "optional JSON report path (stdout when empty)")
	if err := flags.Parse(arguments); err != nil {
		return report{}, err
	}

	allowlist, err := loadAllowlist(*allowlistPath)
	if err != nil {
		return report{}, err
	}
	secretRules := rules()
	result := report{Findings: []finding{}}

	files, err := trackedFiles(*root)
	if err != nil {
		return report{}, err
	}
	scanTargets := append([]scanTarget{}, files...)
	if strings.TrimSpace(*binary) != "" {
		scanTargets = append(scanTargets, scanTarget{path: *binary, binaryMode: true})
	}
	for _, target := range scanTargets {
		content, err := os.ReadFile(target.path)
		if err != nil {
			return report{}, fmt.Errorf("read %s: %w", target.path, err)
		}
		if !target.binaryMode && isBinary(content) {
			continue
		}
		result.ScannedFiles++
		relative, err := filepath.Rel(*root, target.path)
		if err != nil {
			relative = target.path
		}
		relative = filepath.ToSlash(relative)
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			for _, secretRule := range secretRules {
				for _, match := range secretRule.pattern.FindAllStringSubmatch(line, -1) {
					candidate := match[0]
					if len(match) > 1 && match[1] != "" {
						candidate = match[1]
					}
					if secretRule.minEntropy > 0 && entropy(candidate) < secretRule.minEntropy {
						continue
					}
					item := finding{Path: relative, Line: lineNumber, Rule: secretRule.name, Match: candidate}
					if note, ok := allowlisted(allowlist, relative, candidate); ok {
						item.Allowed = true
						item.AllowNote = note
						result.Allowed++
					}
					result.Findings = append(result.Findings, item)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return report{}, fmt.Errorf("scan %s: %w", target.path, err)
		}
	}

	blocking := 0
	for _, item := range result.Findings {
		if !item.Allowed {
			blocking++
		}
	}
	result.Status = "PASS"
	if blocking > 0 {
		result.Status = "FAIL"
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return report{}, err
	}
	payload = append(payload, '\n')
	if strings.TrimSpace(*outPath) == "" {
		if _, err := stdout.Write(payload); err != nil {
			return report{}, err
		}
	} else if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
		return report{}, fmt.Errorf("write report: %w", err)
	}
	if blocking > 0 {
		return result, fmt.Errorf("%d secret findings require review", blocking)
	}
	return result, nil
}

type scanTarget struct {
	path       string
	binaryMode bool
}

// trackedFiles lists git-tracked files so ignored build outputs and dependency
// directories are never scanned.
func trackedFiles(root string) ([]scanTarget, error) {
	command := exec.Command("git", "-c", "core.quotepath=false", "ls-files")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	targets := []scanTarget{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		targets = append(targets, scanTarget{path: filepath.Join(root, filepath.FromSlash(line))})
	}
	return targets, nil
}

func isBinary(content []byte) bool {
	limit := len(content)
	if limit > 8000 {
		limit = 8000
	}
	return bytes.IndexByte(content[:limit], 0) >= 0
}

func loadAllowlist(path string) ([]allowEntry, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	var entries []allowEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, fmt.Errorf("parse allowlist: %w", err)
	}
	return entries, nil
}

// allowlisted reports whether a finding is exempt: the allowlist entry's path
// must prefix-match the file and its match must equal the candidate string.
func allowlisted(entries []allowEntry, path, match string) (string, bool) {
	for _, entry := range entries {
		if entry.Match != match {
			continue
		}
		if entry.Path == "" || strings.HasPrefix(path, entry.Path) {
			return entry.Note, true
		}
	}
	return "", false
}
