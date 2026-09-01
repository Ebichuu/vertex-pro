# UI-09 Downloader Detail 设计

## 范围

在 `local-only` 边界内补齐下载器详情页。页面展示单个 qB 下载器的实时传输、任务数、容量、日/周/月流量统计和数据质量；不连接真实 API、数据库、qui、qB 或外部服务。

## 方案

采用“详情投影 + 可注入加载器 + 路由 fixture”方案：

- `DownloaderDetail` 只携带页面需要的实时、容量、周期统计和质量字段。
- `DownloaderDetailPage` 接收 `downloaderId` 与异步 `DownloaderDetailLoader`，独立负责加载、错误、重试、周期切换和展示。
- `/downloaders/$downloaderId` 默认使用确定性本地 fixture；总览卡片通过回调进入详情，纯组件测试不依赖 RouterProvider。
- 统计图使用语义化 DOM/CSS 柱状图，不引入额外图表运行时依赖；图表提供 `role=img` 文本摘要和隐藏的逐点数据。

## 数据质量

- `FRESH`、`STALE`、`UNKNOWN` 与下载器健康状态分别展示，不能把未知或过期数据美化为在线或零值。
- 容量缺失显示“暂无数据”，容量比例也显示未知。
- 周期摘要保留 `missingPoints` 和 `counterResets`，缺失小时在图表中留空，计数器重置以说明文字呈现。
- 数据来源和最近观测时间始终可见，方便后续替换为真实统计 API。

## 验收

- 覆盖在线下载器的实时状态、容量、图表和周期切换。
- 覆盖降级/过期下载器、未知容量和缺失小时。
- 覆盖未知下载器的失败态与重试。
- 覆盖总览卡片详情入口不依赖组件测试中的 RouterProvider。
- 通过前端测试、类型检查和生产构建；真实 qui/API 接线留给后续 API-10、API-11 与统计链路闭合阶段。
