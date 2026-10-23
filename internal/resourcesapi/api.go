package resourcesapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/apipaging"
	"github.com/vertex-pro/vertex-pro/internal/historyproject"
	"github.com/vertex-pro/vertex-pro/internal/historytimeline"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/settings"
	"github.com/vertex-pro/vertex-pro/internal/statssummary"
)

// Deps 聚合资源 API 依赖；handler 不开事务、不碰 Repository 细节，
// 只调用读模型与应用服务（§17.1 Handler 模板）。
type Deps struct {
	Database       *sql.DB
	Auth           *adminauth.Service
	Web            *httpapi.WebProtection
	Validator      *ResponseValidator
	Now            func() time.Time
	Realtime       *realtimecards.Registry
	RSS            *rssservice.Service
	FeedTest       FeedTester
	Rules          *ruleservice.Service
	Events         http.Handler
	SessionTTL     time.Duration
	Settings       *settings.Service
	ActiveSettings settings.Values
	Version        string
	// SnapshotRoot enables server-verified evidence for manual exception actions.
	SnapshotRoot string
}

// Router 返回资源端点与 acknowledge-unknown 命令端点的处理器。
// 所有端点要求有效管理员会话（认证前端点豁免）。

func Router(deps Deps) http.Handler {
	mux := http.NewServeMux()
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	guard := func(handler func(http.ResponseWriter, *http.Request)) http.Handler {
		return requireSession(deps, http.HandlerFunc(handler))
	}
	guardHandler := func(handler http.Handler) http.Handler {
		return requireSession(deps, handler)
	}
	mux.Handle("GET /api/v1/auth/status", http.HandlerFunc(deps.authStatus))
	mux.Handle("POST /api/v1/auth/initialize", http.HandlerFunc(deps.initializeAuth))
	mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(deps.loginAuth))
	mux.Handle("POST /api/v1/auth/logout", guardHandler(http.HandlerFunc(deps.logoutAuth)))
	mux.Handle("GET /api/v1/settings", settingsGuard(deps, deps.getSettings))
	mux.Handle("PUT /api/v1/settings", settingsGuard(deps, deps.updateSettings))
	mux.Handle("GET /api/v1/settings/diagnostics", settingsGuard(deps, deps.downloadDiagnostics))
	mux.Handle("GET /api/v1/sites", guard(deps.listSites))
	mux.Handle("POST /api/v1/sites", guard(deps.createSite))
	mux.Handle("GET /api/v1/feeds", guard(deps.listFeeds))
	mux.Handle("GET /api/v1/feeds/{id}", guard(deps.getFeed))
	mux.Handle("POST /api/v1/feeds", guard(deps.createFeed))
	mux.Handle("PUT /api/v1/feeds/{id}", guard(deps.updateFeed))
	mux.Handle("POST /api/v1/feeds/{id}/test", guard(deps.testFeed))
	mux.Handle("GET /api/v1/rules", guard(deps.listRules))
	mux.Handle("GET /api/v1/rules/{id}", guard(deps.getRule))
	mux.Handle("POST /api/v1/rules", guard(deps.createRule))
	mux.Handle("PUT /api/v1/rules/{id}", guard(deps.updateRule))
	mux.Handle("POST /api/v1/rules/dry-run", guard(deps.dryRunRules))
	mux.Handle("POST /api/v1/rules/{id}/dry-run", guard(deps.dryRunRule))
	mux.Handle("GET /api/v1/jobs", guard(deps.listJobs))
	mux.Handle("GET /api/v1/jobs/{id}", guard(deps.getJob))
	mux.Handle("GET /api/v1/instances", guard(deps.listInstances))
	mux.Handle("GET /api/v1/commands/{command_id}", guard(deps.getCommand))
	mux.Handle("GET /api/v1/stats/downloaders/summary", guard(deps.statsSummary))
	mux.Handle("GET /api/v1/stats/downloaders/timeseries", guard(deps.statsSummary))
	mux.Handle("GET /api/v1/dashboard", guard(deps.dashboard))
	mux.Handle("GET /api/v1/stats/downloaders/realtime", guard(deps.statsRealtime))
	mux.Handle("POST /api/v1/jobs/{id}/acknowledge-unknown", guard(deps.acknowledgeUnknown))
	if deps.Events != nil {
		mux.Handle("GET /api/v1/events/stream", guardHandler(deps.Events))
	}
	handler := http.Handler(mux)
	if deps.Web != nil {
		handler = deps.Web.Middleware(handler)
	}
	if deps.Validator != nil {
		handler = deps.Validator.Middleware(handler)
	}
	return handler
}

func requireSession(deps Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if deps.Auth == nil {
			next.ServeHTTP(response, request)
			return
		}
		cookie, err := request.Cookie(httpapi.SessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			httpapi.WriteError(response, request, "UNAUTHENTICATED", "session required")
			return
		}
		now := time.Now()
		if deps.Now != nil {
			now = deps.Now()
		}
		valid, err := deps.Auth.ValidateSession(request.Context(), cookie.Value, now)
		if err != nil || !valid {
			httpapi.WriteError(response, request, "UNAUTHENTICATED", "session invalid or expired")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (d Deps) listSites(response http.ResponseWriter, request *http.Request) {
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "site service is not configured")
		return
	}
	sites, err := service.ListSites(request.Context())
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query sites failed")
		return
	}
	feeds, err := service.ListFeeds(request.Context(), "")
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query feeds failed")
		return
	}
	feedsBySite := make(map[string][]map[string]any, len(sites))
	for _, feed := range feeds {
		feedsBySite[feed.SiteID] = append(feedsBySite[feed.SiteID], feedPayload(feed))
	}
	items := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		siteFeeds := feedsBySite[site.ID]
		if siteFeeds == nil {
			siteFeeds = []map[string]any{}
		}
		items = append(items, map[string]any{
			"id": site.ID, "name": site.Name, "base_url": site.BaseURL, "enabled": site.Enabled,
			"feeds": siteFeeds,
		})
	}
	d.writePage(response, request, items, nil)
}

func (d Deps) listFeeds(response http.ResponseWriter, request *http.Request) {
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "feed service is not configured")
		return
	}
	feeds, err := service.ListFeeds(request.Context(), request.URL.Query().Get("site_id"))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid site ID") {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "site_id is invalid")
			return
		}
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query feeds failed")
		return
	}
	items := make([]map[string]any, 0, len(feeds))
	for _, feed := range feeds {
		items = append(items, feedPayload(feed))
	}
	d.writePage(response, request, items, nil)
}

func (d Deps) rssService() (rssservice.Service, error) {
	if d.RSS != nil {
		return *d.RSS, nil
	}
	return rssservice.New(d.Database)
}

func feedPayload(feed rssservice.Feed) map[string]any {
	return map[string]any{
		"id": feed.ID, "site_id": feed.SiteID, "name": feed.Name, "feed_url": feed.FeedURL,
		"enabled": feed.Enabled, "poll_minutes": feed.PollMinutes, "etag": nullableString(feed.ETag), "last_modified": nullableString(feed.LastModified),
		"updated_at": feed.UpdatedAt,
	}
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func (d Deps) listRules(response http.ResponseWriter, request *http.Request) {
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service is not configured")
		return
	}
	rules, err := service.List(request.Context(), request.URL.Query().Get("site_id"))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid site ID") {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "site_id is invalid")
			return
		}
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query rules failed")
		return
	}
	items := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		items = append(items, rulePayload(rule))
	}
	d.writePage(response, request, items, func(item map[string]any) string {
		return fmt.Sprintf("%020d:%020d:%s", int64(^uint64(0)>>1)-int64(item["priority"].(int)), int64(^uint64(0)>>1)-int64(item["revision"].(int)), item["id"])
	})
}

func (d Deps) ruleService() (ruleservice.Service, error) {
	if d.Rules != nil {
		return *d.Rules, nil
	}
	return ruleservice.New(d.Database, nil)
}

func rulePayload(rule ruleservice.Rule) map[string]any {
	var siteID any
	if rule.SiteID != "" {
		siteID = rule.SiteID
	}
	target := ""
	if match, err := ruleservice.MatchFor(rule, ""); err == nil {
		target = match.TargetInstanceID
		if match.TargetMode == "INSTANCE_GROUP" {
			target = match.TargetGroupID
		}
	}
	return map[string]any{
		"id": rule.ID, "site_id": siteID, "name": rule.Name, "enabled": rule.Enabled,
		"priority": rule.Priority, "revision": rule.Revision, "state": rule.State,
		"target": target, "updated_at": rule.UpdatedAt.Format(time.RFC3339Nano), "definition": rule.Definition,
	}
}

func (d Deps) listJobs(response http.ResponseWriter, request *http.Request) {
	limit, cursor, err := readPage(request)
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", err.Error())
		return
	}
	entries, err := historyEntries(d, request)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query jobs failed")
		return
	}
	query := request.URL.Query()
	statusFilter := query.Get("user_status")
	downloaderFilter := query.Get("downloader")
	filtered := make([]historyproject.Entry, 0, len(entries))
	for _, entry := range entries {
		if statusFilter != "" && string(entry.UserStatus.ID) != statusFilter {
			continue
		}
		if downloaderFilter != "" && entry.InstanceID != downloaderFilter {
			continue
		}
		filtered = append(filtered, entry)
	}
	// 删除期间稳定的键元组分页：created_at DESC + job_id DESC。
	cursorParam := query.Get("cursor")
	paged := make([]historyproject.Entry, 0, limit)
	nextCursor := ""
	for _, entry := range filtered {
		if cursorParam != "" {
			if !(entry.DiscoveredAt < cursor.SortKey || (entry.DiscoveredAt == cursor.SortKey && entry.JobID < cursor.ID)) {
				continue
			}
		}
		if len(paged) == limit {
			nextCursor = apipaging.Encode(apipaging.Cursor{SortKey: paged[limit-1].DiscoveredAt, ID: paged[limit-1].JobID})
			break
		}
		paged = append(paged, entry)
	}
	items := make([]map[string]any, 0, len(paged))
	for _, entry := range paged {
		item := historyPayload(entry)
		if string(entry.UserStatus.ID) == "NEEDS_CONFIRMATION" {
			for key, value := range d.exceptionPayload(request, entry.JobID) {
				item[key] = value
			}
		}
		items = append(items, item)
	}
	d.writeData(response, request, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (d Deps) getJob(response http.ResponseWriter, request *http.Request) {
	jobID := request.PathValue("id")
	if strings.TrimSpace(jobID) == "" {
		httpapi.WriteError(response, request, "NOT_FOUND", "job not found")
		return
	}
	var title string
	var jobState string
	err := d.Database.QueryRowContext(request.Context(), `SELECT COALESCE(i.title,''), j.job_state FROM race_jobs j LEFT JOIN identities i ON i.canonical_hash = j.identity_key WHERE j.job_id = ?`, jobID).Scan(&title, &jobState)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(response, request, "NOT_FOUND", "job not found")
		return
	}
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query job failed")
		return
	}
	// HIS-03 详情时间线：服务端按 occurred_at/event_id 稳定排序，stage 输出
	// 用户可读阶段名（§16.6），内部 from/to 状态仅作为诊断字段保留。
	events, err := historytimeline.ForJob(request.Context(), d.Database, jobID)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query job timeline failed")
		return
	}
	timeline := make([]map[string]any, 0, len(events))
	for _, event := range events {
		timeline = append(timeline, map[string]any{
			"event_id":    event.EventID,
			"stage":       event.Stage(),
			"from_state":  event.FromState,
			"to_state":    event.ToState,
			"reason_code": event.ReasonCode,
			"occurred_at": event.OccurredAt,
		})
	}
	payload := map[string]any{"id": jobID, "title": title, "job_state": jobState, "timeline": timeline}
	entries, err := historyEntries(d, request)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query job projection failed")
		return
	}
	for _, entry := range entries {
		if entry.JobID == jobID {
			for key, value := range historyPayload(entry) {
				payload[key] = value
			}
			break
		}
	}
	for key, value := range d.exceptionPayload(request, jobID) {
		payload[key] = value
	}
	d.addJobFacts(request, jobID, payload)
	d.writeData(response, request, payload)
}

func (d Deps) listInstances(response http.ResponseWriter, request *http.Request) {
	rows, err := d.Database.QueryContext(request.Context(), `SELECT s.instance_id, s.sync_state, s.snapshot_revision,COALESCE(m.name,s.instance_id),m.enabled FROM sync_instance_state s LEFT JOIN instance_metadata m ON m.instance_id=s.instance_id ORDER BY s.instance_id`)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query instances failed")
		return
	}
	type instanceRow struct {
		instanceID, syncState string
		revision              int64
		name                  string
		enabled               sql.NullBool
	}
	collected := []instanceRow{}
	for rows.Next() {
		var row instanceRow
		if err := rows.Scan(&row.instanceID, &row.syncState, &row.revision, &row.name, &row.enabled); err != nil {
			rows.Close()
			httpapi.WriteError(response, request, "INTERNAL_ERROR", "scan instances failed")
			return
		}
		collected = append(collected, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "iterate instances failed")
		return
	}
	// API-11 data quality：freshness/source/profile/capacity 状态与原因码
	// 原样输出，UNKNOWN/STALE 绝不美化为 healthy。外层 rows 必须先关闭，
	// 才能在单连接 SQLite 上执行嵌套查询。
	items := []map[string]any{}
	for _, row := range collected {
		quality := map[string]any{
			"sync_state": row.syncState, "snapshot_revision": row.revision,
			"freshness": freshnessFor(row.syncState), "realtime_source": realtimeSource(d, row.instanceID),
		}
		var capacityState, storageState string
		_ = d.Database.QueryRowContext(request.Context(), `SELECT COALESCE(MAX(capacity_state),'') FROM capacity_epochs e JOIN storage_targets t ON t.storage_target_id = e.storage_target_id WHERE t.instance_id = ?`, row.instanceID).Scan(&capacityState)
		_ = d.Database.QueryRowContext(request.Context(), `SELECT MIN(storage_verification_state) FROM storage_targets WHERE instance_id = ?`, row.instanceID).Scan(&storageState)
		quality["capacity_state"] = capacityState
		quality["storage_state"] = storageState
		quality["reason_codes"] = qualityReasons(row.syncState, capacityState, storageState)
		if d.Realtime != nil {
			card := d.Realtime.CardForInstance(row.instanceID, d.Now())
			quality["realtime_freshness"] = card.Freshness
		}
		var enabled any
		if row.enabled.Valid {
			enabled = row.enabled.Bool
		}
		items = append(items, map[string]any{"id": row.instanceID, "name": row.name, "enabled": enabled, "sync_state": row.syncState, "snapshot_revision": row.revision, "data_quality": quality})
	}
	d.writePage(response, request, items, nil)
}
func (d Deps) getCommand(response http.ResponseWriter, request *http.Request) {
	commandID := request.PathValue("command_id")
	payload, err := commandPayload(request.Context(), d.Database, commandID)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(response, request, "NOT_FOUND", "command not found")
		return
	}
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query command failed")
		return
	}
	d.writeData(response, request, payload)
}

func (d Deps) statsSummary(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	rangeKind := statssummary.Range(strings.ToUpper(query.Get("range")))
	if rangeKind == "" {
		rangeKind = statssummary.RangeDay
	}
	anchor := d.currentTime()
	if query.Get("anchor") != "" {
		parsed, err := time.Parse(time.RFC3339, query.Get("anchor"))
		if err != nil {
			httpapi.WriteError(response, request, "VALIDATION_FAILED", "anchor is not RFC3339")
			return
		}
		anchor = parsed
	}
	summary, err := statssummary.Summarize(request.Context(), d.Database, statssummary.Request{
		InstanceID: query.Get("instance_id"), Range: rangeKind, Anchor: anchor,
		Limit:  intFromQuery(query.Get("limit")),
		Cursor: query.Get("cursor"),
	})
	if err != nil {
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
		return
	}
	d.writeData(response, request, map[string]any{
		"instance_id": summary.InstanceID, "range": string(summary.Range),
		"range_start": summary.RangeStart, "range_end": summary.RangeEnd,
		"download_bytes": summary.DownloadBytes, "upload_bytes": summary.UploadBytes,
		"series": seriesPayload(summary), "next_cursor": summary.NextCursor,
		"missing_count": summary.MissingCount,
	})
}

func (d Deps) dashboard(response http.ResponseWriter, request *http.Request) {
	entries, err := historyEntries(d, request)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query dashboard failed")
		return
	}
	counts := map[string]int{}
	for _, entry := range entries {
		counts[string(entry.UserStatus.ID)]++
	}
	cards, err := d.instanceCards(request)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "query instance cards failed")
		return
	}
	d.writeData(response, request, map[string]any{
		"jobs_total": len(entries), "user_status_counts": counts, "instances": cards,
	})
}

// instanceCards 是 §18.2 的 Dashboard 汇总投影：单条聚合查询取全部实例的
// 同步状态、容量与验证状态，再合并实时卡片；浏览器不逐实例调用 qB。
func (d Deps) instanceCards(request *http.Request) ([]map[string]any, error) {
	rows, err := d.Database.QueryContext(request.Context(), `SELECT s.instance_id, s.sync_state, s.snapshot_revision, COALESCE(e.capacity_state,''), COALESCE(MAX(e.physical_free_bytes), -1), COALESCE(MAX(e.effective_free_bytes), -1), COALESCE(MIN(t.storage_verification_state),''),COALESCE(m.name,s.instance_id) FROM sync_instance_state s LEFT JOIN instance_metadata m ON m.instance_id=s.instance_id LEFT JOIN storage_targets t ON t.instance_id = s.instance_id LEFT JOIN capacity_epochs e ON e.storage_target_id = t.storage_target_id GROUP BY s.instance_id, s.sync_state, s.snapshot_revision ORDER BY s.instance_id`)
	if err != nil {
		return nil, fmt.Errorf("query instance cards: %w", err)
	}
	defer rows.Close()
	cards := make([]map[string]any, 0)
	for rows.Next() {
		var card map[string]any
		if err := scanCard(rows, &card, d, request); err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, rows.Err()
}

func historyEntries(d Deps, request *http.Request) ([]historyproject.Entry, error) {
	projection := historyproject.Projection{}
	return projection.Project(request.Context(), d.Database)
}

func seriesPayload(summary statssummary.Summary) []map[string]any {
	series := make([]map[string]any, 0, len(summary.Series))
	for _, point := range summary.Series {
		series = append(series, map[string]any{
			"bucket_start": point.BucketStart, "download_bytes": point.DownloadBytes,
			"upload_bytes": point.UploadBytes, "missing": point.Missing,
		})
	}
	return series
}

func intFromQuery(raw string) int {
	var value int
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0
		}
		value = value*10 + int(digit-'0')
	}
	return value
}

func (d Deps) writeData(response http.ResponseWriter, request *http.Request, payload map[string]any) {
	if d.Validator != nil {
		if err := d.Validator.Validate(request.URL.Path, payload); err != nil {
			httpapi.WriteError(response, request, "INTERNAL_ERROR", "response schema validation failed")
			return
		}
	}
	httpapi.WriteJSON(response, http.StatusOK, payload)
}

// statsRealtime 返回实时瞬时卡片快照；断线后由本 REST 端点重建，
// 瞬时数据不占持久 event ID。
func (d Deps) statsRealtime(response http.ResponseWriter, request *http.Request) {
	if d.Realtime == nil {
		httpapi.WriteError(response, request, "NOT_FOUND", "realtime registry not configured")
		return
	}
	cards := d.Realtime.Snapshot(d.Now())
	items := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		items = append(items, map[string]any{
			"instance_id": card.InstanceID, "sync_state": card.SyncState,
			"download_rate": card.DownloadRate, "upload_rate": card.UploadRate,
			"downloading_count": card.DownloadingCount, "seeding_count": card.SeedingCount,
			"source": card.Source, "sampled_at": card.SampledAt.UTC().Format(time.RFC3339Nano),
			"freshness": card.Freshness,
		})
	}
	d.writeData(response, request, map[string]any{"items": items})
}

func freshnessFor(syncState string) string {
	if syncState == "READY" {
		return "FRESH"
	}
	return "NOT_FRESH"
}

func realtimeSource(d Deps, instanceID string) string {
	if d.Realtime == nil {
		return ""
	}
	return d.Realtime.CardForInstance(instanceID, d.Now()).Source
}

// qualityReasons 汇集数据质量原因码；UNKNOWN/STALE 原样保留。
func qualityReasons(syncState, capacityState, storageState string) []string {
	reasons := []string{}
	if syncState != "READY" {
		reasons = append(reasons, "SNAPSHOT_STALE")
	}
	if capacityState == "CAPACITY_UNCERTAIN" || capacityState == "RECONCILING" {
		reasons = append(reasons, capacityState)
	}
	if storageState == "UNVERIFIED" {
		reasons = append(reasons, "STORAGE_UNVERIFIED")
	}
	return reasons
}
