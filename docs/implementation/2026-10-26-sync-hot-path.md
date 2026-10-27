# 2026-10-26 同步热路径与 maindata 上限

## 问题

- `qui/proxy` 对所有 GET 响应统一使用 1 MiB 上限。全量 `sync/maindata` 每个种子约 1.5 KiB，**1000 个种子时全量同步即失败**（`qB proxy response exceeds limit`），实例无法进入 READY。
- 每个实例每个同步周期（默认 10 s）：
  - `syncmerge.Merge` 深拷贝全部种子的 `Fields`；
  - `proxy.Fetch` 先解码成 map，再 Marshal、Unmarshal 一遍；
  - `snapshot.Store.Save` 对整个快照做 `json.Marshal` + sha256，并把文件 fsync 写盘；
  - 开启投递时，`LoadLatest` 再把文件读回、校验、解码；
  - 有待确认链时，`confirmcoordinator.SnapshotDigest` 再次 Marshal 全量快照。

## 改动

| 位置 | 改动 |
|---|---|
| `qui/proxy` | `Fetch` 流式解码一次，同时计算响应体 sha256（`Delta.BodyDigest`）；maindata 上限 512 MiB，超限报错而不是截断 |
| `syncmerge` | 写时复制：只复制种子索引，变化的种子才新建 `Fields`；`Ignored` 不再克隆。快照发布后视为不可变 |
| `snapshot` | 新增 `Commit`（只写 `sync_snapshots` 行 + `sync_instance_state`，`snapshot_path=''`）和 `Coordinates`；`Load` 读到内存态修订返回 `ErrNotPersisted` |
| `syncwatcher` | 启动时只读取已提交的坐标，然后从 rid=0 重新建立基线（生成新 generation）。digest 改为链式摘要 `sha256(坐标, rid, 上一 digest, 响应体摘要)`。新增 `Current()` |
| `confirmcoordinator` | `Event.Digest` 非空时直接使用，不再全量重编码 |
| `app` / `resourcesapi` | 投递证据与异常证据改从 watcher 内存快照读取，并仍与 `sync_instance_state` 的坐标和 digest 比对 |
| `retention` | 跳过空路径（内存态修订没有文件） |

**不变量**
- 每个修订仍会写 `sync_snapshots` 行，所以 `sync_waiters` 外键、历史观察、确认链的坐标与 digest 校验都不受影响。
- 旧 digest 里包含 RID，每个修订本来就唯一；链式 digest 保持了这个性质。
- qB 才是事实来源：重启后用一次全量同步重建快照，不依赖本地快照文件。旧的快照文件和行仍由 retention 按原规则清理。

## 基准测试（同一台 2 核 Windows Server 2022、Go 1.26.6，`-count 6` 取中位数）

```
go test -run '^$' -bench . -count 6 -benchmem ./internal/syncmerge/ ./internal/qui/proxy/ ./internal/syncwatcher/ ./internal/confirmcoordinator/
```

| 基准 | 改动前 | 改动后 | 倍数 | 分配 前 → 后 |
|---|---|---|---|---|
| RunOnceIncremental 1k（一次完整同步周期） | 62.8 ms | 0.92 ms | 68× | 8.5 MB → 0.4 MB |
| RunOnceIncremental 10k | 513.6 ms | 6.24 ms | 82× | 78.3 MB → 3.7 MB |
| MergeIncremental 10k | 74.1 ms | 5.5 ms | 13.5× | 24 MB → 3.5 MB |
| MergeIncremental 100k | 904 ms | 61.5 ms | 14.7× | 238 MB → 33 MB |
| MergeFull 10k | 131 ms | 71 ms | 1.8× | 45 MB → 24 MB |
| FetchIncrementalMaindata 10k | 10.4 ms | 5.1 ms | 2.0× | 1.5 MB → 0.7 MB |
| FetchFullMaindata 1k / 10k | **失败（>1 MiB）** | 60 ms / 584 ms | — | — |
| SnapshotDigest 10k（已移出同步周期） | 410 ms | 不在热路径 | — | — |

RunOnceIncremental 的增量为 5% 种子各变化 8 个字段（`syncfixture.IncrementalDelta`）。

## 第二轮：有类型的 Torrent

### 改动

- `syncmerge.Torrent` 从 `Fields map[string]any` 改为有类型的结构，共 27 个 qB 字段：name/state/category/tags/tracker/save_path、各类字节计数、速度与限速、做种/下载人数、时间戳、progress/ratio、amount_left。
  - `Known` 位掩码记录 qB 报告过、且值有效的字段，`Has(field)` 判断是否“已报告”。
  - `Value(name)` 按 qB 原字段名取值，供实验室探针工具使用。
  - 其余 maindata 字段在解码阶段直接跳过，不再分配内存。
- `syncmerge.Delta.Torrents` 改为 `map[string]TorrentDelta`。字段用指针表示“是否出现”，数字仍按 float64 解码，与原先 `map[string]any` 的数值语义一致。
- `amount_left` 保留原始 JSON：显式 null、负数、非整数、非数字字符串都视为 UNKNOWN，数字字符串照旧解析。
- `Snapshot.Torrents` 改为 `map[string]*Torrent`。Torrent 超过 128 字节，Go map 会把这类值单独放在堆上，按值存时每个条目都多一次分配，写时复制克隆的成本也随条目数上升；改成指针后，克隆只复制 8 字节指针。
- 读取方随之迁移：`app` 的监控与历史观察、`dispatchstage`（对缺失种子加了 nil 防护）、`confirmcoordinator` 测试，以及 `qutransactionprobe`、`quaddprobe`、`qupresentprobe` 三个探针。

**语义变化：**
- 我们读取的字段如果在 maindata 里类型不符（例如数字字段收到字符串），整次同步会失败（实例进入 STALE），不再像以前那样把错误值静默存进 map。qB 4.3–5.2 这些字段的类型是固定的；真出现类型不符，说明 qB/qui 版本不兼容，应当暴露出来。
- 旧版本写下的快照文件（`snapshot.Store.Save`）里是 `Fields`，用新结构解码会丢失字段。运行时已经不读这些文件（见第一轮）。

### 基准（与第一轮改动后的版本对比，同机，`-count 6` 中位数）

| 基准 | 第一轮后 | 有类型 | 倍数 | 分配 前 → 后 |
|---|---|---|---|---|
| 常驻内存（每种子，10k / 100k） | 2982 / 2958 B | 580 / 571 B | 5.2× | 10 万种子约 296 MB → 57 MB |
| RunOnceIncremental 1k（一次同步周期） | 0.80 ms | 0.29 ms | 2.8× | 399 KB → 74 KB |
| RunOnceIncremental 10k | 5.16 ms | 1.03 ms | 5.0× | 3.66 MB → 0.60 MB |
| FetchFullMaindata 10k（启动全量） | 595 ms | 330 ms | 1.8× | 102 MB → 40 MB，分配次数 1.92M → 0.36M |
| FetchIncrementalMaindata 10k | 5.5 ms | 4.2 ms | 1.3× | 0.69 MB → 0.51 MB |
| MergeFull 100k | 707 ms | 126 ms | 5.6× | 238 MB → 35 MB |
| MergeIncremental 100k | 73 ms | 6.4 ms | 11.5× | 32.7 MB → 4.9 MB |

与最初版本相比，10k 种子一次同步周期从 513.6 ms 降到 1.03 ms（约 500×）。

**测量说明：**
- `BenchmarkSnapshotRetainedHeap` 前后各做两次 GC，以清掉 `encoding/json` 在 `sync.Pool` 里缓存的缓冲区；只做一次 GC 时，10 万种子会被错误地测成 3255 B。
- `BenchmarkRunOnceIncremental` 的增量数据改为预先生成，避免把构造测试数据的开销算进同步周期。这两项都在两个版本上用同一份基准代码重新测过。

## 剩余项

- 全量 maindata 解码仍用 `encoding/json` 反射（10k 种子约 330 ms），只在启动或 qB 重置时发生。在数十万种子的规模下，可以考虑换成手写或代码生成的解码器。
- `proxy` 默认 `http.Client` 超时 10 s，十万级种子的全量响应可能需要单独放宽。
- `TestPipelineBurst30WithSlowPreparationAndSlowInstance` 的 p95 断言在 2 核机器上跑全量 `go test ./...` 时会偶发失败（改动前后都会）；单独运行时改动前后均 3/3 通过。
