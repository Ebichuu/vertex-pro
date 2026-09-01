package quprobe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

func TestBuildReportRedactsRequestsResponsesAndComputesDigest(t *testing.T) {
	report, err := BuildReport(Fixture{Name: "qb-522", Profile: qbprofile.Profile{ID: "QB_522", ProfileVerificationState: "EXPECTED"}, Requests: []Request{{Method: "GET", Endpoint: "/api/v2/sync/maindata", Query: map[string]string{"rid": "0", "api_key": "secret"}, Headers: map[string]string{"Authorization": "Bearer secret"}, Body: map[string]any{"cookie": "private", "rid": 0}}}, Responses: []Response{{Status: 200, Headers: map[string]string{"Set-Cookie": "private"}, Body: map[string]any{"full_update": true, "passkey": "private"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if report.FixtureDigest == "" || report.Verification != "EXPECTED" {
		t.Fatalf("report = %+v", report)
	}
	payload, _ := json.Marshal(report)
	text := string(payload)
	if strings.Contains(text, "secret") || strings.Contains(text, "private") || !strings.Contains(text, "redacted") {
		t.Fatalf("report leaked sensitive data: %s", text)
	}
}

func TestBuildReportRejectsIncompleteFixture(t *testing.T) {
	for _, fixture := range []Fixture{{}, {Name: "fixture"}, {Name: "fixture", Profile: qbprofile.Profile{ID: "QB_438"}}} {
		if _, err := BuildReport(fixture); err == nil {
			t.Fatalf("fixture accepted: %+v", fixture)
		}
	}
}
