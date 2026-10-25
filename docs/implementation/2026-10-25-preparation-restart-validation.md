# 准备许可让位后的 Runtime 重启回归

生产基线为 `03603baae4abed47178a75e2487fb9793eac8752`。本次继续检查部署后的行为，增加组合回归和证据；没有修改生产调度代码。

## 跨启动边界验证

`internal/app/preparation_restart_test.go` 的 `TestDeferredPreparationSurvivesRuntimeRestart` 使用真实 `app.New`、后台 RSS/准备 worker、SQLite 和回环 HTTP，覆盖直接下载及跨 hostname 重定向。

1. 使用生产共享限流器的两个受控 HTTP 请求占住目标 host 的两个槽，启动测试 feed，等待 consumer 真实保存继续位置并让出 worker。
2. 停用测试 feed 并等待当前认领退出，然后调用 `Runtime.Close`。断言数据库连接全部释放且 `Ping` 失败，再用同一文件调用 `app.New`，真实执行启动恢复。
3. feed 仍停用时，显式运行调度周期并等过许可重查时间和一秒后台轮询，确认继续位置、跳数、代数和尝试计数未变化，没有请求种子。
4. 重新启用夹具 feed，由后台 worker 自动完成准备。重定向场景直接从持久化目标继续，入口没有重复请求。
5. 再关闭、重开 Runtime 并重复运行调度周期，验证只有一个 race job、一个 artifact、一次 torrent 获取、一次 RSS 获取；重定向入口总请求次数为一。

这里的 feed 和 HTTP 服务都是测试进程内夹具，不是用户真实订阅。受控占槽请求不绑定 consumer，避免把取消下载后的两分钟租约恢复混入许可让位回归。没有配置 qui 或 Add sender；本测试证明准备与建单的恢复，不声称验证真实 qB Add 或下载数据增长。

两种场景均通过，共六次 Runtime 启动。原始[回归日志](../evidence/preparation-restart-20261025/runtime-restart.log)随本次提交保存。该测试不在 `-short` 时跳过，因此现有 Linux race 和普通 CI 都会执行；远端结果按本次提交 SHA 单独核验。

## 部署后的只读复查

2026-10-25 21:20（Asia/Hong_Kong），本机 `127.0.0.1:18963` 仍运行 `03603ba`，运行约 21 分钟，进程内存约 35 MB。health/ready、认证、管理 API、实例同步和发布文件哈希复查通过。两台配置实例为 READY/FRESH；10 条历史任务保持 DELETED，RSS、测试规则和托管策略启用数均为 0，指定保留的旧做种任务仍在。

这是短期运行快照，不代表长时间 soak 通过。本次不重启生产服务、不新增真实下载批次，生产继续使用已完成 G1 和部署验收的 `03603ba`。本机复查报告位于 `artifacts/pipeline-phase7-followup/`。
