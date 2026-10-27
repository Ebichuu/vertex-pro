# 2026-10-26 RSS 存储层：Discover 批量化与 last_matched 索引

## 问题

- `rsssourcedb.Store.Finish` 每处理完一个条目，都会对该 feed 的**全部** consumer 执行一次 `count(*)`，条件是 `julianday(discovered_at) >= julianday(last_polled_at)`。这个条件用不上索引，而 `rss_source_consumers` 从不清理，所以单个条目的收尾成本随 feed 的历史条目数线性增长。`app.recordFeedPoll` 每次轮询也会做同样的全量计数。
- `Discover` 对每个条目分别执行 SELECT、INSERT/UPDATE、INSERT consumer，100 个条目约 300 条语句。

## 改动

| 位置 | 改动 |
|---|---|
| `migrations/0025_consumer_match_index.sql` | 新增部分索引 `ix_consumer_matched_discovered ON rss_source_consumers(feed_id, discovered_at) WHERE matched=1` |
| `rsssourcedb.RefreshLastMatched` | 先读 `last_polled_at`（RFC3339Nano），转成 consumer 使用的定宽时间格式，再按 `discovered_at >= ?` 计数，走上面的部分索引，只扫描“上次轮询以来已匹配”的行。`last_polled_at` 无法解析时退回原查询 |
| `Store.Finish`、`app.recordFeedPoll` | 都改为调用 `RefreshLastMatched` |
| `Store.Discover` | 先去掉同批重复，然后：每 100 个 key 一次 `IN (...)` 查询已有来源 → 新来源多行 INSERT → 变化的来源逐行 UPDATE（少见）→ consumer 多行 `INSERT ... ON CONFLICT DO NOTHING`。仍在同一个 IMMEDIATE 事务内完成 |

**语义保持不变**
- `last_matched` 仍是“上次轮询以来发现、且已匹配的 consumer 数”。
- `TestRefreshLastMatchedMatchesFormerQuery` 在同一批数据上对比了新旧查询，结果一致：边界时刻计入，`discovered_at=''` 的旧行不计入。
- 唯一差别是比较精度：旧条件经 `julianday` 只有毫秒级精度，新条件按纳秒定宽字符串比较，更精确。
- `Discover` 的 New/Updated/Duplicates/Invalid 结果、legacy 来源生成 SKIPPED consumer、baseline 行为都不变；`TestDiscoverBatchesAcrossChunks` 覆盖了 250 条跨 3 个分块的情况。

## 基准（同机 2 核，`-count 6` 中位数）

```
go test -run '^$' -bench . -count 6 -benchmem ./internal/rsssourcedb/
```

| 基准 | 改动前 | 改动后 | 倍数 | 分配次数 |
|---|---|---|---|---|
| ClaimFinish，历史 0 条 | 1.74 ms | 0.94 ms | 1.8× | 165 → 192 |
| ClaimFinish，历史 1 万条 | 9.12 ms | 0.96 ms | 9.5× | 647 → 191 |
| ClaimFinish，历史 10 万条 | 82.2 ms | 0.95 ms | 86× | 4930 → 191 |
| Discover，100 条全部已知（常态） | 9.96 ms | 3.09 ms | 3.2× | 5543 → 3201 |
| Discover，95 已知 + 5 新 | 10.7 ms | 3.88 ms | 2.8× | 5700 → 3305 |
| Discover，100 条全新 | 17.1 ms | 8.16 ms | 2.1× | 8214 → 4332 |

Claim+Finish 不再随历史条目数增长。

## 未做：批量 Claim

现在 Claim+Finish 合计不到 1 ms/条，而每个条目还要下载 .torrent（几十到几百 ms）并做规则匹配，批量领取能省下的时间不到单条耗时的 1%。

另一方面，准备 worker 是“领一条、处理一条”，并按站点/host 做并发限额。一次领取多条会让同批靠后的条目在 2 分钟租约到期后被其他 worker 重新领走，导致重复下载（虽然 `claim_generation` 能保证结果只被提交一次），还会绕过按 host 的限额。收益小、风险实在，所以不做。

## 剩余项

- 常态轮询（100 条全部已知）仍约 3 ms，主要花在每条 `json.Marshal(source)` 和 consumer `ON CONFLICT DO NOTHING` 上。可以在内存中记住每个 feed 上次的条目集合（或首条 GUID），条目没变化时整次跳过写库；配合现有的 ETag/304，可以进一步减少空轮询。
- `rss_source_consumers` 没有保留期清理，表会无限增长。查询已不再受它影响，但磁盘占用仍会增加；可以给 retention 加上规则，按 `discovered_at` 清理已结束（DONE/SKIPPED/FAILED）的旧行。
