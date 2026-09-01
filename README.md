# Vertex Pro

Vertex Pro V1 是面向多 qBittorrent 实例的 RSS 自动投递、状态确认与持久化历史系统。

## 当前阶段

正式 Gate 仍停在阶段 A 的 `BOOT-01`，Gate A 尚未通过。功能开发已在 local-only 范围内触达阶段 J 的 `UI-10`，并补齐 UI-03 Sites & RSS、UI-04 Rules 列表、UI-05 Rule Editor、UI-06 Dry-run/聚合预览、UI-07 History、UI-08 History Detail 和 UI-09 Downloader Detail；当前 149 个实施项中 121 项为 `IN_PROGRESS`、28 项为 `PENDING`，没有任何项标记为 `DONE`。本轮已接通并验证 Go 启动装配、SQLite migration/一致性检查、认证资源 API、SSE 边界、前端构建和 Docker Desktop 容器边界；另已通过真实 qui Client Proxy 接通一台 qB 5.0.4 的只读 Control、Profile、Transfer、Capabilities 和连续 Sync 链路，运行期间未调用任务写端点且任务集合保持不变。当前生产式 qui Proxy 对 RID follow-up 仍返回 `full_update=true`，因此只宣称完整快照 reset 安全，不宣称该链路的真实增量 delta 已验证。另已在用户明确授权下，由隔离协议 harness 通过 qui Proxy 新增并保留一个无 Tracker、1 字节、`stopped=true` 的测试任务，验证 qB 5.0.4 的 legacy Add wire response 与后续 Sync Identity 可见；随后生产 Go 客户端复用该任务完成 Already-present 不 dispatch、`ReserveAtomic`、原子释放 Reservation、幂等重放以及双启动只读恢复验证，原有任务均未修改或删除。额外的完全隔离 Docker lab 已让生产 Go `DispatchWorker` 经 qui Client Proxy 对空 qB 5.0.4 完成 Add→`stoppedDL`，并验证 duplicate/invalid/Tracker/transfer-info/增量删除后清空 lab；但 lab qui commit 尚未匹配冻结 lock，完整 Placement/SubmitIntent/首次确认、故障矩阵、其余冻结 qB Profile 和发布级集成证据仍未完成。

产品边界、状态机和安全不变量以 `docs/产品方案设计/VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md` 为准；施工顺序以 `docs/实施流程/VERTEX_PRO_V1_CODE_IMPLEMENTATION_PLAYBOOK.md` 为准；具体进度以 `docs/实施流程/IMPLEMENTATION_STATUS_V1.md` 为准。

## 目录约束

- `cmd/`：可执行程序入口。
- `internal/`：领域、应用服务、适配器和基础设施实现。
- `api/` 与 `contracts/`：唯一接口和状态合同源。
- `web/`：React 前端。
- `tests/`：合同、集成、协议、故障和性能测试。
- `tools/`：合同检查、协议探针、脱敏和许可证检查工具。
- `deploy/`：容器和实验室部署文件。
- `docs/`：规范、ADR、实施记录、运行手册和证据。
- `artifacts/`：生成的验证证据，不默认作为业务数据目录。
- `third_party/qui/`：固定的 qui 上游锁定信息。

## 开发原则

- qBittorrent 的控制和数据访问只能经由 qui。
- `internal/domain` 与 `internal/state` 不得依赖 HTTP、SQLite、qui Client 或前端代码。
- API 层不得直接执行 SQL 或调用 qui。
- 未经明确记录的合同、状态、错误码和事件变更不得进入实现。
- 不得使用 `latest`、伪造测试证据或把未完成项标记为 `DONE`。

## 本地启动

不配置 qui 时保持 local-only：

```powershell
go run ./cmd/vertex-pro serve
```

启用已验证的 qui runtime 时，从 `.env.example` 复制所需环境变量并在进程环境中注入 Secret；不要把真实 Key 写入仓库。该 runtime 当前只自动执行 Control/Profile/Transfer/Capabilities 和 Sync 读取，不会自动启动 Add Worker：

```powershell
$env:VERTEX_PRO_QUI_URL = "http://qui.example:7476"
$env:VERTEX_PRO_QUI_INSTANCE_ID = "13"
$env:VERTEX_PRO_QUI_API_KEY = "<control-api-key>"
$env:VERTEX_PRO_QUI_PROXY_KEY = "<per-instance-client-proxy-key>"
go run ./cmd/vertex-pro serve
```

已保留的 inactive 测试 Identity 可通过只读事务探针复核；该工具的 HTTP transport 只允许 `GET`，使用临时 SQLite 调用 `ReserveAtomic` 构造 `Reservation` / `SubmitIntent` 并执行 Already-present 原子事务，结束后删除临时数据库：

```powershell
go run ./tools/qupresentprobe
```

StorageTarget 证据可用 `tools/qustorageprobe` 只读核验。管理员设备/挂载证据文件必须保存在仓库外并限制 ACL；工具输出不会包含真实路径、device/mount 原文或 Secret：

```powershell
$env:VERTEX_PRO_STORAGE_EVIDENCE_FILE = "$env:LOCALAPPDATA\VertexPro\live-storage-verification.json"
go run ./tools/qustorageprobe
```

完成 `BOOT-01` 后，按进度表继续执行 `BOOT-02`，固定 Go、Node、pnpm 工具链。
