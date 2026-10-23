package historyproject

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UserStatusID 是 contracts/user_status_mapping.yaml 冻结的用户状态语言。
type UserStatusID string

// 用户状态标签与 contract visible_labels 一致，由 TestUserStatusLabelsMatchContract 守护。
var visibleLabels = map[UserStatusID]string{
	"WAITING_RULE":       "等待规则",
	"PREPARING_SEED":     "准备种子",
	"WAITING_DOWNLOADER": "等待下载器",
	"ADDING":             "正在添加",
	"DOWNLOADING":        "下载中",
	"SEEDING":            "做种中",
	"COMPLETED":          "已完成",
	"DELETED":            "已删除",
	"FAILED":             "失败",
	"NEEDS_CONFIRMATION": "需确认",
}

// precedence 固定合同中的判定顺序；第一条命中即返回。
var precedence = []UserStatusID{
	"NEEDS_CONFIRMATION", "FAILED", "DELETED", "COMPLETED", "SEEDING",
	"DOWNLOADING", "ADDING", "WAITING_DOWNLOADER", "PREPARING_SEED", "WAITING_RULE",
}

// deliveryChainJobStates 是投递链已开始的 Job 状态。
var deliveryChainJobStates = map[string]bool{
	"RESERVED": true, "ADD_SENT": true, "AWAITING_CONFIRMATION": true, "HASH_SEEN": true,
	"ALREADY_PRESENT": true, "ADD_FAILED": true, "UNKNOWN": true,
	"RECONCILED_CONFIRMED": true, "RECONCILED_NO_EFFECT": true, "EXPIRED_UNKNOWN": true,
}

// Facts 是投影所需的内部状态快照（诊断字段，绝不进入用户可见输出）。
type Facts struct {
	JobState           string
	RuleState          string
	TorrentState       string
	PlacementState     string
	ObservationState   string
	TorrentObservation string
	HistoryState       string
	DeletionFact       string
	InstanceAvailable  bool
}

// Status 是一次映射结果。
type Status struct {
	ID    UserStatusID
	Label string
}

// ProjectStatus 把内部状态映射为唯一用户状态：先按合同 when_any 与优先级
// 判定，未命中的内部状态使用固定兜底（均不会把 UNKNOWN/EXPIRED_UNKNOWN 等
// 不可证明状态美化为成功）：
//
//	HASH_SEEN / RECONCILED_CONFIRMED -> ADDING（副作用已确认但观测尚未报告）
//	ALREADY_PRESENT / RECONCILED_NO_EFFECT / EXPIRED -> FAILED（本次未交付）
//	READY -> PREPARING_SEED（等待 Placement 最终事务）
func ProjectStatus(facts Facts) Status {
	for _, status := range precedence {
		if matches(status, facts) {
			return Status{ID: status, Label: visibleLabels[status]}
		}
	}
	switch facts.JobState {
	case "HASH_SEEN", "RECONCILED_CONFIRMED":
		return Status{ID: "ADDING", Label: visibleLabels["ADDING"]}
	case "ALREADY_PRESENT", "RECONCILED_NO_EFFECT", "EXPIRED":
		return Status{ID: "FAILED", Label: visibleLabels["FAILED"]}
	case "READY":
		return Status{ID: "PREPARING_SEED", Label: visibleLabels["PREPARING_SEED"]}
	}
	return Status{ID: "PREPARING_SEED", Label: visibleLabels["PREPARING_SEED"]}
}

func matches(status UserStatusID, facts Facts) bool {
	switch status {
	case "NEEDS_CONFIRMATION":
		return facts.JobState == "UNKNOWN" || facts.JobState == "EXPIRED_UNKNOWN"
	case "FAILED":
		return facts.JobState == "PREPARE_FAILED" || facts.JobState == "ADD_FAILED" ||
			facts.RuleState == "REJECTED" || facts.TorrentState == "FAILED"
	case "DELETED":
		return facts.HistoryState == "REMOVED_EXTERNALLY" || facts.DeletionFact == "CONFIRMED"
	case "COMPLETED":
		return facts.ObservationState == "COMPLETED" || facts.TorrentObservation == "COMPLETED"
	case "SEEDING":
		return facts.TorrentObservation == "SEEDING"
	case "DOWNLOADING":
		return facts.TorrentObservation == "DOWNLOADING"
	case "ADDING":
		return facts.JobState == "RESERVED" || facts.JobState == "ADD_SENT" || facts.JobState == "AWAITING_CONFIRMATION"
	case "WAITING_DOWNLOADER":
		return facts.JobState == "WAITING_CAPACITY" || facts.PlacementState == "WAITING_CAPACITY" || !facts.InstanceAvailable
	case "PREPARING_SEED":
		// 局部修正（已记录）：delivery 链状态（RESERVED..RECONCILED_*）期间
		// placement_state 仍为 PREPARED，但合同意图是 PREPARING_SEED 仅表示
		// 投递尚未开始；否则 HASH_SEEN 会错误显示为"准备种子"。
		if deliveryChainJobStates[facts.JobState] {
			return false
		}
		return facts.JobState == "PREPARING" || facts.RuleState == "RUNNING" ||
			facts.TorrentState == "FETCHING" || facts.PlacementState == "PREPARED"
	case "WAITING_RULE":
		return facts.JobState == "DISCOVERED" || facts.RuleState == "PENDING"
	}
	return false
}

// Entry 是 History 列表的一条投影：来源、种子、目标、用户状态与时间线
// summary。字段名与值不得包含 Reservation/SubmitIntent/Candidate 等内部
// 术语，也不得携带容量与投递链诊断细节。
type Entry struct {
	JobID          string
	Title          string
	SiteName       string
	InstanceID     string
	UserStatus     Status
	DiscoveredAt   string
	AddedAt        string
	ConfirmedAt    string
	FinishedAt     string
	AttemptCount   int
	DeletedAt      string
	DeletionSource string
}

// Project 生成全部 RaceJob 的 History 投影，按发现时间倒序、job_id 稳定排序。
// 删除事实（HIS-02）左连接进入投影：binding 已删除的历史仍保留删除发现
// 时间与来源，用户状态按合同显示"已删除"。
type Projection struct{}

func (Projection) Project(ctx context.Context, database *sql.DB) ([]Entry, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	rows, err := database.QueryContext(ctx, `SELECT j.job_id,j.job_state,j.rule_state,j.torrent_state,j.placement_state,j.observation_state,
COALESCE(NULLIF(j.effective_target_instance_id,''),a.instance_id,''),j.created_at,
COALESCE(NULLIF(i.title,''),NULLIF(o.title,''),rs.title,''),COALESCE(s.name,''),COALESCE(a.attempt_no,0),COALESCE(a.started_at,''),COALESCE(a.confirmed_at,''),
COALESCE(NULLIF(o.completed_at,''),CASE WHEN j.observation_state='COMPLETED' THEN a.finished_at ELSE '' END,''),COALESCE(h.deletion_source,CASE WHEN o.deleted_at!='' THEN 'UNKNOWN_ORIGIN' END,''),COALESCE(h.deleted_at,o.deleted_at,''),COALESCE(o.state,'')
FROM race_jobs j LEFT JOIN identities i ON i.canonical_hash=j.identity_key
LEFT JOIN placement_attempts a ON a.job_id=j.job_id AND a.attempt_no=(SELECT MAX(attempt_no) FROM placement_attempts WHERE job_id=j.job_id)
LEFT JOIN rss_sources rs ON rs.site_id||'/'||rs.source_key=j.source_id
LEFT JOIN sites s ON s.site_id=COALESCE(rs.site_id,(SELECT MIN(site_id) FROM identity_sources WHERE identity_id=i.identity_id))
LEFT JOIN history_deletion_facts h ON h.fact_id=(SELECT fact_id FROM history_deletion_facts WHERE job_id=j.job_id ORDER BY deleted_at DESC LIMIT 1)
LEFT JOIN job_observations o ON o.job_id=j.job_id
ORDER BY j.created_at DESC,j.job_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		var jobState, ruleState, torrentState, placementState, observationState, liveState string
		if err := rows.Scan(&entry.JobID, &jobState, &ruleState, &torrentState, &placementState, &observationState, &entry.InstanceID, &entry.DiscoveredAt, &entry.Title, &entry.SiteName, &entry.AttemptCount, &entry.AddedAt, &entry.ConfirmedAt, &entry.FinishedAt, &entry.DeletionSource, &entry.DeletedAt, &liveState); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		// 实例可用性由 Placement 的 WAITING_CAPACITY 显式表达；
		// 健康探测接续（OBS-02）后再并入该事实。
		facts := Facts{JobState: jobState, RuleState: ruleState, TorrentState: torrentState, PlacementState: placementState, ObservationState: observationState, InstanceAvailable: true, TorrentObservation: liveState}
		if entry.DeletedAt != "" {
			facts.DeletionFact = "CONFIRMED"
		}
		entry.UserStatus = ProjectStatus(facts)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return entries, nil
}

// HiddenTerms 返回用户可见输出中禁止出现的内部术语（来自合同 safety 段）。
func HiddenTerms() []string {
	return []string{"Candidate", "Placement", "Reservation", "Reflection", "SubmitIntent", "capacity_epoch"}
}

// ValidateEntry 检查投影输出不泄漏内部术语。
func ValidateEntry(entry Entry) error {
	serialized := fmt.Sprintf("%+v", entry)
	for _, term := range HiddenTerms() {
		if strings.Contains(serialized, term) {
			return fmt.Errorf("history entry leaks internal term %s", term)
		}
	}
	if _, ok := visibleLabels[entry.UserStatus.ID]; !ok {
		return fmt.Errorf("history entry carries unknown user status %s", entry.UserStatus.ID)
	}
	return nil
}
