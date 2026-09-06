# Vertex Pro

Vertex Pro V1 是面向多 qBittorrent 实例的 RSS 自动投递、状态确认与持久化历史系统。

## 当前阶段

截至 2026-09-06，149 个正式实施项中 `137 DONE / 1 IN_PROGRESS / 11 PENDING`。阶段 A-I、K 和 G1 实验室验证已通过；Gate J、Gate L、G2、C1、A24 和 G3 仍待完成。当前施工前沿是 `UI-11` Settings、`HARD-02` 依赖最终重扫和 `HARD-07` 性能基线。

G1 已完成三 Profile（qB 4.3.8、5.0.4、5.2.2）的固定 qui 真实链路、21/21 用例、外部 kill -9 恢复和连续三次验证。验证报告与 activation binding 绑定构建 `63161b1`；生产运行时必须使用该构建，或在新 commit 上重跑 P0×3 后再激活 Profile。

RSS→规则→Placement→Reservation→Dispatch→FirstConfirm/对账的生产调度链已接通，但 qB 写操作仍遵循 `VERTEX_PRO_ENABLE_DISPATCH` 显式开启、Profile 验证通过才允许的 fail-closed 规则。当前主分支还不能声称 `FROZEN-V1`；G2、C1、A24 和 RC 冻结流程仍未完成。

镜像只通过 GitHub Actions 构建和发布。

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

阶段 A 的 `BOOT-01..09` 均已完成，Gate A 为 `PASS`。当前工具链固定为 Go `1.26.6`、Node `24.14.0`、pnpm `10.33.0`；后续任务以本文“当前阶段”和实施状态表为准。
