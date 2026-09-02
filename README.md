# Vertex Pro

Vertex Pro V1 是面向多 qBittorrent 实例的 RSS 自动投递、状态确认与持久化历史系统。

## 当前阶段

`BOOT-01` 已由提交 `f741663` 正式收口，当前施工入口推进到阶段 A 的 `BOOT-02`；Gate A 仍为 `PENDING`。149 个正式实施项中现有 `1 DONE / 120 IN_PROGRESS / 28 PENDING`。功能开发在 local-only 范围触达阶段 J 的 `UI-10`，但依赖、正式 Gate 和发布证据尚未闭合。

隔离 G1 实验室已从冻结 commit 构建 qui，并覆盖精确 qB 4.3.8、5.0.4、5.2.2。三 Profile 的核心生命周期以及归档的 `3 Profile × 6 mode` 事务/故障矩阵均为 PASS，18 次投递均只有一次 sender 调用、没有自动重投，且每次实验室最终为空。当前 Dispatch 实现又进一步修复了崩溃窗口：本地校验通过后，先在 SQLite `BEGIN IMMEDIATE` 事务中原子持久化 `ADD_SENT / STARTED / POSSIBLE_EFFECT`，再进行唯一一次网络发送，最后单独记录响应事实；若进程在发送前后中断，启动恢复会把遗留 `ADD_SENT` 提升为 `UNKNOWN` 并禁止重放同一 Attempt。明确未发送的结果关闭为 `ADD_FAILED / FAILED_NO_EFFECT`，安全重试只能走两阶段重排并创建新 Attempt，不能把旧 Attempt 退回 `NOT_STARTED`。

上述归档矩阵早于新的“发送前 durable marker”证据字段，当前 Docker Desktop daemon 又未运行，因此尚未在这一源码版本上重跑；外部进程 `kill -9`、`profile_verification_runs` 持久化、固定 harness image、clean commit 上 P0 连续三次稳定运行也仍缺失。三个 Profile 继续保持 `EXPECTED`，G1 保持 `PENDING`，生产 mutation 默认关闭；即使显式设置 `VERTEX_PRO_ENABLE_DISPATCH=true`，Profile 非 `VERIFIED` 时仍 fail closed。RSS→规则→Placement→Reservation→Dispatch→FirstConfirm 的生产调度链也尚未接通。

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

启用 qui runtime 时，从 `.env.example` 复制所需环境变量并在进程环境中注入 Secret；不要把真实 Key 写入仓库。该 runtime 当前只自动执行 Control/Profile/Transfer/Capabilities 和 Sync 读取，不会自动启动 Add 调度链：

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

`BOOT-01` 已完成；下一项按进度表执行 `BOOT-02`，固定 Go、Node、pnpm 工具链。
