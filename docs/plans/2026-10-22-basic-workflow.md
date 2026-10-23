# 基础功能接线与验收

范围：沿用 RC8 与用户合同，优先完成 UI-03..11、RSS-01..06、RULE-01..11、API-04/10、STAT-01..04 已有模块的真实使用链。保留已有 Settings 改动，不把本地模块测试等同于 G1/G2 或正式发布验收。

## 完成条件

- 规则使用真实站点、下载器与持久化版本；刷新后可以读取和编辑原条件、动作，保存失败不会显示成功。
- Dry-run 使用服务端规则计算并说明缺失事实；详情、异常和历史分页不使用产品示例数据。
- RSS 获取正确的种子 enclosure、准确大小与来源身份；首次建立基线，重启不会回刷，轮询遵守 Feed 周期。
- qui 实例、实时速率与统计从实际响应更新；缺失、离线与陈旧数据如实显示。
- 投递仍要求显式开启与有效 Profile 验证，UNKNOWN 不自动重放；不以固定 1 字节或静态可信状态替代容量证据。
- 以本地真实 HTTP/SQLite 的用户流程测试、前后端测试、类型/构建/合同生成检查及浏览器操作验证本阶段。

## 实施分工

- 前端规则与路由：真实创建/编辑、Dry-run、异常、分页。
- 前端详情：历史、下载器的 API 适配及未知数据展示。
- 资源 API：规则合同、查询详情、Dry-run 和错误边界。
- 主代理：工具链、后台运行装配、RSS、容量边界、集成与最终验证。

## 验证边界

当前 G1 档案绑定旧提交且已过期；本阶段不绕过验证门槛。镜像只通过 GitHub Actions 构建发布。真实 qui/qB 及 G2、性能和 RC 验收单独记录，未运行的项目不得标记通过。

## 2026-10-23 本地验收记录

已完成并验证：

- 修正调度读取不存在的 poll_interval_seconds，使用持久化 poll_minutes；首次基线不建任务，重启保留初始化、ETag 和轮询时间，禁用站点不轮询。
- RSS/Atom enclosure 相对地址解析与实际 torrent 大小；流量首次采样只建基线，重复/乱序采样不重复累计，计数回退重建基线。
- qui 缺失计数不当作零；下载器元数据返回真实名称，未知速率和任务数量返回 null。
- API 客户端绑定浏览器原生 fetch，CSRF Cookie 根路径可读并清除旧 API 路径令牌；Feed 返回 updated_at 供版本校验。
- 前端服务端 Dry-run、详情路由及规则组件测试与持久化接口对齐；SPA 页面刷新正常，未知 API/资源与路径穿越仍拒绝。
- Dispatch 按 torrent 实际大小预留，重下载后核对身份与不可变规则快照；固定目标、分类与标签生效，不支持的动作和目标明确拒绝。

验证结果：Go 全套 PASS；前端 20 文件 106 测试 PASS；tsc -b、Vite 生产构建、Go build、go vet、codegen -check、contractcheck PASS。最后 SPA 变更后复跑 webembed/app/resourcesapi/codegen 包 PASS。Vite 主 chunk 约 625 KB，仍有体积提示。

浏览器使用独立 artifacts/basic-workflow/browser.db：登录、设置保存、服务重启后 Asia/Hong_Kong 生效、真实无规则 Dry-run、规则空状态、/aggregation 刷新均已验证。截图为 artifacts/basic-workflow/settings-verified.png。未验证 1440/1024/390 全响应式矩阵，未进行真实 qui/qB 写入、镜像发布或生产部署。

剩余基础闭环问题：

- serve 尚未暴露 Scheduler/DispatchStage 的完整启动配置；当前自动调度验证覆盖 app 装配，不等于默认 CLI 已自动投递。
- DispatchStage 的容量和存储仍使用操作者提供的静态参考值，需要接入动态且可核验的容量/快照证据。
- 实例组、存储标签/Profile 约束、分类子目录与顺序下载尚未在 dispatchstage 支持，当前明确拒绝，不静默忽略。
- 全局轮询设置与每 Feed 周期的产品语义还需统一；当前 worker 至少每分钟检查一次，各 Feed 按 poll_minutes 控制到期。
- 真实站点/规则创建编辑完整浏览器流程、实时 SSE 恢复、多下载器完整同步与正式发布 Gate 继续保持未验收。

本轮未提交 Git，未更改正式 DONE/Gate 计数。Astra Ultra 子代理已创建，但多次未接收任务正文并返回待命，本轮新增修复和验收由主代理完成。

## 2026-10-23 真实环境补充

后续按用户授权，对织梦 RSS 的同一枚 325.88 MiB 种子经 qui 向实例 2 和实例 7 各提交一次暂停添加。实例 7 的完整哈希、路径、停止状态和 0 B 下载量已核实；实例 2 的任务被既有“除HH删种”工作流删除，未重复投递、未修改规则。此结果仅覆盖人工 RSS → qui → qB 的冒烟验证，Vertex Pro 自动调度及双端任务保留仍未通过。详见 [真实 RSS 冒烟记录](2026-10-23-live-rss-smoke.md)。
