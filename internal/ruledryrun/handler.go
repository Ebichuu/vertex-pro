package ruledryrun

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
)

type Request struct {
	Tree         rules.ConditionTree   `json:"condition_tree"`
	Facts        rules.Facts           `json:"facts"`
	Matches      []ruleaggregate.Match `json:"matches,omitempty"`
	ReservedTags []string              `json:"reserved_tags,omitempty"`
}

type Response struct {
	State       rules.State           `json:"state"`
	Reason      rules.Reason          `json:"reason"`
	Aggregation *ruleaggregate.Result `json:"aggregation,omitempty"`
}

type Service struct{}

func (Service) Run(request Request) (Response, error) {
	result, err := rules.Evaluate(request.Tree, request.Facts)
	if err != nil {
		return Response{}, err
	}
	response := Response{State: result.State, Reason: result.Reason}
	if len(request.Matches) > 0 {
		aggregated, err := ruleaggregate.Aggregate(request.Matches, request.ReservedTags)
		if err != nil {
			return Response{}, err
		}
		response.Aggregation = &aggregated
	}
	return response, nil
}

type Handler struct{ Service Service }

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !isDryRunPath(request.URL.Path) {
		writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input Request
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			writeError(writer, http.StatusBadRequest, "MULTIPLE_JSON_VALUES")
			return
		}
		writeError(writer, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	response, err := handler.Service.Run(input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, normalizeError(err))
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		return
	}
}

func isDryRunPath(path string) bool {
	if path == "/rules/dry-run" {
		return true
	}
	if !strings.HasPrefix(path, "/rules/") || !strings.HasSuffix(path, "/dry-run") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/rules/"), "/dry-run")
	return id != "" && !strings.Contains(id, "/")
}

func normalizeError(err error) string {
	if err == nil {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(err.Error(), " ", "_"))
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"code": code})
}
