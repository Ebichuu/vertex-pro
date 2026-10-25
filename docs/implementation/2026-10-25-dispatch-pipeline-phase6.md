# 流水线第六阶段：重选恢复、容量与请求等待

本轮修复对 `bffd6df` 的评审指出的四处兼容问题。此前的三机合成测试和 G1 证据保留原适用范围；本轮继续遵守“先完成部署，暂不新增实测任务”，不启用真实 RSS、重置去重或申请新标签豁免。

## 修复行为

- 启动恢复允许历史终态 attempt 与后续当前 attempt 共存。历史终态仅在 job 仍指向它自身时报告 `JOB_POINTER_NOT_CLEARED`；另从 job 侧校验当前指针存在、属于同一 job、身份相同、状态允许且镜像一致，避免放过悬空或跨 job 指针。违规仍在任何恢复写入前拒绝启动。
- 重选对原目标的可用性复用 sender 的容量投影检查，覆盖物理空闲下降、未知负债和不可信容量。投影已经包含现有预留，不再次要求一份任务大小。连续失效满 30 秒才允许安全替换；恢复可用清零计时。已有 hash、STARTED 和 UNKNOWN 的保护保持不变。
- 限流等待进度改为单后台 worker、128 个尝试槽，按尝试合并最新等待状态和累计时长。取得许可后直接继续 HTTP；饱和丢弃新尝试的展示数据，不阻塞请求。每个展示写入最多 1 秒，不重试可能已生效的加法。关闭应用先停止准备 worker，再取消并等待进度 worker，最后关闭数据库。同代迟到结束只补时长，不恢复已结束状态；旧代不能写入新尝试。
- 实际 torrent 请求按目标 hostname 共享并发槽，采用 `MaxPreparationsPerHost` 配置，默认 2。不同 RSS origin、大小写、尾点及不同端口的同一下载 hostname 共用限制；每次重定向重新按目标获取。槽持有至正文结束或关闭，取消、错误及超时也释放。限流与并发等待不占网络超时，仍受整个准备任务期限约束；RSS 独立槽保留。
- CI 增加独立 Linux `-race -short` 检测，覆盖启动恢复、dispatchstage、rssfetch、schedulercycle、rsssourcedb 和 app。短模式跳过长批次性能夹具，原普通模式 100 ms p95 断言不放宽。

## 复现与回归

修正前，生产重选成功后真实关闭、重开 SQLite，再调用 `startuprecover.Recover`，新 attempt 为 RESERVED、ADD_SENT、HASH_SEEN 的三例均复现 `JOB_POINTER_NOT_CLEARED:job-1`。容量 42 字节的既有预留遇到 41 字节实际空闲时，原逻辑也复现未开始不可用计时。进度回调阻塞夹具复现获得许可后 HTTP 仍被写入阻挡。原始失败日志保存在本地 `artifacts/pipeline-phase6/`。

- 三种跨数据库启动边界回归修复后通过；重复恢复幂等，旧链保持 ADD_FAILED/RELEASED/CLOSED，RESERVED 新链只发一次，ADD_SENT 转 UNKNOWN 且不重发，HASH_SEEN 不重发。
- 错误自指、缺失目标、跨 job、镜像状态和身份不符均拒绝恢复且不写 waiter/event。历史 ADD_FAILED 还必须具有 FAILED_NO_EFFECT 事实；损坏为 POSSIBLE_EFFECT 时禁止恢复后继链。
- 容量失效后转移、恰好覆盖已有预留、空闲不足第二份预留、短暂恢复重置计时均通过。
- 真实 SQLite 连接池阻塞和真实 HTTP 测试证明进度持久化不挡请求；覆盖队列饱和、合并、迟到写入、重试代数及并发关闭。
- 真实 HTTP 覆盖两个 RSS origin 共用下载 host、重定向、正文持槽、取消/EOF/读错/重复关闭/网络超时释放，以及其他 host 和 RSS 不受该并发槽阻塞。
- 完整 `go test -p 2 ./... -count=1 -json` 的 106 个测试包通过，`go vet ./...`、生成契约一致性和契约检查通过。本机没有 C 编译器，race 结论以本提交 Linux CI 为准。

## 交付边界

本文件随代码提交，远端 CI/G1 和部署按该提交 SHA 单独核验，运行证据另记 `artifacts/pipeline-phase6/REPORT.md`。本轮没有新增真实双机下载任务；RSS 到异地 qB、实际下载增长、做种与删除的新版端到端时延仍未验收。旧批次和托管策略继续停用，指定保留的旧做种任务不受本轮影响。
