# 2026-10-27 删种 D2：策略模型与编译器

对应方案 `docs/plans/2026-10-26-deletion-engine.md` 的 D2。新增包 `internal/deletionpolicy`：纯函数，不涉及 HTTP 或 DB，把 Vertex 删种策略编译成 qui `bfe94b5e` 的 automation。包里只用到 `qui/control.Automation` 这个类型，它本身就是对接 qui 的适配层，不属于领域层。

## 影响设计的 qui 语义（源码依据：`internal/services/automations/evaluator.go`）

| 事实 | 后果 |
|---|---|
| `compareInt64` / `compareFloat64`：值为空字符串时**按 0 比较**（只有非空且解析失败才返回 false） | `SEEDING_TIME GREATER_THAN ""` 会变成“> 0”，几乎全部命中。编译器绝不输出空的数值，并由属性测试逐个叶子检查 |
| `NOT_EQUAL` 和 `Negate` 在“事实缺失”时为真 | 否定式的保护条件会在数据缺失时放行 |
| qB 的 `tracker` 字段在 tracker 失效时为空（未注册的种子正是这种情况）；`TRACKERS` 只在规则用到 tracker 状态或消息时才会填充 | **任何基于 tracker 的否定保护都会在出问题时失效**。因此安全外壳里与站点相关的部分全部基于 Vertex 自己控制的标签 |
| `TAGS` 按集合比较且不区分大小写；`STARTS_WITH` 对每个标签分别判断 | 可以用来判断“必须带有某个站点归属标签” |
| `FREE_SPACE` 比较的是“当前可用空间 + 本轮已计划腾出的空间” | 水位条件写成 `FREE_SPACE LESS_THAN 目标字节数`，qui 按排序依次删除，达标即停 |

## 策略模型

- `Policy`：ID、名称、修订号、`Live`、`Triggers`、`Watermark`、`Stalled`、`DeleteMode`、`ConfirmFor`、`Interval`。
- **Trigger**：内部条件全部满足才删除（AND），多个 trigger 之间是 OR。每个 trigger **必须包含一个“持有下限”**：做种时长或添加时长 ≥ 10 分钟、分享率大于 0，或种子已未注册。否则像“没有下载者就删”这样的条件会在种子刚完成时就把它删掉。
- 可用指标：做种时长、添加时长、最近活动时长、分享率、上传量/体积（不受辅种影响的分享率）、上传量、平均上传速度、当前上传速度、连接的下载者数、swarm 下载者数、连接的做种者数、swarm 做种者数、体积、未注册。
- **Watermark**：可用空间目标（qB 上报值或指定路径）、可选的资格条件、打分规则（字段 × 权重，以及站点加分，分数越高越先删）。
- **Stalled**：清理卡住的未完成下载，添加时长和最近活动时长都必须 ≥ 30 分钟；**H&R 站点一律排除**。
- **DeleteMode**：仅保留种子、连同文件删除、连同文件删除但保留辅种。**不提供 qui 的“连同辅种一起删”模式**，因为它会删掉安全外壳之外的种子。

## 编译结果

- 每个角色编译成一个 qui automation，名称固定为 `vertex-del-<策略ID>-<triggers|watermark|stalled>`，后续修订更新同一个 automation。
- `Live=false` 时编译为 `enabled=false` 且 `dryRun=true`，双重保险：即使有人在 qui 里手动启用，它也只会 dry-run。
- 删除条件 = `AND(安全外壳…, 策略条件)`。

### 安全外壳（编译器强制附加，按顺序）

1. `TAGS EQUAL vertex-managed`：只处理 Vertex 投递的种子。
2. `TAGS STARTS_WITH vertex-site-`：必须能确定站点归属，否则不删（H&R 底线无从判断时宁可不删）。
3. `TAGS NOT_EQUAL vertex-keep`：保种标签。
4. 每个保护分类：`CATEGORY NOT_EQUAL x`。
5. 每个保护站点：`TAGS NOT_EQUAL vertex-site-x`。
6. 完成度：triggers 和 watermark 要求 `AMOUNT_LEFT EQUAL 0`；stalled 要求 `AMOUNT_LEFT GREATER_THAN 0`，并排除所有 H&R 站点。
7. 每个 H&R 站点（仅 triggers 和 watermark）：`OR(不是该站点, SEEDING_TIME ≥ 要求时长, RATIO ≥ 要求分享率)`。

## 测试

- **golden**（`testdata/golden/*.json`，用 `go test -run TestCompileGolden -update` 重新生成）：三种代表性策略编译出的完整 qui JSON。
- **属性测试**（3000 个随机策略和安全配置，固定随机种子；其中约 1/3 被拒绝，生成器覆盖面另有断言）：
  - 每个 automation 的根条件都是 AND，且开头逐项等于安全外壳；
  - 没有空分组、没有正则；每个叶子都有字段和非空值，数值字段的值都能被解析；
  - 删除模式永远不是“连同辅种一起删”；`Live` 与 enabled、dryRun 的关系始终一致；
  - 发给 qui 的 map 与类型化的删除动作完全一致。
  - **语义检查**：测试里有一个按 qui 比较规则实现的小求值器，包括“空数值按 0 比较”的特性。每个 automation 各跑 40 个随机种子，凡是会被删除的种子，都必须满足用普通 Go 代码独立写出的安全外壳规则。
- **变异检查**（手工执行后已还原）：分别删掉保种标签条件、管理范围条件、完成度条件，或把 H&R 底线里的站点判断换掉，属性测试都会失败并给出对应原因。其中 H&R 那一项只有语义检查能发现，因为结构检查拿来比对的外壳本身也被改了。
- **校验拒绝**：22 种不安全或格式错误的策略，以及 5 种不合法的安全配置，都会返回 `ErrInvalid`。

## 已知的保守取舍与 D3 待办

- H&R 站点上未达底线的**未注册**种子不会被删除（外壳优先于未注册 trigger）。
- 从未有过传输活动（`last_activity` 为 0）的卡住下载，qui 的 `LAST_ACTIVITY_AGE` 判断不会命中，因此不会被 stalled 清理。
- **D3 必须做的事：** 投递时由 Vertex 打上 `vertex-managed` 和 `vertex-site-<站点>` 标签（目前没有打），并为已有种子补打标签。在此之前，所有策略都不会命中任何种子，这是安全的默认状态。
