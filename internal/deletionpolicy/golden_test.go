package deletionpolicy

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenSafety() Safety {
	return Safety{
		ProtectedCategories: []string{"keep", "archive", "keep"},
		ProtectedSites:      []string{"siteC"},
		HitAndRun: map[string]HitAndRun{
			"siteA": {MinSeedingTime: 72 * time.Hour, MinRatio: 1},
			"siteB": {MinRatio: 2},
		},
	}
}

func goldenPolicies() map[string]Policy {
	return map[string]Policy{
		"triggers_dry_run": {
			ID: "brush", Name: "Brush cleanup", Revision: 3, DeleteMode: WithFilesPreserveCrossSeeds,
			Triggers: []Trigger{
				{All: []Threshold{{Metric: SeedingTime, Op: GreaterOrEqual, Duration: 72 * time.Hour}}},
				{All: []Threshold{{Metric: Ratio, Op: GreaterOrEqual, Value: 3}}},
				{All: []Threshold{{Metric: SeedingTime, Op: GreaterOrEqual, Duration: 24 * time.Hour}, {Metric: UpSpeedAvg, Op: LessThan, Value: 51200}}},
				{All: []Threshold{{Metric: Unregistered}}},
			},
		},
		"watermark_live": {
			ID: "disk", Name: "Disk watermark", Revision: 1, Live: true, DeleteMode: WithFiles,
			Interval: 2 * time.Minute, ConfirmFor: 10 * time.Minute,
			Watermark: &Watermark{
				MinFreeBytes: 200 << 30, FreeSpacePath: "/data",
				Eligible:   []Threshold{{Metric: SeedingTime, Op: GreaterOrEqual, Duration: time.Hour}},
				Order:      []Weight{{Metric: Size, Multiplier: 1e-9}, {Metric: SeedingTime, Multiplier: 0.001}, {Metric: UpSpeedAvg, Multiplier: -0.0001}},
				SiteScores: map[string]float64{"siteB": 5, "siteA": -10},
			},
		},
		"stalled_keep_files": {
			ID: "stalled", Name: "Stalled downloads", Revision: 2, DeleteMode: KeepFiles,
			Stalled: &StalledCleanup{MinAddedAge: 2 * time.Hour, MinLastActivityAge: time.Hour},
		},
	}
}

type goldenAutomation struct {
	Role       Role `json:"role"`
	Automation any  `json:"automation"`
}

func TestCompileGolden(t *testing.T) {
	for name, policy := range goldenPolicies() {
		t.Run(name, func(t *testing.T) {
			compiled, err := Compile(policy, goldenSafety())
			if err != nil {
				t.Fatal(err)
			}
			out := make([]goldenAutomation, 0, len(compiled))
			for _, item := range compiled {
				out = append(out, goldenAutomation{Role: item.Role, Automation: item.Automation})
			}
			got, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -run TestCompileGolden -update)", err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
				t.Fatalf("compiled %s differs from %s:\n%s", name, path, got)
			}
		})
	}
}
