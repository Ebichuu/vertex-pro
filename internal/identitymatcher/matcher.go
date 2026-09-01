package identitymatcher

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

const (
	StatusMatched   = "MATCHED"
	StatusNotFound  = "NOT_FOUND"
	StatusAmbiguous = "AMBIGUOUS"
)

type Candidate struct {
	ID           string
	Identity     torrentidentity.Identity
	QBTorrentID  string
	LegacyMapKey string
}

type Request struct {
	Identity        torrentidentity.Identity
	QBTorrentIDHint string
	LegacyMapKey    string
}

type Result struct {
	Status               string
	CandidateID          string
	MatchedByV1          bool
	MatchedByV2          bool
	MatchedByLegacyKey   bool
	QBTorrentIDHintMatch bool
	CandidateIDs         []string
	HintAuthoritative    bool
}

func Match(request Request, candidates []Candidate) (Result, error) {
	requestKeys, err := identityKeys(request.Identity)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.LegacyMapKey) != "" {
		requestKeys[request.LegacyMapKey] = struct{}{}
	}
	matches := make([]Candidate, 0)
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			return Result{}, errors.New("candidate ID is required")
		}
		if _, ok := seen[candidate.ID]; ok {
			return Result{}, fmt.Errorf("duplicate candidate ID %q", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		candidateKeys, keyErr := identityKeys(candidate.Identity)
		if keyErr != nil {
			return Result{}, fmt.Errorf("candidate %s: %w", candidate.ID, keyErr)
		}
		if strings.TrimSpace(candidate.LegacyMapKey) != "" {
			candidateKeys[candidate.LegacyMapKey] = struct{}{}
		}
		if intersects(requestKeys, candidateKeys) {
			matches = append(matches, candidate)
		}
	}
	result := Result{HintAuthoritative: false}
	for _, candidate := range matches {
		result.CandidateIDs = append(result.CandidateIDs, candidate.ID)
		if request.Identity.V1 != "" && candidate.Identity.V1 != "" && equalHex(request.Identity.V1, candidate.Identity.V1) {
			result.MatchedByV1 = true
		}
		if request.Identity.V2 != "" && candidate.Identity.V2 != "" && equalHex(request.Identity.V2, candidate.Identity.V2) {
			result.MatchedByV2 = true
		}
		if request.LegacyMapKey != "" && candidate.LegacyMapKey == request.LegacyMapKey {
			result.MatchedByLegacyKey = true
		}
		if request.QBTorrentIDHint != "" && candidate.QBTorrentID == request.QBTorrentIDHint {
			result.QBTorrentIDHintMatch = true
		}
	}
	if len(matches) == 0 {
		result.Status = StatusNotFound
		return result, nil
	}
	if len(matches) > 1 {
		result.Status = StatusAmbiguous
		return result, nil
	}
	result.Status = StatusMatched
	result.CandidateID = matches[0].ID
	return result, nil
}

func identityKeys(identity torrentidentity.Identity) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	if identity.V1 != "" {
		if len(identity.V1) != 40 || !isHex(identity.V1) {
			return nil, errors.New("v1 identity must be 40 hex characters")
		}
		keys["v1:"+strings.ToLower(identity.V1)] = struct{}{}
	}
	if identity.V2 != "" {
		if len(identity.V2) != 64 || !isHex(identity.V2) {
			return nil, errors.New("v2 identity must be 64 hex characters")
		}
		keys["v2:"+strings.ToLower(identity.V2)] = struct{}{}
	}
	if len(keys) == 0 {
		return nil, errors.New("identity has no digest")
	}
	return keys, nil
}

func intersects(left, right map[string]struct{}) bool {
	for key := range left {
		if _, ok := right[key]; ok {
			return true
		}
	}
	return false
}

func equalHex(left, right string) bool { return strings.EqualFold(left, right) }

func isHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
