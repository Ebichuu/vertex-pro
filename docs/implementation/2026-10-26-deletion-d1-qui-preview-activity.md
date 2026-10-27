# 2026-10-26 删种 D1：qui preview / dry-run / activity 客户端与契约测试

对应方案：`docs/plans/2026-10-26-deletion-engine.md` 中的 D1。目标 qui 版本：`bfe94b5e4d63f213af747fab39719d47aae99d22`（用户确认的部署版本）。

## 核对过的 qui 行为（源码依据）

| 接口 | qui 实现 | 要点 |
|---|---|---|
| `POST /api/instances/{id}/automations/preview` | `handlers.PreviewDeleteRule` → `Service.PreviewDeleteRule` | 只读。payload 同 `AutomationPayload`，外加 `previewLimit`、`previewOffset`、`previewView`（`needed`：FREE_SPACE 规则只列出达标前需要删的；`eligible`：列出全部命中的）。同时启用 delete 和 category 会返回 400，两者都没有也返回 400 |
| `POST …/automations/dry-run` | `handlers.DryRunNow` → `Service.ApplyRuleDryRun` | 服务端强制 `DryRun=true`；`applyRulesForInstance` 在 dry-run 分支**先记录 activity 再 return**，删除逻辑在这之后，所以 dry-run 不会删除任何种子。但它会往 activity 日志里写入 outcome 为 `dry-run` 的记录。返回 202 |
| `GET …/automations/activity?limit=` | `handlers.ListActivity` → `AutomationActivityStore.ListByInstance` | 已持久化；`ORDER BY created_at DESC`；limit 上限 1000，**没有游标** |
| `GET …/automations/activity/{id}` | `Service.GetActivityRun` | **只存在内存中**，qui 重启后丢失，因此 D4 的留痕不能依赖它（本次没有接入） |
| `POST …/automations/apply` | `ApplyNow`：立即强制执行 | **Vertex 刻意不提供这个客户端方法** |
| 错误体 | `RespondError` → `{"error": "..."}` | |

## 改动

- `internal/qui/control/client.go`
  - `getJSON` 改为通用的 `doJSON`：支持 POST/JSON 请求体，响应体上限 16 MiB。
  - `HTTPError` 新增 `ValidationMessage`：只在 400/422 时填充 qui 的错误文案，长度上限 4 KiB，**不写进 `Error()`**，保持原有“错误信息不泄露响应体”的约束（`TestClientMapsHTTPFailuresAndOptionalCapabilitiesFallback`）。
- `internal/qui/control/preview.go`（新增）
  - `PreviewDelete(instance, automation, limit, offset, view)`
  - `DryRun(instance, automation)`：强制 `DryRun=true`、`Enabled=true`，清掉 `ID`（不会引用已保存的规则）；响应里只要出现非 dry-run 的结果就报错。
  - `Activity(instance, limit)`：校验每条记录都有 id/action/outcome/createdAt，供 D4 按 ID 幂等导入。
  - `Activity.IsDeletion()`、`IsDryRun()`，以及 qui 的动作与结果常量。
  - 本地预校验：名称、已启用的 delete 动作、不能同时有 category、view、分页参数；不满足时不会发出请求。
- `internal/qui/control/testdata/qui-bfe94b5e/`：按源码结构构造的响应 fixture，README 写明每个文件对应的源码位置。
- `tools/quprobe`：新增可选开关。
  - `VERTEX_PRO_QUI_PROBE_AUTOMATIONS=1`：读取 activity，并对一个“随机标签、必然零命中”的删除规则做 preview；命中数不为 0 时直接失败。
  - 再加 `VERTEX_PRO_QUI_PROBE_DRYRUN=1` 才会执行 dry-run，报告里的 `safety.activity_log_written` 会如实标为 `true`。
  - 探针从不保存 automation，也不调用 apply。

## 测试

- `internal/qui/control`：5 个新契约测试，覆盖方法与路径、请求头、请求体字段、fixture 解码（包括 qui 新增的字段）、dry-run 强制开启且拒绝非 dry-run 结果、400 文案不进 `Error()`、非法请求不会发出、任何测试都没有触达 `/apply`。原有 6 个测试照常通过。
- `tools/quprobe`：2 个新测试。默认只调用 activity 和 preview；dry-run 只在开关打开时调用；探测规则意外命中时失败，并且不会继续执行 dry-run。

## 上线前需要你在 lab 环境做的验证

```
VERTEX_PRO_QUI_PROBE_AUTOMATIONS=1 go run ./tools/quprobe
```

这一步完全只读：会列出 activity，preview 结果应为 0 命中。确认无误后可以再运行一次：

```
VERTEX_PRO_QUI_PROBE_AUTOMATIONS=1 VERTEX_PRO_QUI_PROBE_DRYRUN=1 go run ./tools/quprobe
```

这一步不会改动任何种子，但会在 qui 的 activity 日志里留下一条 dry-run 记录。

本机没有 qui，所以本次的验证只到“基于源码的契约”这一层。
