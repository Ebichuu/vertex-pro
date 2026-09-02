package g1lab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	StatusExpected = "EXPECTED"
	StatusPassed   = "PASSED"
	StatusFailed   = "FAILED"
)

type Profile struct {
	ID            string `json:"id"`
	QBVersion     string `json:"qb_version"`
	WebAPIVersion string `json:"webapi_version"`
	Libtorrent    string `json:"libtorrent"`
}
type Baseline struct {
	QuiImage      string    `json:"qui_image"`
	QuiDigest     string    `json:"qui_digest"`
	HarnessImage  string    `json:"harness_image"`
	Profiles      []Profile `json:"profiles"`
	ExternalCalls bool      `json:"external_calls"`
}
type Result struct {
	ProfileID string `json:"profile_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}
type Report struct {
	BaselineDigest string   `json:"baseline_digest"`
	Results        []Result `json:"results"`
}

func NewBaseline() Baseline {
	return Baseline{QuiImage: "UNSET", QuiDigest: "UNSET", HarnessImage: "UNSET", Profiles: []Profile{{ID: "QB_438", QBVersion: "4.3.8", WebAPIVersion: "2.8.2", Libtorrent: "1.2.14.0"}, {ID: "QB_504", QBVersion: "5.0.4", WebAPIVersion: "2.11.2", Libtorrent: "2.0.11.0"}, {ID: "QB_522", QBVersion: "5.2.2", WebAPIVersion: "2.15.1", Libtorrent: "2.0.13.0"}}, ExternalCalls: false}
}
func Validate(b Baseline) error {
	if strings.TrimSpace(b.QuiImage) == "" || strings.TrimSpace(b.QuiDigest) == "" || strings.TrimSpace(b.HarnessImage) == "" {
		return errors.New("qui image, qui digest, and harness image are required")
	}
	if b.ExternalCalls {
		return errors.New("baseline validation cannot perform external calls")
	}
	seen := map[string]struct{}{}
	for _, p := range b.Profiles {
		if p.ID == "" || p.QBVersion == "" || p.WebAPIVersion == "" || p.Libtorrent == "" {
			return errors.New("profile facts are incomplete")
		}
		if _, ok := seen[p.ID]; ok {
			return fmt.Errorf("duplicate profile %s", p.ID)
		}
		seen[p.ID] = struct{}{}
	}
	for _, id := range []string{"QB_438", "QB_504", "QB_522"} {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("required profile %s is missing", id)
		}
	}
	return nil
}
func BuildReport(b Baseline) (Report, error) {
	if err := Validate(b); err != nil {
		return Report{}, err
	}
	canonical, err := json.Marshal(b)
	if err != nil {
		return Report{}, fmt.Errorf("marshal baseline: %w", err)
	}
	d := sha256.Sum256(canonical)
	results := make([]Result, 0, len(b.Profiles))
	for _, p := range b.Profiles {
		results = append(results, Result{ProfileID: p.ID, Status: StatusExpected, Reason: "real qui/qB lab execution is not available in this local harness"})
	}
	return Report{BaselineDigest: hex.EncodeToString(d[:]), Results: results}, nil
}
