# 2026-10-26 规则字段补全与规则编译缓存

## 问题

- 规则字段目录里的 `category`、`publisher`、`group`、`discovery_delay`、`promotion`、`tags`、`qb_profile`、`instance_labels`、`storage_labels`，在生产匹配中**从未被赋值**：`matchpipeline.buildFacts` 只填了 site/rss/title/size/published_at。用到这些字段的条件恒为 UNKNOWN，规则静默地永远不命中。
- 前端 `web/src/features/rules/model.ts` 把 `promotion` 列为“简单字段”，用户很容易建出这种死规则；保存时后端也不检查条件树。
- 每个条目在 `Prefilter` 和 `Process` 中各做一遍：从 DB 读规则 → JSON 解析条件树 → 校验字段目录（两次）→ 每次求值都重新 `regexp.Compile`。

## 改动

### 字段补全（数据来自 RSS）

| 字段 | 来源 |
|---|---|
| `category` | RSS `<category>` 中第一个非空值；Atom `<category label|term>` |
| `publisher` | `dc:creator`，没有则用 `<author>`；Atom `<author><name>` |
| `group` | 从标题提取（`matchpipeline.ReleaseGroup`）：后缀 `-GROUP` / `-GROUP@SITE`，或番剧前缀 `[Group]`；先去掉结尾的 `[副标题]`、`【】`、`()`。`WEB-DL`、`BD-Rip` 这类格式词不算组名。无法可靠识别时字段缺省（UNKNOWN），不做猜测 |
| `discovery_delay` | consumer 的 `discovered_at` 减发布时间，单位秒，向下取整，时钟偏差时取 0；旧行或缺少发布时间时缺省 |

- `rsssource.Source` 新增 `Category`、`Publisher`，随 consumer 的 `source_json` 持久化。它们**不参与**去重 key 和 fingerprint，所以升级后已知条目不会被批量判成“已更新”。
- `rsssource.Consumer` 新增 `DiscoveredAt`，由 `Claim` 读出。
- 这四个字段都只依赖 RSS，下载 .torrent 后不会变化，所以也加入了 Prefilter 的稳定字段集。效果：`category = Movies` 这类规则现在能在**下载 .torrent 之前**就排除不相关条目。

### 没有数据来源的字段：保存时拒绝

- `rules.FieldSpec` 新增 `Live`，标记生产匹配是否提供该事实。`promotion`、`tags` 需要站点驱动抓取种子页；`qb_profile`、`instance_labels`、`storage_labels` 描述的是投递目标，不是种子本身。这五个标为非 Live。
- `ruleservice` 创建或更新规则时，如果条件树引用了非 Live 字段，返回 `invalid rule condition: fields have no live data source yet and would never match: …`，API 层映射为 `VALIDATION_FAILED`。
- dry-run（调用方自带事实）仍可使用全部字段，目录本身没有变。
- 已存在的旧规则不会被改动；它们在修改并保存时才会被要求去掉这些字段。

### 规则编译缓存

- `rules.Compile`：只校验一次字段目录，并预编译正则。`(*Compiled).Evaluate` / `EvaluatePartial` 的结果和原因码与 `Evaluate` / `EvaluatePartial` 完全一致（`TestCompiledMatchesEvaluate` 逐项比对，包括非法正则返回 `INVALID_REGEX`）。
- `ruleservice.ListCompiled`：每次仍从 DB 读取规则列表，所以启用、停用、归档、优先级的变更立即生效。编译结果按 rule ID 缓存，**只有 revision 和 definition 字节都相同**时才复用，编辑或重建都不会命中过期缓存。缓存上限 4096 条，超过后整体重置。停用的规则不编译。
- `Prefilter` 和 `Process` 改用 `ListCompiled`。

## 基准（同机 2 核，`-count 6` 中位数）

```
go test -run '^$' -bench . -count 6 -benchmem ./internal/matchpipeline/
```

每条规则：site 等值 + 大小区间 + （标题正则 OR 标题包含）；均不命中，所以每条规则都完整求值、不写 race job。

| 基准 | 改动前 | 改动后 | 倍数 | 分配 KB / 次数 |
|---|---|---|---|---|
| Prefilter，5 条规则 | 0.54 ms | 0.35 ms | 1.5× | 101 → 24 / 852 → 208 |
| Prefilter，20 条规则 | 1.81 ms | 0.54 ms | 3.3× | 396 → 89 / 3291 → 697 |
| Prefilter，50 条规则 | 4.71 ms | 1.10 ms | 4.3× | 980 → 211 / 8157 → 1666 |
| Process，5 条规则 | 0.59 ms | 0.21 ms | 2.8× | 105 → 28 / 863 → 214 |
| Process，20 条规则 | 1.89 ms | 0.53 ms | 3.6× | 414 → 103 / 3332 → 718 |
| Process，50 条规则 | 4.43 ms | 1.20 ms | 3.7× | 1023 → 246 / 8258 → 1718 |

剩余耗时主要是每次从 DB 读规则列表（为保证变更即时生效而保留）。

## 剩余项

- **前端**：`model.ts` 仍把 `promotion` 列为简单字段，`tags`、`qb_profile` 等也照常可选，保存时会被后端拒绝。应在规则编辑器里隐藏这些字段或标为“暂无数据来源”，仅在 dry-run 中保留。本机没有 Node 环境，这一处未改动、未验证。
- **`promotion`（免费/2x/H&R）** 需要站点驱动读取种子详情页或站点 API，属于差距清单中的 P1“站点驱动化”，不在本次范围内。
- **`group` 的提取是启发式的**：只覆盖常见的 scene/P2P 后缀与番剧前缀。识别不了时字段缺省，规则保持 UNKNOWN（fail-closed），不会误命中。
