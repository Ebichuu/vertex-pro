package resourcesapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/apiidempotency"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/observability"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
)

type createFeedRequest struct {
	ID          string `json:"id"`
	SiteID      string `json:"site_id"`
	Name        string `json:"name"`
	FeedURL     string `json:"feed_url"`
	Enabled     *bool  `json:"enabled"`
	PollMinutes int    `json:"poll_minutes"`
}

type updateFeedRequest struct {
	Name        *string `json:"name"`
	FeedURL     *string `json:"feed_url"`
	Enabled     *bool   `json:"enabled"`
	PollMinutes *int    `json:"poll_minutes"`
}

type FeedTester func(context.Context, rssservice.Feed) (rsspreview.Preview, error)

func (d Deps) createFeed(response http.ResponseWriter, request *http.Request) {
	var input createFeedRequest
	if err := httpapi.DecodeJSON(response, request, &input, 64<<10); err != nil {
		var typed *httpapi.TypedError
		if errors.As(err, &typed) {
			httpapi.WriteError(response, request, typed.Code, typed.Message)
			return
		}
		httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid feed request")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "feed service is not configured")
		return
	}
	feed, err := service.CreateFeed(request.Context(), rssservice.CreateFeedInput{
		ID: input.ID, SiteID: input.SiteID, Name: input.Name, FeedURL: input.FeedURL, Enabled: enabled, PollMinutes: input.PollMinutes,
	})
	if err != nil {
		writeFeedCreateError(response, request, err)
		return
	}
	location := "/api/v1/feeds/" + url.PathEscape(feed.ID)
	response.Header().Set("Location", location)
	response.Header().Set("ETag", feedETag(feed))
	httpapi.WriteJSON(response, http.StatusCreated, d.feedRuntimePayload(request, feed))
}

func (d Deps) getFeed(response http.ResponseWriter, request *http.Request) {
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "feed service is not configured")
		return
	}
	feed, err := service.GetFeed(request.Context(), request.PathValue("id"))
	if err != nil {
		writeFeedReadError(response, request, err)
		return
	}
	response.Header().Set("ETag", feedETag(feed))
	d.writeData(response, request, d.feedRuntimePayload(request, feed))
}

func (d Deps) updateFeed(response http.ResponseWriter, request *http.Request) {
	expected, err := parseFeedETag(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain one quoted feed revision")
		return
	}
	var input updateFeedRequest
	if err := httpapi.DecodeJSON(response, request, &input, 64<<10); err != nil {
		var typed *httpapi.TypedError
		if errors.As(err, &typed) {
			httpapi.WriteError(response, request, typed.Code, typed.Message)
			return
		}
		httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid feed update")
		return
	}
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "feed service is not configured")
		return
	}
	feed, err := service.UpdateFeed(request.Context(), rssservice.UpdateFeedInput{
		ID: request.PathValue("id"), ExpectedUpdatedAt: expected,
		Name: input.Name, FeedURL: input.FeedURL, Enabled: input.Enabled, PollMinutes: input.PollMinutes,
	})
	if err != nil {
		writeFeedUpdateError(response, request, err)
		return
	}
	response.Header().Set("ETag", feedETag(feed))
	d.writeData(response, request, d.feedRuntimePayload(request, feed))
}

func (d Deps) testFeed(response http.ResponseWriter, request *http.Request) {
	body, err := readFeedTestBody(request)
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", err.Error())
		return
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "Idempotency-Key is required")
		return
	}
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "feed service is not configured")
		return
	}
	feed, err := service.GetFeed(request.Context(), request.PathValue("id"))
	if err != nil {
		writeFeedReadError(response, request, err)
		return
	}
	now := d.currentTime()
	fingerprintInput := append([]byte(request.URL.RequestURI()+"\x00"), body...)
	begin, err := apiidempotency.Begin(request.Context(), d.Database, key, "admin", apiidempotency.Fingerprint(fingerprintInput), now)
	if err != nil {
		if errors.Is(err, apiidempotency.ErrKeyReused) {
			httpapi.WriteError(response, request, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was used with a different request")
			return
		}
		if errors.Is(err, apiidempotency.ErrMissingKey) {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "Idempotency-Key is required")
			return
		}
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "register feed test command failed")
		return
	}
	if !begin.Replayed {
		if d.FeedTest == nil {
			_ = apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, "INTERNAL_ERROR", "", now)
		} else {
			preview, testErr := d.FeedTest(request.Context(), feed)
			if testErr != nil {
				_ = apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, "INTERNAL_ERROR", "", now)
			} else {
				resultJSON, marshalErr := json.Marshal(feedPreviewPayload(feed, preview, now))
				if marshalErr != nil {
					_ = apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, "INTERNAL_ERROR", "", now)
				} else if err := apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, "", string(resultJSON), now); err != nil {
					httpapi.WriteError(response, request, "INTERNAL_ERROR", "complete feed test command failed")
					return
				}
			}
		}
	}
	command, err := commandPayload(request.Context(), d.Database, begin.CommandID)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "read feed test command failed")
		return
	}
	response.Header().Set("Location", "/api/v1/commands/"+url.PathEscape(begin.CommandID))
	httpapi.WriteJSON(response, http.StatusAccepted, command)
}

func feedPreviewPayload(feed rssservice.Feed, preview rsspreview.Preview, now time.Time) map[string]any {
	entries := make([]map[string]any, 0, len(preview.Entries))
	for _, entry := range preview.Entries {
		entries = append(entries, map[string]any{
			"id": fmt.Sprintf("%x", sha256.Sum256([]byte(entry.ID))), "title": entry.Title, "link": observability.PublicURL(entry.Link), "published_at": entry.Published,
		})
	}
	return map[string]any{
		"feed_id": feed.ID, "source": "READ_ONLY_FETCH", "checked_at": now.UTC().Format(time.RFC3339Nano),
		"feed_url": observability.PublicURL(preview.FeedURL), "format": preview.Format, "entries": entries,
	}
}

func (d Deps) feedRuntimePayload(request *http.Request, feed rssservice.Feed) map[string]any {
	payload := feedPayload(feed)
	var success, code sql.NullString
	var entries, matched int
	err := d.Database.QueryRowContext(request.Context(), `SELECT last_success_at,last_error_code,last_entries,last_matched FROM feed_runtime_state WHERE feed_id=?`, feed.ID).Scan(&success, &code, &entries, &matched)
	payload["last_success_at"] = nil
	payload["last_error_code"] = nil
	payload["last_entries"] = 0
	payload["last_matched"] = 0
	if err == nil {
		payload["last_success_at"] = nullableString(success)
		payload["last_error_code"] = nullableString(code)
		payload["last_entries"] = entries
		payload["last_matched"] = matched
	} else if err != sql.ErrNoRows {
		payload["last_error_code"] = "RUNTIME_STATUS_UNAVAILABLE"
	}
	return payload
}

func readFeedTestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return []byte("{}"), nil
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 64<<10+1))
	if err != nil {
		return nil, errors.New("request body is not valid JSON")
	}
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var body map[string]json.RawMessage
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, errors.New("request body must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON value")
	}
	if len(body) != 0 {
		return nil, errors.New("feed test request does not accept fields")
	}
	return raw, nil
}

func commandPayload(ctx context.Context, database *sql.DB, commandID string) (map[string]any, error) {
	var commandType, status, resultJSON, errorJSON, createdAt, startedAt, finishedAt, errorCode sql.NullString
	var revision int
	err := database.QueryRowContext(ctx, `SELECT command_type,command_status,revision,result_json,error_json,error_code,created_at,started_at,finished_at FROM api_commands WHERE command_id=?`, commandID).Scan(&commandType, &status, &revision, &resultJSON, &errorJSON, &errorCode, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"command_id": commandID, "command_type": commandType.String, "state": status.String, "revision": revision}
	if resultJSON.Valid {
		var result any
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return nil, fmt.Errorf("decode command result: %w", err)
		}
		payload["result"] = result
	} else {
		payload["result"] = nil
	}
	if errorCode.Valid {
		payload["error"] = map[string]any{"code": errorCode.String}
	} else if errorJSON.Valid {
		var failure any
		if err := json.Unmarshal([]byte(errorJSON.String), &failure); err != nil {
			return nil, fmt.Errorf("decode command error: %w", err)
		}
		payload["error"] = failure
	} else {
		payload["error"] = nil
	}
	for key, value := range map[string]sql.NullString{"created_at": createdAt, "started_at": startedAt, "finished_at": finishedAt} {
		if value.Valid {
			payload[key] = value.String
		}
	}
	return payload, nil
}

func (d Deps) currentTime() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func feedETag(feed rssservice.Feed) string { return fmt.Sprintf("%q", feed.UpdatedAt) }

func parseFeedETag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, ",") || strings.HasPrefix(value, "W/") {
		return "", errors.New("invalid feed ETag")
	}
	parsed, err := strconv.Unquote(value)
	if err != nil || strings.TrimSpace(parsed) == "" {
		return "", errors.New("invalid feed ETag")
	}
	return parsed, nil
}

func writeFeedCreateError(response http.ResponseWriter, request *http.Request, err error) {
	message := err.Error()
	switch {
	case errors.Is(err, rssservice.ErrSiteNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "feed site not found")
	case strings.HasPrefix(message, "invalid "):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", message)
	case strings.Contains(strings.ToUpper(message), "FOREIGN KEY"):
		httpapi.WriteError(response, request, "NOT_FOUND", "feed site not found")
	case strings.Contains(strings.ToUpper(message), "UNIQUE"):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "feed already exists")
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "create feed failed")
	}
}

func writeFeedReadError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, rssservice.ErrFeedNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "feed not found")
	case strings.HasPrefix(err.Error(), "invalid "):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "get feed failed")
	}
}

func writeFeedUpdateError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, rssservice.ErrFeedNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "feed not found")
	case errors.Is(err, rssservice.ErrFeedRevisionConflict):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "feed revision changed")
	case strings.Contains(strings.ToUpper(err.Error()), "UNIQUE"):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "feed already exists")
	case strings.HasPrefix(err.Error(), "invalid ") || strings.Contains(err.Error(), "revision is required") || strings.Contains(err.Error(), "no feed fields"):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "update feed failed")
	}
}
