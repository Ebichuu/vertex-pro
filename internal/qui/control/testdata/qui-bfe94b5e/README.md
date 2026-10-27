# qui bfe94b5e 契约 fixture

这些响应是按 qui `bfe94b5e4d63f213af747fab39719d47aae99d22` 的源码结构手工构造的，**不是**从真实 qui 抓取的流量。

| 文件 | 来源（qui 源码） |
|---|---|
| `preview_delete.json` | `internal/services/automations/service.go`：`PreviewResult`、`PreviewTorrent`（`POST /api/instances/{id}/automations/preview`） |
| `dry_run.json` | `internal/api/handlers/automations.go`：`AutomationDryRunResult`；`models.AutomationActivity`（`POST …/automations/dry-run`，状态码 202） |
| `activity.json` | `internal/models/automation_activity.go`：`AutomationActivity`、动作与结果常量；`ListByInstance` 按 `created_at DESC` 排序（`GET …/automations/activity?limit=`） |
| `error_preview_no_action.json` | `internal/api/handlers/helpers.go`：`RespondError` → `{"error": "..."}`；文案取自 `PreviewDeleteRule` |

fixture 里多出的字段（例如 `crossSeedCount`、`availability`）用于确认客户端能容忍 qui 新增字段。

**升级 qui 版本时**：对照上述源码文件重新核对这些 fixture；在 lab 环境运行 `go run ./tools/quprobe`，用 `VERTEX_PRO_QUI_PROBE_AUTOMATIONS=1` 打开只读的 preview/activity 检查。
