# 2026-10-26 SQLite 取消查询资源释放

## 触发与原因

前端修复提交 `b2bb8b0` 的 CI 中，双实例生命周期测试在关闭运行时、重新启动时出现 `initialize qui sync state: database is locked (5) (SQLITE_BUSY)`；同次独立 integration 检查通过。Windows 重复测试也出现数据库文件仍被占用、无法清理的失败。

检查当前 `modernc.org/sqlite v1.36.1` 源码发现：查询已产生 Rows，但 context 在函数返回前被取消时，驱动直接将 Rows 覆盖为 nil，调用方无法再关闭底层 statement。上游 [MR 81](https://gitlab.com/cznic/sqlite/-/merge_requests/81) 修复该路径，首次随 v1.40.1 发布。

新增回归让 SQLite 函数进入查询后再取消 context，验证同一连接可以 checkpoint、数据库关闭后可重新打开写入。旧版本首轮复现 `checkpoint=interrupted (9)` 和 Windows 文件占用；升级后连续 10 轮、合计 200 次取消查询通过。这证明驱动资源释放缺陷已修复；CI 原始锁错误通过生命周期重复回归和完整 CI 进一步验证，不以增加超时掩盖。

## 变更与边界

本地 `go test ./... -count=1` 完整回归通过；`TestGroupBatchLifecycleAndQuiPolicy` 连续 20 轮通过。`go vet ./...`、`go mod verify` 与生成代码一致性检查通过。证据位于 `artifacts/frontend-phase10/`，Linux race 和最终 G1 由本提交 CI 单独验证。

- SQLite 驱动固定为包含该修复的 v1.40.1，libc 同步为该版本要求的 v1.66.10，更新模块校验文件。
- 增加取消查询、checkpoint、关闭重开写入的回归，并将数据库包加入独立 Linux race 检查。
- 取消可能与成功返回竞争，两种结果都必须清理资源；测试不要求取消必然抢先。
- 不改变数据库 schema、任务状态、去重、重试策略或性能门槛；没有新建真实下载任务。
- 部署需使用最终提交对应的 CI/G1 产物，先在数据库副本演练，再备份并更新原服务。
