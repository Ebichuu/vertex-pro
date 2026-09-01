package qbprofile

import (
	"context"
	"errors"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
)

type fixtureProxy struct {
	app, web, libtorrent string
	process              map[string]any
	processErr           error
}

func (fixture fixtureProxy) Version(context.Context) (string, error)       { return fixture.app, nil }
func (fixture fixtureProxy) WebAPIVersion(context.Context) (string, error) { return fixture.web, nil }
func (fixture fixtureProxy) BuildInfo(context.Context) (map[string]any, error) {
	return map[string]any{"libtorrent": fixture.libtorrent}, nil
}
func (fixture fixtureProxy) ProcessInfo(context.Context) (map[string]any, error) {
	return fixture.process, fixture.processErr
}

func TestProbeRecognizesThreeFrozenProfiles(t *testing.T) {
	cases := []struct {
		name, web, app, libtorrent, id, start, response, identity string
		process                                                   bool
	}{
		{name: "QB_438", web: "2.8.2", app: "4.3.8", libtorrent: "1.2.11", id: "QB_438", start: "paused", response: "legacy_text", identity: "legacy_v1"},
		{name: "QB_504", web: "2.11.2", app: "5.0.4", libtorrent: "1.2.14", id: "QB_504", start: "stopped", response: "legacy_text", identity: "dual_hash"},
		{name: "QB_522", web: "2.15.1", app: "5.2.2", libtorrent: "2.0.9", id: "QB_522", start: "stopped", response: "json_counts", identity: "dual_hash", process: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := fixtureProxy{app: testCase.app, web: testCase.web, libtorrent: testCase.libtorrent}
			if testCase.process {
				fixture.process = map[string]any{"launch_time": float64(1)}
			} else {
				fixture.processErr = &proxy.HTTPError{StatusCode: 404, Status: "404 Not Found"}
			}
			profile, err := Probe(context.Background(), fixture)
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if profile.ID != testCase.id || profile.AddStartField != testCase.start || profile.AddResponseFormat != testCase.response || profile.IdentityMode != testCase.identity {
				t.Fatalf("profile = %+v", profile)
			}
			if profile.ProfileVerificationState != "EXPECTED" {
				t.Fatalf("verification state = %q", profile.ProfileVerificationState)
			}
			if profile.SupportsV2 != (testCase.libtorrent[0] == '2') {
				t.Fatalf("SupportsV2 = %v", profile.SupportsV2)
			}
			if profile.SupportsProcessInfo != testCase.process {
				t.Fatalf("SupportsProcessInfo = %v", profile.SupportsProcessInfo)
			}
		})
	}
}

func TestProbeRejectsMalformedOrMissingFacts(t *testing.T) {
	for _, fixture := range []fixtureProxy{{web: "not-a-version", libtorrent: "1.2.0"}, {web: "2.15.1", libtorrent: "not-a-version"}} {
		if _, err := Probe(context.Background(), fixture); err == nil {
			t.Fatal("Probe() error = nil")
		}
	}
	fixture := fixtureProxy{web: "2.15.1", libtorrent: "2.0.9", processErr: errors.New("network failure")}
	if _, err := Probe(context.Background(), fixture); err == nil {
		t.Fatal("process probe error = nil")
	}
}

func TestProbeRequiresProxy(t *testing.T) {
	if _, err := Probe(context.Background(), nil); err == nil {
		t.Fatal("nil proxy error = nil")
	}
}
