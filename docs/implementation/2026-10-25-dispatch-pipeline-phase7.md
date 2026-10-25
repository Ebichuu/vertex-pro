# 第七阶段：准备许可等待归还 worker

## 问题与改动

四个 RSS 来源的八条任务都指向慢下载主机 H 时，两个请求持有 H 的 HTTP 槽，其余六条过去会同步等待许可，耗尽全局八个准备 worker。新到主机 K 的任务因此无法开始。历史基线及约三秒阻塞证据见[准备池测量](2026-10-25-preparation-pool-saturation.md)。

生产 `ConsumeOne` 现在使用非阻塞许可获取。实际目标 hostname 的并发槽或请求预算不足时，不发送 HTTP、不消费 token，保存继续处理的位置和下次检查时间，归还上层 worker。现有 worker 按 feed 轮转处理其他可执行条目；下载正文仍持有正常 worker 和 HTTP 槽。

- 并发槽满时下次检查约 250 ms 后；请求预算不足时按现有限流器计算的许可时间检查。休眠 worker 根据数据库中最近的继续时间醒来，RSS 新条目仍通过原有通知唤醒。缓存中的相同下载正在进行时，其他 consumer 也归还 worker，不同步等它结束。
- 默认全局准备并发 8、每来源/实际 hostname 并发 2、请求预算 30/min、burst 4 不变。没有新建每任务 goroutine 或额外消息服务。直接调用的阻塞 HTTP 客户端和手动 `Cycle.Run` 保留原接口行为。
- 数据库 migration 24 保存 `artifact_deferred_until`、内部继续 URL 和累计重定向数。重定向目标通过原有 allowlist、出站与凭据检查后才保存；恢复时再次检查。成功或实际失败会清理继续位置，总重定向限制不会因让位而重置。继续 URL 不进入 API、日志或展示队列。
- 让位归还本次重试预算。新增单调 `claim_generation` 同时约束完成、让位和异步进度写入，避免 attempt 次数被复用时旧回调覆盖新认领。重复让位不能重复减计数。实际失败仍最多五次，consumer 仍受原 24 小时处理期限限制。
- 等待许可显示为 `RATE_WAIT`，计入等待时长，不计为请求失败。许可重查时间使用与限流器相同的墙上时钟，业务 deadline、lease 和显示时间继续使用已有业务时钟；固定业务时钟的测试因此也能正确等真实网络许可。

## 验证

`TestPreparationPoolSaturation` 使用生产 `app.New`、SQLite、RSS 与 torrent 回环 HTTP，覆盖单来源、四来源共享下载 host、四来源经重定向共享下载 host。每组八条 H 任务加一条 K 任务，只有 K 先到达 HTTP 才释放 H。三秒是测试失败期限，不是产品延迟承诺。

本地完整 Go 回归中，默认 30/min 的结果如下：

| 场景 | K 的 RSS 响应至 HTTP 到达 | H 释放前开始 | H 最大 HTTP 并发 | 最终准备 / 总尝试次数 |
|---|---:|---|---:|---:|
| 单来源 | 1.053 ms | 是 | 2 | 9 / 9 |
| 四来源共享 H | 1.023 ms | 是 | 2 | 9 / 9 |
| 四来源重定向至 H | 1.250 ms | 是 | 2 | 9 / 9 |

重定向入口恰好收到八次请求。四来源的六条等待任务不再持续占有 worker。使用本轮代码但关闭 `ConsumeOne` 非阻塞上下文作为反向对照，四来源测试在 H 保持阻塞三秒后以 `H permission wait prevented K from preparing` 失败；这项对照只切换等待方式，不伪称是对历史提交的重新构建。

脱敏[回归日志](../evidence/preparation-isolation-20261025/runtime.log)、[测量数据](../evidence/preparation-isolation-20261025/results.json)及[反向对照](../evidence/preparation-isolation-20261025/blocking-control.log)随代码提交。另有 600/min 的快速功能夹具，保留相同隔离、并发、去重和完成断言，供 `-race -short` 执行；它不替代默认预算测量。CI race 范围增加 `torrentdownload`。

其他回归覆盖：许可拒绝不消费请求预算，RSS 保留额度和取消；数据库真实关闭重开后保留继续位置和到期时间；相同 attempts、不同 claim generation 的旧完成和进度拒绝；十二次让位不耗重试次数、真实失败五次停止和 deadline 仍有效；v23 升级保留租约和已完成 consumer；缓存等待让位；继续 URL 的 allowlist/凭据重验、跨让位重定向循环上限；API 等待状态与私有 URL 隔离。

固定参数 30 条流水线本轮 `ready → STARTED` p95 为 30.4233 ms，原 100 ms 失败断言保持。默认 30/min 自动组三机分配 11/11/8，首条 Add 约 17.1 ms、末条约 60.03 秒；站点预算仍决定整批尾部。全部指标均来自本地合成测试，不能代替异地 qB 实测。

## 交付与边界

后续补充[Runtime 真正关闭、重开与停用 feed 的组合回归](2026-10-25-preparation-restart-validation.md)：直接请求和跨域重定向均覆盖三次启动，确认继续位置不丢失、已完成准备不重放；生产逻辑没有追加改动。

本轮本地完整 Go、vet、代码生成和契约检查结果单独记录在 `artifacts/pipeline-phase7/`，CI/G1 按新提交 SHA 核对。首次本地全量执行还发现历史诊断 `.go` 文件被 Go 递归识别、Windows 临时数据库清理占用；诊断副本改为 `.go.txt`，保持测试断言后重跑。远端状态和部署证据写入该目录报告，不能以本文代替在线通过结果。

这是许可等待的隔离修复。当八个 worker 都在实际传输、解析或写入必要业务状态时，新任务仍受全局上限约束；本轮不提高该上限。没有新增真实 RSS/双机下载批次，现有 RSS 和测试规则继续停用。
