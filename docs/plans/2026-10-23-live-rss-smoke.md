# 一个站点、两个下载器的真实 RSS 冒烟验证

日期：2026-10-23（Asia/Hong_Kong）。用户指定现有织梦 RSS 和两个 qBittorrent，并要求每个下载器各添加一次。

本轮实际执行的是人工读取 RSS、经现有 qui 界面投递到 qBittorrent 的链路。没有将它计为 Vertex Pro Scheduler/DispatchStage 自动调度验收，也没有修改正式 Gate 状态。

## 测试对象

- 站点：zmpt.cc。使用用户提供的临时 RSS；完整 RSS 地址、下载令牌、登录凭据不写入此记录。
- qui 实例 2：01-hostdzire NL，对应用户提供的 89.149.243.60:9090。
- qui 实例 7：05-保种 ks-b GRA，对应用户提供的 51.38.42.186:9090。
- 两个实例均通过 qui 操作，没有从 Vertex Pro 直连 qBittorrent。
- 添加时启用“暂停启动”，关闭自动种子管理，保存路径为 `/home/ebichu/qbittorrent/Downloads/vertex-pro-test`。

## 种子身份

- RSS 标题：`Su Dong Po Yu Hang Zhou De Gu Shi 2026 S01E38 2160p WEB-DL H265 AAC-UBWEB`。
- 实际 v1 infohash：`af85fb12c51c06a7f5358af9293a70bfd95e9566`。
- 实际内容大小：341,713,663 字节，325.88 MiB；单个 S01E38 MKV 文件。
- torrent 顶层目录名含 `S01.Complete`，但元数据内只有第 38 集一个文件，大小与 RSS enclosure 一致。
- 旧下载链接后来返回 HTTP 200、30 字节 HTML 错误 `invalid downhash, decrpyt fail`，不是 torrent。刷新 RSS 后重新获得有效的 934 字节 torrent 文件并解析确认身份；为定位已滑出最新列表的同一条目，临时扩大读取条数，没有创建持续 RSS 订阅。

## 结果

| 实例 | 本轮累计添加提交 | 可核实结果 | 最终判断 |
| --- | ---: | --- | --- |
| 2：01-hostdzire NL | 1 次 | 添加后短暂出现“已停止”；qui 工作流于 00:51 记录相同种子名和哈希前缀 af85fb1 被“除HH删种 (copy)”移除，包含文件 | 到达下载器，但暂停任务保留失败 |
| 7：05-保种 ks-b GRA | 1 次 | 02:15:51 添加；详情中的完整哈希、大小、保存路径匹配；已停止、0%、已下载 0 B、已上传 0 B | 暂停添加与属性核对通过 |

实例 2 的触发规则排除 tracker.hhanclub.net，条件为上传速度 <=100 KiB/s，持续时间 0 分钟，操作为“移除并清除文件”，运行间隔 1 分钟。暂停的织梦任务满足该规则。实例 7 的 qui 工作流页显示尚未配置自动化规则。

本轮没有更改既有自动化，没有恢复实际内容下载，也没有向实例 2 重复提交。实例 2 的逐任务下载量未留存可核实读数，因此不声称它的累计下载量已核对为零。

## 证据

- `artifacts/live-rss-20261023/torrent-summary.json`：脱敏的 torrent 身份、文件数和大小。
- `artifacts/live-rss-20261023/instance2-delete-rule.png`：命中的规则条件。
- `artifacts/live-rss-20261023/instance2-removed.png`：自动删除事件。
- `artifacts/live-rss-20261023/instance7-stopped.png`：暂停状态、完整哈希、路径和 0 B 下载量。

## 尚未通过的范围

- 两个实例同时持续保留测试任务：实例 2 被既有删种规则清除。
- 实际文件下载、完成、做种和 Tracker 通信：未运行。
- Vertex Pro 内站点/规则创建、RSS 基线与新条目触发、自动选择目标、投递历史、恢复：本轮未运行。

下次若要求两端保留，需要先获得对实例 2 删种规则的具体调整授权，核查其他删除规则，并为测试路径或标签添加狭窄豁免后再明确重测。Vertex Pro 自动链路还需要完成 serve 装配、动态容量证据与有效 Profile 校验，不能用本次人工投递替代。
