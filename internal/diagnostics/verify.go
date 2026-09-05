package diagnostics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/observability"
)

// VerifyChecks 是 HARD-10 verify/readiness 共用的离线检查集合：数据库可达、
// 迁移版本与期望一致、启动一致性、外键完整性。每条检查独立报告原因，
// 不合并成单一布尔。
func VerifyChecks(ctx context.Context, database *sql.DB, runner db.Runner, expectedVersions int) []Check {
	checks := make([]Check, 0, 4)
	ping := Check{Name: "database_ping"}
	if database == nil {
		ping.Detail = "database not configured"
	} else if err := database.PingContext(ctx); err != nil {
		ping.Detail = observability.RedactString(err.Error())
	} else {
		ping.OK = true
		ping.Detail = "ok"
	}
	checks = append(checks, ping)

	schema := Check{Name: "schema_migrations"}
	if !ping.OK {
		schema.Detail = "skipped: database unreachable"
	} else {
		applied, err := runner.AppliedVersion(ctx, database)
		if err != nil {
			schema.Detail = observability.RedactString(err.Error())
		} else if applied != expectedVersions {
			schema.Detail = fmt.Sprintf("applied version %d, expected %d (run migrations)", applied, expectedVersions)
		} else {
			schema.OK = true
			schema.Detail = fmt.Sprintf("version %d", applied)
		}
	}
	checks = append(checks, schema)

	consistency := Check{Name: "startup_consistency"}
	if !ping.OK {
		consistency.Detail = "skipped: database unreachable"
	} else if err := db.CheckStartupConsistency(ctx, database); err != nil {
		consistency.Detail = observability.RedactString(err.Error())
	} else {
		consistency.OK = true
		consistency.Detail = "ok"
	}
	checks = append(checks, consistency)

	foreignKeys := Check{Name: "foreign_key_check"}
	if !ping.OK {
		foreignKeys.Detail = "skipped: database unreachable"
	} else {
		rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			foreignKeys.Detail = observability.RedactString(err.Error())
		} else {
			violations := 0
			for rows.Next() {
				violations++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				foreignKeys.Detail = observability.RedactString(err.Error())
			} else if violations > 0 {
				foreignKeys.Detail = fmt.Sprintf("%d foreign key violations", violations)
			} else {
				foreignKeys.OK = true
				foreignKeys.Detail = "no violations"
			}
		}
	}
	checks = append(checks, foreignKeys)
	return checks
}

// AllOK 报告检查集合是否全部通过。
func AllOK(checks []Check) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

// Counts 返回运维关注的表行数，供 support-bundle 与容量观察使用。
func Counts(ctx context.Context, database *sql.DB) (map[string]int64, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	tables := []string{"race_jobs", "event_stream", "api_commands", "api_idempotency_records", "sync_snapshots", "instance_counter_samples"}
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table) // 表名来自固定清单，非用户输入
		if err := database.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return counts, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

// SupportBundle 在诊断报告之上追加数据库行数与保留策略摘要，作为提交给
// 维护者的完整支持包；digest 覆盖除自身外的全部字段。
type SupportBundle struct {
	Report    Report           `json:"report"`
	Counts    map[string]int64 `json:"counts"`
	Retention map[string]any   `json:"retention,omitempty"`
	Digest    string           `json:"digest"`
}

// BuildSupportBundle 组装支持包：数据库行数失败不静默，错误原样上报。
func BuildSupportBundle(ctx context.Context, report Report, database *sql.DB, retentionSummary map[string]any) (SupportBundle, error) {
	counts, err := Counts(ctx, database)
	if err != nil {
		return SupportBundle{}, err
	}
	bundle := SupportBundle{Report: report, Counts: counts, Retention: retentionSummary}
	payload, err := json.Marshal(bundle)
	if err != nil {
		return SupportBundle{}, fmt.Errorf("marshal support bundle: %w", err)
	}
	sum := sha256.Sum256(payload)
	bundle.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return bundle, nil
}
