package quprobe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

type Request struct {
	Method   string
	Endpoint string
	Query    map[string]string
	Headers  map[string]string
	Body     any
}

type Response struct {
	Status  int
	Headers map[string]string
	Body    any
}

type Fixture struct {
	Name      string
	Profile   qbprofile.Profile
	Requests  []Request
	Responses []Response
}

type Report struct {
	Name          string     `json:"name"`
	ProfileID     string     `json:"profile_id"`
	Verification  string     `json:"verification_state"`
	FixtureDigest string     `json:"fixture_digest"`
	Requests      []Request  `json:"requests"`
	Responses     []Response `json:"responses"`
}

func BuildReport(fixture Fixture) (Report, error) {
	if strings.TrimSpace(fixture.Name) == "" {
		return Report{}, errors.New("fixture name is required")
	}
	if fixture.Profile.ID == "" {
		return Report{}, errors.New("profile ID is required")
	}
	if fixture.Profile.ProfileVerificationState == "" {
		return Report{}, errors.New("profile verification state is required")
	}
	canonical, err := json.Marshal(struct {
		Name      string            `json:"name"`
		Profile   qbprofile.Profile `json:"profile"`
		Requests  []Request         `json:"requests"`
		Responses []Response        `json:"responses"`
	}{fixture.Name, fixture.Profile, fixture.Requests, fixture.Responses})
	if err != nil {
		return Report{}, fmt.Errorf("marshal fixture: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return Report{Name: fixture.Name, ProfileID: fixture.Profile.ID, Verification: fixture.Profile.ProfileVerificationState, FixtureDigest: hex.EncodeToString(digest[:]), Requests: redactRequests(fixture.Requests), Responses: redactResponses(fixture.Responses)}, nil
}

func redactRequests(requests []Request) []Request {
	result := make([]Request, len(requests))
	for index, request := range requests {
		result[index] = request
		result[index].Query = redactMap(request.Query)
		result[index].Headers = redactMap(request.Headers)
		result[index].Body = redactValue(request.Body)
	}
	return result
}

func redactResponses(responses []Response) []Response {
	result := make([]Response, len(responses))
	for index, response := range responses {
		result[index] = response
		result[index].Headers = redactMap(response.Headers)
		result[index].Body = redactValue(response.Body)
	}
	return result
}

func redactMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		if sensitiveKey(key) {
			result[key] = "<redacted>"
		} else {
			result[key] = value
		}
	}
	return result
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			if sensitiveKey(key) {
				result[key] = "<redacted>"
			} else {
				result[key] = redactValue(nested)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, nested := range typed {
			result[index] = redactValue(nested)
		}
		return result
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range []string{"key", "token", "cookie", "passkey", "password", "secret", "authorization", "query"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
