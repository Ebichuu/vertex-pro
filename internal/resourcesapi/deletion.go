package resourcesapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
	"github.com/vertex-pro/vertex-pro/internal/deletionsync"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

// Deletion policy endpoints. Handlers only call the deletion service; they
// never touch SQL or qui directly. Nothing here deletes a torrent: qui does,
// for automations an operator activated after a recorded preview.

func registerDeletionRoutes(mux *http.ServeMux, deps Deps, guard func(func(http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("GET /api/v1/deletion/policies", guard(deps.listDeletionPolicies))
	mux.Handle("POST /api/v1/deletion/policies", guard(deps.createDeletionPolicy))
	mux.Handle("GET /api/v1/deletion/policies/{id}", guard(deps.getDeletionPolicy))
	mux.Handle("PUT /api/v1/deletion/policies/{id}", guard(deps.updateDeletionPolicy))
	mux.Handle("DELETE /api/v1/deletion/policies/{id}", guard(deps.archiveDeletionPolicy))
	mux.Handle("POST /api/v1/deletion/policies/{id}/preview", guard(deps.previewDeletionPolicy))
	mux.Handle("POST /api/v1/deletion/policies/{id}/activate", guard(deps.activateDeletionPolicy))
	mux.Handle("POST /api/v1/deletion/policies/{id}/deactivate", guard(deps.deactivateDeletionPolicy))
	mux.Handle("GET /api/v1/deletion/safety", guard(deps.getDeletionSafety))
	mux.Handle("PUT /api/v1/deletion/safety", guard(deps.updateDeletionSafety))
	mux.Handle("GET /api/v1/deletion/audit/{instance}", guard(deps.listDeletionAudit))
	mux.Handle("GET /api/v1/deletion/tag-backfill/{instance}", guard(deps.planTagBackfill))
	mux.Handle("POST /api/v1/deletion/tag-backfill/{instance}", guard(deps.applyTagBackfill))
}

func (d Deps) deletion(response http.ResponseWriter, request *http.Request) (deletionsync.Service, bool) {
	if d.Deletion == nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "deletion service is not configured")
		return deletionsync.Service{}, false
	}
	service, ok := d.Deletion()
	if !ok {
		httpapi.WriteError(response, request, "INSTANCE_UNAVAILABLE", "deletion service is not available")
		return deletionsync.Service{}, false
	}
	return service, true
}

// reconcileAfterChange pushes a changed policy set to qui. Failures are
// reported but do not undo the change: the background reconciler retries
// and every failure path in the syncer leaves automations disabled.
func reconcileAfterChange(ctx context.Context, service deletionsync.Service, instances []string) []string {
	var problems []string
	for _, instance := range instances {
		if _, err := service.Reconcile(ctx, instance); err != nil {
			problems = append(problems, instance+": "+syncProblem(err))
		}
	}
	return problems
}

func syncProblem(err error) string {
	switch {
	case errors.Is(err, deletionsync.ErrInstanceUnavailable):
		return "instance not connected"
	default:
		return "qui sync failed; retried in background"
	}
}

type deletionPolicyRequest struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Instances []string        `json:"instances"`
	Spec      json.RawMessage `json:"spec"`
}

func deletionPolicyPayload(record deletionsync.PolicyRecord) map[string]any {
	return map[string]any{"id": record.ID, "name": record.Name, "revision": record.Revision, "live": record.Live, "instances": record.Instances, "spec": record.Spec, "updated_at": record.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func writeDeletionError(response http.ResponseWriter, request *http.Request, err error) {
	var httpErr *control.HTTPError
	switch {
	case errors.Is(err, deletionpolicy.ErrInvalid):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, deletionsync.ErrNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "deletion policy not found")
	case errors.Is(err, deletionsync.ErrConflict):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "deletion policy revision is stale")
	case errors.Is(err, deletionsync.ErrNotPreviewed):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, deletionsync.ErrInstanceUnavailable):
		httpapi.WriteError(response, request, "INSTANCE_UNAVAILABLE", err.Error())
	case errors.As(err, &httpErr) && httpErr.ValidationMessage != "":
		httpapi.WriteError(response, request, "VALIDATION_FAILED", "qui rejected the policy: "+httpErr.ValidationMessage)
	case errors.As(err, &httpErr):
		httpapi.WriteError(response, request, "QUI_UNREACHABLE", "qui request failed")
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "deletion operation failed")
	}
}

func decodePolicyRequest(response http.ResponseWriter, request *http.Request) (deletionPolicyRequest, deletionpolicy.PolicySpec, bool) {
	var input deletionPolicyRequest
	if err := httpapi.DecodeJSON(response, request, &input, 256<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return input, deletionpolicy.PolicySpec{}, false
	}
	spec, err := deletionpolicy.DecodePolicySpec(input.Spec)
	if err != nil {
		writeDeletionError(response, request, err)
		return input, deletionpolicy.PolicySpec{}, false
	}
	return input, spec, true
}

func (d Deps) listDeletionPolicies(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	records, err := service.Store.Policies(request.Context(), false)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, deletionPolicyPayload(record))
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) getDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	record, err := service.Store.Policy(request.Context(), request.PathValue("id"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	response.Header().Set("ETag", ruleETag(record.Revision))
	httpapi.WriteJSON(response, http.StatusOK, deletionPolicyPayload(record))
}

func (d Deps) createDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	input, spec, ok := decodePolicyRequest(response, request)
	if !ok {
		return
	}
	record, err := service.Store.CreatePolicy(request.Context(), strings.TrimSpace(input.ID), input.Name, spec, input.Instances)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	payload := deletionPolicyPayload(record)
	payload["sync_problems"] = reconcileAfterChange(request.Context(), service, record.Instances)
	response.Header().Set("Location", "/api/v1/deletion/policies/"+record.ID)
	response.Header().Set("ETag", ruleETag(record.Revision))
	httpapi.WriteJSON(response, http.StatusCreated, payload)
}

func (d Deps) updateDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	revision, err := parseRuleRevision(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain a positive policy revision")
		return
	}
	input, spec, ok := decodePolicyRequest(response, request)
	if !ok {
		return
	}
	previous, err := service.Store.Policy(request.Context(), request.PathValue("id"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	record, err := service.Store.UpdatePolicy(request.Context(), previous.ID, revision, input.Name, spec, input.Instances)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	// Reconcile old and new scope: dropped instances must retire automations.
	scope := append(append([]string{}, previous.Instances...), record.Instances...)
	payload := deletionPolicyPayload(record)
	payload["sync_problems"] = reconcileAfterChange(request.Context(), service, uniqueStrings(scope))
	response.Header().Set("ETag", ruleETag(record.Revision))
	httpapi.WriteJSON(response, http.StatusOK, payload)
}

func (d Deps) archiveDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	revision, err := parseRuleRevision(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain a positive policy revision")
		return
	}
	record, err := service.Store.Policy(request.Context(), request.PathValue("id"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	if err := service.Store.ArchivePolicy(request.Context(), record.ID, revision); err != nil {
		writeDeletionError(response, request, err)
		return
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"id": record.ID, "archived": true, "sync_problems": reconcileAfterChange(request.Context(), service, record.Instances)})
}

func (d Deps) previewDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	var input struct {
		Limit int `json:"limit"`
	}
	if request.ContentLength != 0 {
		if err := httpapi.DecodeJSON(response, request, &input, 4<<10); err != nil {
			writeRuleRequestError(response, request, err)
			return
		}
	}
	if input.Limit <= 0 || input.Limit > 200 {
		input.Limit = 50
	}
	record, err := service.Store.Policy(request.Context(), request.PathValue("id"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	previews, err := service.Preview(request.Context(), record.ID, input.Limit)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	instances := map[string]any{}
	for instance, roles := range previews {
		items := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			examples := make([]map[string]any, 0, len(role.Examples))
			for _, torrent := range role.Examples {
				examples = append(examples, map[string]any{"hash": torrent.Hash, "name": torrent.Name, "size": torrent.Size, "ratio": torrent.Ratio, "seeding_time": torrent.SeedingTime, "tracker": torrent.Tracker, "category": torrent.Category, "tags": torrent.Tags, "state": torrent.State, "score": torrent.Score})
			}
			items = append(items, map[string]any{"role": role.Role, "total_matches": role.TotalMatches, "examples": examples})
		}
		instances[instance] = items
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"policy_id": record.ID, "revision": record.Revision, "instances": instances})
}

func (d Deps) activateDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	var input struct {
		Revision int `json:"revision"`
	}
	if err := httpapi.DecodeJSON(response, request, &input, 4<<10); err != nil || input.Revision <= 0 {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "revision of the previewed policy is required")
		return
	}
	id := request.PathValue("id")
	if err := service.Store.Activate(request.Context(), id, input.Revision); err != nil {
		writeDeletionError(response, request, err)
		return
	}
	record, err := service.Store.Policy(request.Context(), id)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	payload := deletionPolicyPayload(record)
	payload["sync_problems"] = reconcileAfterChange(request.Context(), service, record.Instances)
	httpapi.WriteJSON(response, http.StatusOK, payload)
}

func (d Deps) deactivateDeletionPolicy(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	id := request.PathValue("id")
	if err := service.Store.Deactivate(request.Context(), id); err != nil {
		writeDeletionError(response, request, err)
		return
	}
	record, err := service.Store.Policy(request.Context(), id)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	payload := deletionPolicyPayload(record)
	payload["sync_problems"] = reconcileAfterChange(request.Context(), service, record.Instances)
	httpapi.WriteJSON(response, http.StatusOK, payload)
}

func (d Deps) getDeletionSafety(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	record, err := service.Store.Safety(request.Context())
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	response.Header().Set("ETag", ruleETag(record.Revision))
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"revision": record.Revision, "spec": record.Spec})
}

func (d Deps) updateDeletionSafety(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	revision, err := parseRuleRevision(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain a positive safety revision")
		return
	}
	var input struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := httpapi.DecodeJSON(response, request, &input, 64<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	spec, err := deletionpolicy.DecodeSafetySpec(input.Spec)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	record, err := service.Store.UpdateSafety(request.Context(), revision, spec)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	// Every live policy was demoted; push that to every instance now.
	problems := []string{}
	if err := service.ReconcileAll(request.Context()); err != nil {
		problems = append(problems, syncProblem(err))
	}
	response.Header().Set("ETag", ruleETag(record.Revision))
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"revision": record.Revision, "spec": record.Spec, "sync_problems": problems})
}

func (d Deps) planTagBackfill(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	plan, err := service.BackfillPlan(request.Context(), request.PathValue("instance"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(plan))
	for _, item := range plan {
		items = append(items, map[string]any{"hash": item.Hash, "site": item.Site, "missing_tags": item.Missing})
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"instance_id": request.PathValue("instance"), "items": items})
}

func (d Deps) applyTagBackfill(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	applied, err := service.BackfillApply(request.Context(), request.PathValue("instance"))
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"instance_id": request.PathValue("instance"), "tagged": applied})
}

func auditPayload(entry deletionsync.AuditEntry) map[string]any {
	var details any = map[string]any{}
	if len(entry.Details) > 0 {
		_ = json.Unmarshal(entry.Details, &details)
	}
	payload := map[string]any{
		"instance_id": entry.Instance, "activity_id": entry.ActivityID, "hash": entry.Hash, "torrent_name": entry.TorrentName,
		"tracker_domain": entry.TrackerDomain, "action": entry.Action, "outcome": entry.Outcome, "reason": entry.Reason,
		"qui_rule_name": entry.QuiRuleName, "source": entry.Source, "details": details,
		"occurred_at": entry.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	for key, value := range map[string]string{"policy_id": entry.PolicyID, "policy_role": entry.PolicyRole, "vertex_rule_id": entry.VertexRuleID, "job_id": entry.JobID} {
		if value != "" {
			payload[key] = value
		}
	}
	if entry.PolicyRevision > 0 {
		payload["policy_revision"] = entry.PolicyRevision
	}
	return payload
}

// listDeletionAudit pages one instance's imported deletions, newest first.
// ?before=<activity_id> continues to older entries.
func (d Deps) listDeletionAudit(response http.ResponseWriter, request *http.Request) {
	service, ok := d.deletion(response, request)
	if !ok {
		return
	}
	instance := request.PathValue("instance")
	before, limit := 0, 100
	if raw := request.URL.Query().Get("before"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "before must be a positive activity ID")
			return
		}
		before = value
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 500 {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "limit must be between 1 and 500")
			return
		}
		limit = value
	}
	entries, err := service.Store.Audit(request.Context(), instance, before, limit)
	if err != nil {
		writeDeletionError(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, auditPayload(entry))
	}
	payload := map[string]any{"instance_id": instance, "items": items}
	if len(entries) == limit {
		payload["next_before"] = entries[len(entries)-1].ActivityID
	}
	if at, gap, err := service.Store.AuditGap(request.Context(), instance); err == nil && gap {
		// Older qui activity may have scrolled out before it was imported.
		payload["gap_detected_at"] = at.UTC().Format(time.RFC3339Nano)
	}
	httpapi.WriteJSON(response, http.StatusOK, payload)
}

// jobDeletions returns the deletions linked to a delivery job, or nil when
// the deletion service is not available.
func (d Deps) jobDeletions(ctx context.Context, jobID string) []map[string]any {
	if d.Deletion == nil {
		return nil
	}
	service, ok := d.Deletion()
	if !ok {
		return nil
	}
	entries, err := service.Store.AuditForJob(ctx, jobID)
	if err != nil {
		return nil
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, auditPayload(entry))
	}
	return items
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
