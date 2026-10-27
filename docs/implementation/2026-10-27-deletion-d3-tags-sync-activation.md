# 2026-10-27 删种 D3：归属标签、策略持久化、同步 qui、预览后手动启用

对应方案 `docs/plans/2026-10-26-deletion-engine.md` 的 D3。

## 1. 投递时打归属标签

- 位置：`internal/dispatchstage/stage.go`，紧跟在 `actionParams` 之后。添加参数在这里冻结进 `dispatch_queue`，所以直接投递和排队投递两条路径都会带上标签。
- `deletionpolicy.DeliveryTags(tags, site)`：在规则自带标签之后追加 `vertex-managed`，站点 ID 合法时再追加 `vertex-site-<站点>`（站点取自 `race_jobs.source_id` 的 `站点/条目` 前缀）。
- 站点 ID 不合法时**不打站点标签**：安全外壳要求必须有站点归属，所以这个种子永远不会被策略删除，而不是被错误归属。
- 已有测试 `TestAutomationWorkersRouteTwoInstancesAndSurviveRestart` 原先断言标签恰好等于 `vertex-fixture`，已改为精确的 `vertex-fixture,vertex-managed,vertex-site-s`。

## 2. 已有种子补打标签

- `qui/proxy.AddTags(hashes, tags)`：Vertex 除 Add 以外**唯一**的 qB 写操作。
  - 只接受 `vertex-` 开头的标签，hash 必须是 40 或 64 位小写十六进制，每次最多 200 个；不合法的请求不会发出。
  - 只能添加标签，不能删除；不会改动用户自己的标签。
- **路由指纹没有改。** `proxy.RouteFingerprint()` 绑定了 Profile 验证活动；改动它会让投递在重新验证前一直关闭。addTags 不涉及验证所覆盖的 Add 协议，所以单独写成 `tagRouteContract` 常量记录。**如果你认为白名单本身就属于“已验证的范围”，需要改指纹并重新验证，请告诉我。**
- `deletionsync.Backfill`：
  - 只对 `attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED')` 的种子补标签；数据库约束保证这两个状态的 `side_effect_fact` 是 `OBSERVED_EFFECT`，并且带有 qB torrent id，也就是 Vertex 亲眼确认是自己加进去的种子。
  - **排除 `ALREADY_PRESENT`**：这类种子在 Vertex 之前就存在，可能是用户自己的。
  - 只处理仍在当前快照中、且确实缺少标签的种子。
  - `Plan` 只读；`Apply` 会重新计划后，按相同的标签组合分批写入。添加标签是幂等的，失败了重跑即可。
- API：`GET /api/v1/deletion/tag-backfill/{instance}` 只列出计划；`POST` 才执行。

## 3. 策略持久化（迁移 `0026_deletion_policies.sql`）

| 表 | 作用 |
|---|---|
| `deletion_policies` | 策略主体（`spec_json`）、作用实例、修订号、`live`、`archived`；约束保证 `archived=1` 时 `live=0` |
| `deletion_safety` | 单行全局安全设置，带修订号 |
| `deletion_previews` | 预览证据：(策略, 策略修订号, 安全设置修订号, 实例) → 命中数、预览时间 |
| `deletion_policy_bindings` | Vertex 在每个实例上拥有的 qui automation：qui ID、写入时的内容、是否生效、最近一次错误码 |

规则（全部由 `deletionsync.Store` 保证）：
- 新建策略的 `live` 一律为 0。保存前会用当前安全设置试编译，编译不过就拒绝保存。
- **编辑**会产生新修订号并把 `live` 置 0。**归档**同样置 0，并让修订号加一。
- **修改安全设置**会把所有生效中的策略打回 dry-run（可删的范围变了，必须重新预览）；如果新设置会让某条现有策略编译失败，就拒绝修改。
- **启用**必须同时满足：修订号与当前一致；**每个**目标实例都有**这个策略修订号 + 当前安全设置修订号**的预览记录，且在 24 小时之内。

## 4. 同步到 qui（`deletionsync.Syncer`，由 `quipolicy` 的 Ensure / 回读 / 停用推广而来）

- 每个实例：把作用于它的、未归档的策略编译出来，逐个角色确保 qui 里的 automation 与编译结果一致。
- **任何保存都会回读**，回读内容不一致按错误处理。
- **失败一律关闭**：以下情况都会把 automation 改写为 `enabled=false`、`dryRun=true`，并把策略打回 dry-run、记录错误码：

  | 情况 | 错误码 |
  |---|---|
  | qui 里的副本被人改过（与绑定记录的内容不同） | `QUI_DELETION_POLICY_TAMPERED` |
  | qui 里的副本被删了 | `QUI_DELETION_POLICY_MISSING`（会重新创建为关闭状态） |
  | 存在使用 Vertex 名称、但没有绑定的 automation | `QUI_DELETION_POLICY_NAME_TAKEN`（接管并关闭） |
  | 同名 automation 不止一个 | `QUI_DELETION_POLICY_DUPLICATE_NAME` |
  | 保存后回读不一致 | `QUI_DELETION_POLICY_READBACK_MISMATCH`（先记下“本应写入”的内容；下一轮发现不一致即按被改过处理并关闭） |
  | 策略已无法编译 | `DELETION_POLICY_COMPILE_FAILED` |

- 角色被移除、策略被归档、实例被移出作用范围时，对应 automation 会被**关闭但不删除**，保留历史；关闭后同样回读确认。
- 比较只看 Vertex 写入的字段（名称、tracker 模式、开关、dry-run、通知、间隔、条件、可用空间来源、排序），qui 自己管理的字段（排序位置、时间戳、规范化后的 tracker 域名）不参与。
- 后台每分钟收敛一次；**没有任何策略或绑定时完全不访问 qui**。API 修改之后会立即同步一次，同步失败不会回滚修改，而是在响应的 `sync_problems` 里列出。

## 5. API（`internal/resourcesapi/deletion.go`，已写入 `contracts/openapi/openapi.json`）

```
GET    /api/v1/deletion/policies
POST   /api/v1/deletion/policies                    新建（dry-run）
GET    /api/v1/deletion/policies/{id}
PUT    /api/v1/deletion/policies/{id}               If-Match；生成新修订号，不生效
DELETE /api/v1/deletion/policies/{id}               If-Match；归档并关闭 automation
POST   /api/v1/deletion/policies/{id}/preview       调用 qui preview 并记录证据；不删除任何种子
POST   /api/v1/deletion/policies/{id}/activate      {"revision": N}
POST   /api/v1/deletion/policies/{id}/deactivate
GET    /api/v1/deletion/safety
PUT    /api/v1/deletion/safety                      If-Match；所有策略回到 dry-run
GET    /api/v1/deletion/tag-backfill/{instance}     只列出计划
POST   /api/v1/deletion/tag-backfill/{instance}     执行补打标签
```

- handler 只调用 `deletionsync.Service`，不直接执行 SQL，也不直接访问 qui。
- 请求体严格解码，未知字段会被拒绝。
- 错误码映射：校验失败或未预览 → `VALIDATION_FAILED`；修订号冲突 → `REVISION_CONFLICT`；实例未连接 → `INSTANCE_UNAVAILABLE`；qui 返回校验错误 → `VALIDATION_FAILED`，并附上 qui 的原因（只在响应里展示，不写进错误字符串）。

## 测试

- `deletionsync`（9 个）：启用门槛（未预览、只预览了部分实例、修订号不对、预览过期、编辑后旧预览失效、改安全设置后降级、已归档）；同步的各条失败关闭路径（被改、被删、名称被占、回读不一致）；关闭而不删除；预览要求作用范围内的实例都已连接；补打标签只覆盖可证明是 Vertex 添加的种子。
- `resourcesapi`：完整的 HTTP 生命周期测试，覆盖新建、未预览拒绝启用、预览、启用、冲突、不安全的修改、正常修改、安全设置、未知字段、未连接实例、归档。
- `qui/proxy`：addTags 的请求格式、拒绝非 Vertex 标签与非法 hash（不发出请求）、错误信息不泄露代理密钥。
- `deletionpolicy`：`DeliveryTags` 不修改传入的切片，站点不合法时不打站点标签。

## 尚未完成或需要你确认

- **lab 验证**：qui 的 automation 增改与读回、`/proxy/.../torrents/addTags` 是否被 qui 代理放行，都只经过了 fake 测试。上线前请在 lab 环境里：
  1. 新建一个策略，确认 qui 里出现一个关闭且 dry-run 的 `vertex-del-*`；
  2. 运行补打标签的计划和执行；
  3. 预览一次。
- **路由指纹**：见第 2 节，是否需要改动请你决定。
- **旧的规则级做种时长删除**（`quipolicy`，以 `vertex-policy-*` 标签为范围）仍然原样运行，**没有安全外壳**。方案里计划在规则下次编辑时迁移到新策略，D3 没有做；建议作为单独的一步。
- 前端（D6）和删种留痕导入（D4）不在本次范围内。
