# 2026-10-27 删种 D4：qui activity 导入删种留痕

对应方案 `docs/plans/2026-10-26-deletion-engine.md` 的 D4。

## 依据（qui `bfe94b5e`）

- `GET /api/instances/{id}/automations/activity?limit=`：已持久化，按 `created_at DESC` 排序，limit 上限 1000，**没有游标**。
- activity ID 是 qui 全局自增，跨实例共享：同一实例的 ID 不连续。
- dry-run 和真实操作写进同一份日志，靠 `outcome` 区分（`dry-run` / `success` / `failed`）。
- 单次运行的明细（`activity/{id}`）只存在内存中，qui 重启后丢失，因此导入不依赖它。

## 迁移 `0027_deletion_audit.sql`

- `deletion_audit`：主键为（实例, qui activity ID），重复导入不会产生重复记录。
  - 字段：hash、名称、tracker 域名、动作、结果（只有 success 和 failed）、原因、qui 规则 ID 和名称、来源分类、策略/角色/修订号、旧规则 ID、关联的 job、明细 JSON、发生时间、导入时间。
  - 索引：按 job、按（实例, activity ID 倒序）。
- `deletion_audit_cursor`：每个实例已见过的最大 activity ID、上次导入时间、`gap_detected_at`。

## 导入（`deletionsync.AuditImporter`）

- 每轮拉取最新一页（1000 条），在同一个事务里写入：
  - **只导入删除尝试**（`deleted_*` 和 `delete_failed`），并且 outcome 必须是 `success` 或 `failed`；**dry-run 一律跳过**，标签修改等其他动作也跳过。
  - `INSERT OR IGNORE`：按（实例, ID）幂等。
- **来源分类**（按 qui 规则 ID）：
  - `policy`：命中 `deletion_policy_bindings`，记录策略、角色和修订号（修订号取自绑定，即最近一次同步时的修订号）；
  - `legacy_rule`：命中旧的 `qui_policy_bindings`（规则级做种时长自动化，没有安全外壳），记录 Vertex 规则 ID；
  - `external`：qui 里其他人的规则。这类删除照样记录，因为它们同样删掉了种子。
- **关联投递历史**：用（实例, hash）在 `placement_attempts` 里找最近一次 attempt 对应的 `job_id`。
- **缺口检测**：本页是满页，且最小 ID 仍大于之前见过的最大 ID，说明两次导入之间的 activity 超过一页，更早的记录可能已经滚出 qui 的列表。此时写入 `gap_detected_at` 并累加 `vertex_deletion_audit_gaps_total`。因为 ID 跨实例不连续，判断条件是“没有接上边界”，而不是“ID 不连续”，以避免误报。
- 调度：与策略同步共用每分钟一次的后台循环。只有存在删种策略、绑定或旧的规则级绑定时才会访问 qui；出错时累加 `vertex_deletion_audit_errors_total`。

## API

- `GET /api/v1/deletion/audit/{instance}?limit=&before=`：按 activity ID 倒序；满页时返回 `next_before` 用于翻页；如果检测到过缺口，返回 `gap_detected_at`。
- `GET /api/v1/jobs/{id}`：有关联删种记录时增加 `deletions` 字段。
- 已写入 `contracts/openapi/openapi.json`。

## 测试

- `deletionsync`：
  - 5 条 activity：policy 来源成功删除、legacy 来源删除失败、dry-run、标签修改、external 来源删除。导入恰好 3 条，来源、策略/角色/修订号、规则 ID、job 关联都正确；重复导入新增 0 条；翻页正确。
  - 缺口检测：首次导入（无游标）不报；满页且接上了上次的边界不报；满页且整页都比上次的最大 ID 新时报告并持久化。
- `resourcesapi`：审计列表与翻页、非法的 `before` 返回 400、job 详情里的 `deletions` 不包含 dry-run。

## 已知限制

- 两次导入之间产生超过 1000 条 activity（含所有实例的所有动作）时，可能漏掉记录；这种情况会被检测并暴露出来，但无法补回，因为 qui 没有游标。每分钟导入一次，正常规模下不会发生。
- `policy_revision` 是绑定上记录的修订号，即最近一次同步的修订号。如果在修订号变更的同步窗口内发生删除，可能差一个版本。
- `deletion_audit` 不设保留期限（留痕需要长期保存）。如需清理，应作为明确的配置项另行加入。
- 删种事件还没有推送到 event stream 或 Telegram，留给通知阶段。
