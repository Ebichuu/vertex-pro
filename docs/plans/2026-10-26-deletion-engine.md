# 删种与保种系统设计（P1-3）

状态：**方案已确认**（编译成 qui automation；目标 qui `bfe94b5e`）。D1 已完成，见 `docs/implementation/2026-10-26-deletion-d1-qui-preview-activity.md`。

## 一、现状

- 删种唯一的实现是 `internal/quipolicy`：每条带做种时长的投递规则，生成一个 qui automation，条件固定为 `TAGS = vertex-policy-<hash> AND SEEDING_TIME > N`。
- 已有的安全机制：创建或修改后回读核对、DB 绑定、外部改动检测、规则停用时自动停用 automation。这部分做得是对的。
- 缺少：分享率、磁盘水位、做种人数等维度；白名单与计时保种；H&R 底线；删前预览；删种留痕。
- qBittorrent proxy 白名单里只有读接口和 `torrents/add`，Vertex 自身不能删除种子。

## 二、关键发现：qui 已经是完整的删种引擎

依据 qui 源码（`autobrr/qui`，提交 `bfe94b5e`，即 `internal/qui/control` 当前对接的版本）：

| 需求 | qui 提供的能力 |
|---|---|
| 多维度条件 | `SEEDING_TIME`、`RATIO`、`UPLOADED`、`UPLOADED_OVER_SIZE`、`UP_SPEED_AVG`、`NUM_SEEDS`、`NUM_LEECHS`、`SIZE`、`ADDED_ON_AGE`、`LAST_ACTIVITY_AGE`、`COMPLETION_ON_AGE`、`PROGRESS`、`STATE`、`TRACKER`、`CATEGORY`、`TAGS`、`HASH`、`IS_UNREGISTERED`、`HAS_MISSING_FILES`、`SEEDING_ON_OTHER_INSTANCE` 等，支持 AND/OR 嵌套 |
| 磁盘水位清理 | `FREE_SPACE` 条件：按顺序删除，累计“删除后可腾出的空间”，达标即停（`updateCumulativeFreeSpaceCleared`）；空间来源可以是 qB 上报值，也可以是本地路径 |
| 低价值优先 | `SortingConfig`：按单一字段排序，或用多条规则加权打分（字段乘以权重，以及“满足条件则加分”） |
| 辅种安全 | 删除模式 `deleteWithFilesPreserveCrossSeeds`、`deleteWithFilesIncludeCrossSeeds`，以及 `HARDLINK_SCOPE` |
| 预览 / dry-run | `PreviewDeleteRule`（列出会被删除的种子）、`DryRunNow`，以及 automation 的 `dryRun` 开关 |
| 留痕 | `ListActivity` / `GetActivityRun`：按种子记录 hash、名称、站点、动作、规则、结果、原因 |

**结论：** 不在 Vertex 内另写删种引擎，也不把 `torrents/delete` 加进 proxy 白名单。Vertex 负责把“刷流删种策略”编译成 qui automation，并补上 qui 没有的刷流语义，也就是下一节的安全外壳。这样仍完全遵守“只经由 qui”的架构红线，也不重复造一个已经成熟的引擎。

## 三、Vertex 增加的内容

### 1. 删种策略模型（Vertex 自己的 schema，不直接暴露 qui 的 JSON）

```
DeletionPolicy {
  id, name, enabled, revision
  scope:       实例 / 实例组 / 站点
  triggers:    满足任一即可删除（OR），每项是若干条件的 AND
               例如 [{seed_time >= 72h}, {ratio >= 3}, {seed_time >= 24h AND up_speed_avg < 50KB/s}]
  watermark:   可选，{min_free_bytes, source: qb|path, order: 打分规则}
  delete_mode: keep_files / with_files / with_files_preserve_cross_seeds（默认）
}
```

### 2. 安全外壳：由编译器强制附加，用户无法删除或绕过

每条生成的 qui automation 条件都是 `外壳 AND (策略条件)`：

1. **范围**：只处理带 `vertex-managed` 标签的种子，即 Vertex 自己投递的种子；用户手动添加的种子默认永不触碰。
2. **白名单**：
   - 不带 `vertex-keep` 标签（种子级永久保种）；
   - 分类不在保种分类列表中；
   - 站点不在保种站点列表中。
3. **H&R 底线**：站点配置了 H&R 要求时，附加 `SEEDING_TIME >= H&R 时长 OR RATIO >= H&R 分享率`。
4. **未完成的种子**：默认要求 `PROGRESS = 1`。只有策略显式启用“清理卡住的下载”时，才放开为“`PROGRESS < 1 AND LAST_ACTIVITY_AGE > N`”。
5. **新策略一律先进入 dry-run**：必须看过 qui 的预览列表并手动“启用”，才会真正删除。修改策略后重新进入 dry-run。

### 3. 计时保种

- `vertex-keep-until` 标签不适合，因为 qui 无法解析标签里的日期。
- 改为：计时保种记录存在 Vertex DB（hash, until）；Vertex 通过 qui proxy 给种子加或移除 `vertex-keep` 标签，到期自动移除。
- 需要把 `torrents/addTags`、`torrents/removeTags` 加入 proxy 白名单。这仍然是经由 qui 的写操作，但比 `torrents/delete` 的风险小得多。

### 4. 留痕与通知

- 后台定期拉取 qui 的 `ListActivity`，写入 Vertex 的 `deletion_audit` 表（instance, hash, name, site, policy, revision, action, outcome, reason, at），并关联到投递历史（race job）。
- 删种事件进入现有的 event stream，后续接 Telegram 通知时直接复用。

### 5. 与现有 quipolicy 的关系

- 现有“规则级做种时长”的 automation 继续生效，绑定数据不迁移，以免改动线上行为。
- 新的策略系统上线后，规则编辑器的“做种时长”改为引用一个删种策略。旧绑定在规则下一次编辑时迁移过去。

## 四、实施步骤（每步可单独验证、可回滚）

| 步 | 内容 | 验证 |
|---|---|---|
| D1 | `qui/control` 补上 preview / dry-run / activity 接口 | 基于 qui 源码构造响应 fixture 的契约测试；`tools/quprobe` 只读调用真实 qui |
| D2 | 策略模型 + 编译器（纯函数：策略 → qui Automation JSON），由编译器强制附加安全外壳 | golden JSON 测试；属性测试：任意策略编译后都包含完整外壳 |
| D3 | 同步服务：把 quipolicy 的 Ensure/回读/停用推广到策略；新策略默认 dry-run；API（增删改查、预览） | 用模拟 qui 做集成测试，覆盖外部篡改、回读不一致、停用 |
| D4 | 留痕：拉取 activity → `deletion_audit` | 幂等测试：重复拉取不产生重复记录 |
| D5 | 保种标签与计时保种（proxy 白名单加 addTags / removeTags） | 到期移除测试；白名单变更的审计 |
| D6 | 前端：策略列表与编辑、预览、审计日志 | vitest + 构建，刷新嵌入资源 |

每一步都不会自动删除任何种子：只有用户在预览后手动启用策略，才会产生真正的删除。

## 五、需要确认或你提供的信息

1. **方案**：是否同意“编译成 qui automation”，而不是 Vertex 自己实现删除？
2. **qui 版本**：仓库里 `UPSTREAM_LOCK.yaml` 锁定的是 `8fcfc4d2`，`control/automations.go` 注释写的是 `bfe94b5e`。你实际部署的是哪个版本？`FREE_SPACE` 打分、preview 等接口需要在那个版本上核对。
3. **真实环境**：我这里没有 qui 和 qB，D1 只能做到“基于源码的契约测试”。上线前需要你在 lab 环境运行一次只读探针，加上一次 dry-run 预览。
