package identitymatcher

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

const v1A = "1111111111111111111111111111111111111111"
const v1B = "2222222222222222222222222222222222222222"
const v2A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const v2B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestMatchSupportsV1V2HybridAndLegacyKey(t *testing.T) {
	cases := []struct {
		name       string
		request    Request
		candidates []Candidate
		wantID     string
		wantV1     bool
		wantV2     bool
		wantLegacy bool
	}{
		{name: "v1", request: Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1A}}, candidates: []Candidate{{ID: "v1-candidate", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1A}}}, wantID: "v1-candidate", wantV1: true},
		{name: "v2", request: Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionV2, V2: v2A}}, candidates: []Candidate{{ID: "v2-candidate", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV2, V2: v2A}}}, wantID: "v2-candidate", wantV2: true},
		{name: "hybrid", request: Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionHybrid, V1: v1A, V2: v2A}}, candidates: []Candidate{{ID: "hybrid-candidate", Identity: torrentidentity.Identity{Version: torrentidentity.VersionHybrid, V1: v1A, V2: v2A}}}, wantID: "hybrid-candidate", wantV1: true, wantV2: true},
		{name: "legacy", request: Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1A}, LegacyMapKey: "legacy-key"}, candidates: []Candidate{{ID: "legacy-candidate", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1B}, LegacyMapKey: "legacy-key"}}, wantID: "legacy-candidate", wantLegacy: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := Match(testCase.request, testCase.candidates)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusMatched || result.CandidateID != testCase.wantID || result.MatchedByV1 != testCase.wantV1 || result.MatchedByV2 != testCase.wantV2 || result.MatchedByLegacyKey != testCase.wantLegacy {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestMatchRejectsDualHashConflictAndDoesNotTrustHint(t *testing.T) {
	request := Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionHybrid, V1: v1A, V2: v2A}, QBTorrentIDHint: "qb-1"}
	candidates := []Candidate{
		{ID: "v1-only", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1A}, QBTorrentID: "qb-1"},
		{ID: "v2-only", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV2, V2: v2A}, QBTorrentID: "qb-1"},
	}
	result, err := Match(request, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAmbiguous || !result.QBTorrentIDHintMatch || result.HintAuthoritative {
		t.Fatalf("result = %+v", result)
	}
}

func TestMatchReturnsNotFoundForHintOnly(t *testing.T) {
	result, err := Match(Request{Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1A}, QBTorrentIDHint: "qb-unknown"}, []Candidate{{ID: "candidate", Identity: torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: v1B}, QBTorrentID: "qb-unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusNotFound || result.QBTorrentIDHintMatch {
		t.Fatalf("result = %+v", result)
	}
}

func TestMatchRejectsInvalidAndDuplicateCandidates(t *testing.T) {
	if _, err := Match(Request{Identity: torrentidentity.Identity{}}, nil); err == nil {
		t.Fatal("missing identity error = nil")
	}
	if _, err := Match(Request{Identity: torrentidentity.Identity{V1: "bad"}}, nil); err == nil {
		t.Fatal("invalid identity error = nil")
	}
	candidates := []Candidate{{ID: "duplicate", Identity: torrentidentity.Identity{V1: v1A}}, {ID: "duplicate", Identity: torrentidentity.Identity{V1: v1B}}}
	if _, err := Match(Request{Identity: torrentidentity.Identity{V1: v1A}}, candidates); err == nil {
		t.Fatal("duplicate candidate error = nil")
	}
}
