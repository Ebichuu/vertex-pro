# Vertex Pro V1 实施状态表

文档状态：`ACTIVE / AUTHORITATIVE-TRACKER-BY-RC8`  
最后修订：2026-09-02
对应施工手册：`docs/实施流程/VERTEX_PRO_V1_CODE_IMPLEMENTATION_PLAYBOOK.md`  
对应主规范：`docs/产品方案设计/VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md`  
对应产品合同：`docs/产品方案设计/VERTEX_PRO_V1_USER_RULES_CONTRACT.md`  
仓库目标路径：`docs/实施流程/IMPLEMENTATION_STATUS_V1.md`

---

## 1. 使用规则

本文件只记录实施进度和证据，不重新定义业务语义。实施方法以施工手册为准，产品和安全不变量以 RC8 为准。

允许状态：

```text
PENDING -> IN_PROGRESS -> IN_REVIEW -> DONE
                    \-> BLOCKED -> IN_PROGRESS
```

更新要求：

- 领取任务时填写 owner、branch、started commit 和更新时间。
- 打开 PR 后改为 `IN_REVIEW`，填写 PR 链接和测试命令。
- 合并且证据齐全后才能改为 `DONE`，填写 merge commit、evidence path 和 SHA-256。
- 依赖未完成时原则上不得开始；安全可并行时在备注写明不共享的边界。
- 后续证据推翻已完成项时改为 `BLOCKED`，保留旧 merge/evidence，不删除历史。
- 阶段 Gate 和代码项分开判断；代码全 DONE 不等于 Gate 自动 PASS。
- Depends on 支持 `PREFIX-01..05` 闭区间和同组缩写 `PREFIX-01,03,05`；缩写数字继承最近的前缀，计划检查必须全部展开为真实 ID。除这两种语法外只能使用真实实施项 ID 或 `—`。

## 2. 当前总览

| 阶段 | 范围 | 代码状态 | Gate | Gate 状态 |
|---|---|---|---|---|
| A | 仓库与骨架 | `1/9 DONE; 8 IN_PROGRESS` | Gate A | `PENDING` |
| B | 合同与生成 | `0/8 DONE; 8 IN_PROGRESS` | Gate B | `PENDING` |
| C | SQLite 与事务 | `0/11 DONE; 11 IN_PROGRESS` | Gate C | `PENDING` |
| D | qui、Profile、Sync | `0/12 DONE; 12 IN_PROGRESS` | Gate D / G1 基础 | `PENDING` |
| E | RSS、Torrent、并行准备 | `0/13 DONE; 13 IN_PROGRESS` | Gate E | `PENDING` |
| F | 规则与 Query Builder | `0/12 DONE; 12 IN_PROGRESS` | Gate F | `PENDING` |
| G | Placement 与容量 | `0/14 DONE; 14 IN_PROGRESS` | Gate G / G2 基础 | `PENDING` |
| H | Add、确认与恢复 | `0/14 DONE; 14 IN_PROGRESS` | Gate H | `PENDING` |
| I | API、认证与 SSE | `0/12 DONE; 12 IN_PROGRESS` | Gate I | `PENDING` |
| J | 前端页面 | `0/13 DONE; 10 IN_PROGRESS; 3 PENDING` | Gate J | `PENDING` |
| K | 历史、统计与观测 | `0/12 DONE; 6 IN_PROGRESS; 6 PENDING` | Gate K | `PENDING` |
| L | 加固与运维 | `0/12 DONE; 12 PENDING` | Gate L | `PENDING` |
| M | RC 与冻结 | `0/7 DONE; 7 PENDING` | Gate M / G3 | `PENDING` |

总体状态：`1/149 DONE`；`120 IN_PROGRESS`；`28 PENDING`。
正式施工项推进至阶段 A `BOOT-02`（Gate A `PENDING`）；功能开发触达：阶段 J `UI-10`（local-only）。

### 2026-08-30 状态校准

- Go：`go test ./...`、`go vet ./...` 通过。
- 前端：15 个测试文件共 70 项测试、类型检查和生产构建通过；主 JS chunk 约 586 KB 的提示为非阻塞体积警告。
- 生成链：`go run ./tools/codegen -check` 通过，TS Client 支持调用方指定泛型响应类型。
- 状态含义：`IN_PROGRESS` 只表示 local-only 实现或证据已存在，不代表依赖闭合、Gate 通过或生产链路可运行。
- 未覆盖范围：真实 API/数据库启动链路、qui/qB、SSE 完整封装及外部服务联调。

### 2026-08-31 状态校准

- 容器：Docker Desktop Linux engine 已完成镜像构建、lab compose 启动和清理；`healthz`/`readyz` 为 200，认证状态为 200，未认证 Dashboard/SSE 为 401；容器为 non-root、只读根文件系统并 drop all capabilities。
- 合同：`go run ./tools/contractcheck` 的 11 项 C1 检查全部 PASS，但报告 `eligible_for_g3=false`，因为仍是 local-only 且没有真实 qui/qB 证据。
- Go：`go test ./... -count=1`、`go vet ./...`、Docker Linux `go test -race ./... -count=1` 通过；`gofmt -Check` 通过。
- 前端与生成：16 个测试文件共 72 项测试、类型检查、生产构建和 `go run ./tools/codegen -check` 通过。
- 命令链：preflight 已实际执行并通过 `generate-check`、`fmt-check`、`lint`、`test-unit`、`test-contracts`、`test-integration`、`test-e2e`、`test-protocol`、`test-fault`、`verify-license`、`verify-implementation-plan`；四个原主动失败占位入口已替换为对应的 Go 测试套件，但发布级外部三 Profile G1 仍独立受阻。
- 结论：上述检查通过不改变 149 项实施状态、Gate 状态或 BOOT-01 的 baseline commit 阻塞。

### 2026-09-01 状态校准

- 真实链路：已通过真实 qui Control API 与每实例 Client Proxy 接通一台 qB 5.0.4（WebAPI 2.11.2、libtorrent 2.0.11.0），Control、Transfer、Capabilities、Profile、初始完整 Sync 与 RID follow-up Sync 均通过；该 qui Proxy 对 follow-up RID 仍返回 `full_update=true`，不宣称真实增量 delta 已验证。
- 运行装配：Go 服务可选启动只读 qui runtime，启动时 fail closed 探测并持续持久化 Sync snapshot；认证后的 Instances 与 Dashboard API 可读取该实例状态。
- 安全边界：只读探测和持续运行期间未调用新增、暂停、恢复、删除、reannounce 或其他任务写端点，任务集合保持不变；随后在用户明确授权下只新增并保留一个无 Tracker、1 字节、`stopped=true` 的测试任务，原有任务均未修改或删除。仓库未保存主机、任务名、Torrent Hash 或凭据。
- Add 协议验证：生产式远程实例的隔离协议 harness 经真实 qui Client Proxy 收到 HTTP 200/legacy `Ok.`；该响应语义对应设计分类 `POSSIBLE_EFFECT`，后续 Sync 唯一匹配预期 Identity，并观察到 `stoppedDL`，任务数严格增加 1。另在完全隔离 Docker lab 中，生产 Go `DispatchWorker` 通过 qui Client Proxy 对空 qB 5.0.4 成功发出同一 `stopped=true` Add 并确认 `stoppedDL`，随后 protocol harness 验证 duplicate Add、invalid Add、Tracker、transfer-info、增量删除事件并清空 lab。
- 状态含义：QB_504 的真实 Control/Profile/Sync、受控 inactive Add→Sync wire protocol和隔离 lab 生产 Go DispatchWorker 已验证；但 lab qui v1.0.0 commit 与冻结 lock 不一致，且完整 Placement/SubmitIntent/首次确认编排与 G1 故障矩阵尚未通过，因此产品 Profile 仍为 `EXPECTED`，实例不得进入 `AVAILABLE`。
- Go 恢复验证：受 coding harness 非 loopback 出站限制影响，生产 Go 进程通过 loopback-only bridge 访问真实 qui；复用同一数据库和 snapshot root 连续启动两次均 READY/优雅退出，仅观察到 GET，revision 前进且原有任务集合不变。该环境桥接不改变产品网络要求，仍需在非 sandbox 部署环境补一次直接连接证据。
- Storage/容量只读证据：真实 qB 默认保存路径存在、`free_space_on_disk` 合法且新鲜容量快照校验为 `TRUSTED`；管理员在 qB Linux 主机提供只读 mount/device 证据，managed root 选择 qB 默认保存路径，设备标识一致并通过 `storageverify.Validate`，Storage 证据现为 `VERIFIED`。原始路径与挂载信息仅保存在仓库外受限 ACL 文件；因 QB_504 Profile 仍为 `EXPECTED`，Placement 继续 fail closed、Profile 不得提升。
- G1 实验室进展：已从冻结 commit `8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896` 构建 qui，并将精确 qB 4.3.8/WebAPI 2.8.2/libtorrent 1.2.14.0、qB 5.0.4/WebAPI 2.11.2/libtorrent 2.0.11.0、qB 5.2.2/WebAPI 2.15.1/libtorrent 2.0.13.0 组成四容器内部网络。三 Profile 的核心生命周期以及归档的 3 Profile × 6 mode 事务/故障矩阵均 PASS；18 次 sender 调用均无自动重投，最终 lab 任务、容器、卷和临时 Secret 均已清理。
- Dispatch 安全边界：`0de553c` 将投递拆为发送前 `Begin` 与发送后 `Record`。本地请求校验通过后，先在 SQLite immediate transaction 中原子持久化 SubmitIntent `STARTED`、Attempt/Job `ADD_SENT` 和 `POSSIBLE_EFFECT`，再允许唯一一次网络发送；启动恢复把中断的 `ADD_SENT` 提升为 `UNKNOWN` 并禁止重放。明确 not-sent 关闭为 `ADD_FAILED/FAILED_NO_EFFECT`，只能经两阶段重排创建新 Attempt。
- G1 判定：归档 18 例早于新的 durable-marker 证据字段；当前 Docker Desktop daemon 未运行，尚未在这一源码版本重跑。外部 `kill -9`、`profile_verification_runs` 持久化、固定 harness image 和 clean commit 上 P0 连续三次运行仍缺失；三个 Profile 继续保持 `EXPECTED`，G1 保持 `PENDING`。

### 2026-09-02 状态校准

- `BOOT-01` 已由 `f741663a5f27820915c8161201acfb3831bd7369` 收口；总体进度更新为 `1 DONE / 120 IN_PROGRESS / 28 PENDING`，Gate A 仍等待 BOOT-02～09。
- Dispatch 崩溃窗口修复提交为 `0de553ca20876ef1532f63c819febd4d4e25a08f`；覆盖发送前校验、发送前 durable marker、发送后但响应提交前崩溃、提交后幂等、并发 Begin 和启动恢复不可重放。
- 三 Profile lab/归档矩阵提交为 `ec84241`；`g1-three-profile-matrix-summary.json` 汇总 18/18 PASS，同时明确当前源码 revalidation 为 `PENDING_REVALIDATION`，不得据此提升 Profile。
- 当前工作区完整 preflight 的 11 个目标全部 PASS；Go 全量测试/vet、16 个前端测试文件共 81 项、typecheck、生产构建、代码生成、许可证、实施计划和 compose 静态解析均通过。前端主 JS chunk 约 596 KB 的提示仍为非阻塞体积警告。
- Windows race 因 `CGO_ENABLED=0` 无法执行；Docker Desktop daemon 未运行，Linux race 和当前源码三 Profile 重跑保持 BLOCKED。
- 生产 mutation 继续默认关闭；Profile 非 `VERIFIED` 时显式启用仍 fail closed。RSS→规则→Placement→Reservation→Dispatch→FirstConfirm 的生产调度链尚未接通。
- Profile 验证持久化落地：`a314d42` 新增 `internal/profileverify`、`internal/buildidentity`、`tools/profileverification` 与 migration `0011_profile_verification_runs.sql`，固化 run 级 commit 清洁度、镜像/构件摘要、kill -9 用例与 required_repetitions=3 等硬性约束。
- Docker Desktop daemon 恢复后，在同一 `a314d42` 源码上重跑三 Profile 3×6 事务/故障矩阵：18/18 PASS、0 次自动重投、全部 `durable_marker_before_send=true` 且响应事实已记录，lab 已清空。矩阵摘要刷新为 `sha256:9c8b048e2b437169586ca0ae3c3fe11c51ac93321692d9181f766f60a3bcada6`（注意：摘要在运行中生成，`git_clean=false` 属预期，重跑时的源码与提交 `a314d42` 一致）。
- 重跑后 G1 剩余阻塞收敛为：外部 `kill -9` 用例、`profile_verification_runs` 正式接线三连跑（P0×3）、固定 harness image 与 clean-commit 发布级证据；三个 Profile 继续保持 `EXPECTED`，G1 保持 `PENDING`。
- 外部 kill -9 用例落地并三 Profile 全 PASS：`qutransactionprobe` 新增 `external_kill` 模式，父进程 re-exec 真实子进程经 qui Proxy 发送 Add（durable marker 先行、信号后挂起、`dispatchcommit.Record` 永不进入），父进程确认任务物理到达 lab 后强杀子进程；同一数据库启动恢复提升 `ADD_SENT→UNKNOWN`（`STARTUP_INTERRUPTED_DISPATCH`）、重放被拒（`ErrDispatchAlreadyStarted`、0 次额外发送）、快照对账收敛 `RECONCILED_CONFIRMED` 且 `response_fact_at` 保持 NULL（被杀进程从未收到响应，不伪造响应事实）。三份证据 `lab-qb{438,504,522}-external-kill.json` 已归档；矩阵汇总新增 `external_kill_matrix` 段（3/3 PASS）。G1 剩余阻塞收敛为：golden fixture 归档与 digest 持久化、固定 harness image、clean-commit 上 P0×3 连续运行并记录 verification runs。
- Golden fixture 归档完成并三 Profile 全 PASS：`qutransactionprobe` 新增 `fixture_capture` 模式，经 qui Proxy 采集原始 Add 请求/响应、Sync 全量基线与含新增任务的 follow-up、tracker 行和 transfer 字段子集（`amount_left` 等；因 qB proxy 白名单不暴露 `torrents/transferInfo`，来源在 fixture 内如实标注为 sync 任务字段）。12 份 fixture 写入 `docs/evidence/qui-12/fixtures/` 并由汇总脚本逐文件校验 SHA-256；全部载荷不含凭据。G1 剩余阻塞收敛为：固定 harness image、clean-commit 上 P0×3 连续运行并记录 verification runs。
- Harness 固化为容器镜像并完成首个完整容器化矩阵运行：`deploy/compose/lab/images/harness/Dockerfile` 将宿主机 clean 树构建（linux/amd64、`-trimpath`、嵌入 vcs 身份）的 `qutransactionprobe` 二进制封装进 pinned alpine 镜像 `vertex-pro-g1/harness:pinned`，probe 全部经 `docker compose run` 以 non-root、只读根文件系统、drop ALL、tmpfs /tmp 执行，evidence 目录经 `VERTEX_PRO_G1_EVIDENCE_DIR` 显式挂载；`g1-harness-identity.json` 归档 image_id 与 source commit。21 事务/故障用例与 3 份 fixture 采集在此镜像内全 PASS。G1 剩余阻塞收敛为：clean-commit 上 P0×3 连续运行并记录 verification runs。
- Clean-commit P0×3 战役完成并全 PASS：harness 二进制/镜像从 clean 树 `15707ff` 构建（`vertex-pro-g1/harness:pinned` = `sha256:624069b7...`），三轮完整 P0 矩阵（18 事务/故障 + 3 外部 kill -9 + 3 fixture 采集 ×3）全部 PASS、0 自动重投；每轮矩阵摘要归档于仓库外 campaign 目录并按轮次取摘要。9 条 `profile_verification_runs`（3 Profile × 3 repetition，`required_case_count=21`、`kill9_case_passed=true`、`verification_state=VERIFIED`）经 `tools/profileverification` 持久化，第三次重复后 Resolve 判定 `VERIFIED_RUN_MATCHED` 并产出三份 profile activation binding，连同 run JSON 与 campaign-manifest 归档于 `docs/evidence/qui-12/verification-campaign/`。recorder 构建身份为当前 clean commit `63161b1`（与 harness 源码 `15707ff` 仅差文档）。至此 G1 所列阻塞全部闭合；Profile 提升与 G1 Gate 仍待生产装配读取 campaign runs 并由正式评审确认。

> 总数由本表的 149 个正式实施项计算。若施工手册新增、删除或改名实施项，必须同 PR 更新本表并由合同检查验证集合一致。

## 3. 实施项清单

表格中的 `Evidence` 在 PENDING 时留空；完成后填写仓库相对路径及 `sha256:<64-hex>`。

### A. 仓库与可构建骨架

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `BOOT-01` | 单仓库和目录合同 | — | DONE | Codex | `f741663a5f27820915c8161201acfb3831bd7369` | `docs/evidence/boot-01/layout-check.json` / sha256:588d2fa5a8654dacc796a8d7dc90e8743ea7724b04ac35343af5a7475362c37e; `docs/evidence/boot-01/readiness-check.json` / sha256:a431ac74ad91ab93527629d868b9cc8e911262cd17c08563518e381d9cab931a |
| `BOOT-02` | 固定 Go/Node/pnpm 工具链 | BOOT-01 | IN_PROGRESS | Codex | local-only | `go.mod` / sha256:ce30f9a9ff9a9115ba5758b46a31e993a518b3ea260d23448b19e0ac57f16fb2; `pnpm-lock.yaml` / sha256:0d0f142ee439b8faec01d1f40586c27e45e7bc0b6016e034a5c659b3374015ee |
| `BOOT-03` | GPL 项目文件 | BOOT-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-03/license-check.json` / sha256:96c4df1a3802345a0d78ae69dda400bcf9222d348fa29aab9dffef15b7c95277 |
| `BOOT-04` | 固定 qui 上游 | BOOT-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-04/qui-lock-check.json` / sha256:b4defb857d0f9ab2a2aa69a1de31240abc21f7cdb3daff110edd05c87cecc3ef |
| `BOOT-05` | Make 与 CI | BOOT-01..03 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-05/preflight-check.json` / sha256:34532b67bf65b991533e4a1ed5ae82fffd89a84de2211f8ed7482a4129c09492 |
| `BOOT-06` | Go 服务骨架 | BOOT-02,05 | IN_PROGRESS | Codex | local-only | `cmd/vertex-pro/main_test.go` / sha256:a2a5c80284637653eab771d7180c03ba7fd7e4ec1e06fb11fd7196fcafe7344a |
| `BOOT-07` | React 应用骨架 | BOOT-02,05 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-07/frontend-check.json` / sha256:d66fbd061c450d571ad6ecd8b8e5f047c8c17ed378e18b2f8c3f8300085d62ea |
| `BOOT-08` | 单二进制和容器 | BOOT-06,07 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-08/container-check.json` / sha256:68e43b3d83300185b4c19782f4f80345dff5dda1927f5cec7a8078a8e90b662c |
| `BOOT-09` | 结构化日志与关联 ID | BOOT-06 | IN_PROGRESS | Codex | local-only | `docs/evidence/boot-09/observability-check.json` / sha256:e7d52a49a3b4dcf9a1fd85719dbe66168f5180f3d180c863937a4f83f8958955 |

### B. 合同优先与生成链

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `CTR-01` | OpenAPI 3.1 基础 | BOOT-05,06 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-01/openapi-check.json` / sha256:4f9d2048f060a79fe070435763fc129fb6297c278417a3566c80cd77fd8ebfc7 |
| `CTR-02` | 状态枚举清单 | BOOT-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-02/state-enums-check.json` / sha256:1bdf29824286dd72828a8657050ca3ffb1e6e11b99cbd567e0ece9a03729028e |
| `CTR-03` | 错误码清单 | CTR-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-03/error-codes-check.json` / sha256:a4725d5402f71b2e503aeec6066721e23bb7cf4ab758281c94a82fc275c8959e |
| `CTR-04` | 事件类型清单 | CTR-01,02 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-04/event-types-check.json` / sha256:016e231696b962f3b70f9cbc028b3255c9ac6d0bdf363c13c9c0888c120d72df |
| `CTR-05` | 用户状态映射 | CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-05/user-status-mapping-check.json` / sha256:a7b6e4c7d65d9773c5be774fc26bcf5ff2c51e33613eb8a8d8fd47764934ec85 |
| `CTR-06` | Go/TS 代码生成 | CTR-01..05, BOOT-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-06/codegen-check.json` / sha256:3ad271c683c63c385430532f3e3a4f42583f48bc50fc58985151137647c690c1 |
| `CTR-07` | C1 工具骨架 | CTR-01..06 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-07/c1-check.json` / sha256:7bee9d984f78bfccfbaa93678bbe5afad544abd63022ef4a6148e63cbc0b27cd |
| `CTR-08` | Mock API 与 MSW | CTR-01,06, BOOT-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/ctr-08/msw-check.json` / sha256:acbb0cd4241ada659ff351d38a6ce4e041ec7137f676e4777272d3c2d474dcbf |

### C. SQLite、Migration 与事务基础

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `DB-01` | SQLite 连接工厂 | BOOT-06 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-01/sqlite-connection-check.json` / sha256:7914998197e022efbaae227ee8a8c402eaf436dd6a78bf415af154da6af2b022 |
| `DB-02` | Migration runner | DB-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-02/migration-check.json` / sha256:5985a5138bdf922c9643a3966023f53ab29861e450379a8204284a874e8a18b8 |
| `DB-03` | Site/Feed/Rule/Identity 表 | DB-02, CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-03/site-feed-rule-identity-check.json` / sha256:83f20211de72a3c4d4fa2bd5dab9421b01388d73475d27d926227851b6306449 |
| `DB-04` | Sync/Snapshot/Waiter 表 | DB-02, CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-04/sync-snapshot-waiter-check.json` / sha256:9fbbee4a69a9bd4ba99e1566abecacd932e16f1873a0135932fbcf5743f9841c |
| `DB-05` | Storage/Capacity 表 | DB-02, CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-05/storage-capacity-check.json` / sha256:453a2c07162814dbb9cad517cdd30cc4e2e7aa8c0ccfe1e01b3e37a1846144a9 |
| `DB-06` | Race/Attempt/Intent 表 | DB-02, CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-06/race-attempt-intent-check.json` / sha256:62affbd50f8ee127075e04dc988cab468656d9cc2e0b59246745df9fdd8b2382 |
| `DB-07` | Events/Commands/Stats 表 | DB-02, CTR-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-07/events-commands-stats-check.json` / sha256:76c8ca4a8562b64909561599f4388527fc3bc049840571a0c4449f33f016a9de |
| `DB-08` | 索引与触发器 | DB-03..07 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-08/index-trigger-check.json` / sha256:543299e124bbbf9dc8cf4af7939e03e519ad7585e79d350eb806a126af8a2de2 |
| `DB-09` | TxRunner 与 Repository | DB-03..08 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-09/txrunner-repository-check.json` / sha256:59b82b6b3d07b190d0ae3583871b916a9e2ce6dc6d9b65d832caf15ec32c8905 |
| `DB-10` | 启动一致性检查 | DB-08,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-10/startup-consistency-check.json` / sha256:8d4eda5969488ca441da3adced26cdb2389f9783d2aad1e0e0ac39bd0366bd29 |
| `DB-11` | 升级与备份路径 | DB-02..10 | IN_PROGRESS | Codex | local-only | `docs/evidence/db-11/upgrade-backup-check.json` / sha256:0b0e9e9d25795f0b53f4c8f67b2c6e73c46f3108bde67dce2dcd125b71b2ffe9 |

### D. qui 接入、qB Profile 与 Sync

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `QUI-01` | SecretStore | BOOT-06,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/qui-01/secret-store-check.json` / sha256:34139968f27c22db82f21057e5f66af9caa55ad9c698f663f2dd082528a6f6b8 |
| `QUI-02` | QuiControlClient | QUI-01, CTR-03 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-02/control-client-check.json` / sha256:ba722d3a9aa7c4cd74c0a6ef883a382ae1cfe4b07862f26608192c3b8515b56a; `docs/evidence/qui-12/live-qb504-integration-summary.json` / sha256:4bd28967aea8907bd619b86fdf1d6e0f3fdf09a5c02cb777a1dd905fc98c9d21 |
| `QUI-03` | QbProxyClient | QUI-01, CTR-03 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-03/proxy-client-check.json` / sha256:05f91ea29b63a3d1c3c71e7464067d2f9f881c81464e412282b6010962750e3a; `docs/evidence/qui-12/live-qb504-integration-summary.json` / sha256:4bd28967aea8907bd619b86fdf1d6e0f3fdf09a5c02cb777a1dd905fc98c9d21; wire-only Add evidence: `docs/evidence/qui-12/live-qb504-add-summary.json` / sha256:548f4b31a77f4671723df0fb494d8b511faff6835319c498b832d4fba49988d0 |
| `QUI-04` | Profile 探测 | QUI-02,03 | IN_PROGRESS | Codex | `ec84241` | `docs/evidence/qui-04/profile-probe-check.json` / sha256:97ba12e8d1309d6a88c634eb6fc5e97208ba5a595750f7d325289d4548ab5be4; exact three-profile facts: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `QUI-05` | AddRequestAdapter | QUI-04, CTR-02 | IN_PROGRESS | Codex | `ec84241` | `docs/evidence/qui-05/add-request-check.json` / sha256:929b9fc6f6fce4cc185d9456c4099f22b80645ee13eee8af733b7f0a838ef09b; paused/stopped live lab semantics: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `QUI-06` | AddResponseAdapter | QUI-04, CTR-02,03 | IN_PROGRESS | Codex | `ec84241` | `docs/evidence/qui-06/add-response-check.json` / sha256:3ebe870cb2ade1d2d8260173981c309d4c303ea0ffb96a3ca88e62dd9b9480db; legacy/json/409 live lab semantics: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `QUI-07` | Sync delta merger | QUI-03,04 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-07/sync-merger-check.json` / sha256:fc2db2dc8cbb5eef288d3f28261ffecad9856644207a88dd014d4d74e5b02e25; `docs/evidence/qui-12/live-qb504-integration-summary.json` / sha256:4bd28967aea8907bd619b86fdf1d6e0f3fdf09a5c02cb777a1dd905fc98c9d21 |
| `QUI-08` | SnapshotStore | DB-04, QUI-07 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-08/snapshot-store-check.json` / sha256:e1d2f2f2d342233e7733bf5f45fb93622a5687e8652ac62ce5672dc1141eff88; `docs/evidence/qui-12/live-qb504-runtime-check.json` / sha256:7b370b7555043a7eb9c85e18c5c33bebf9847c37da8032b1ce59ae12518480f3 |
| `QUI-09` | 单实例 SyncWatcher | QUI-07,08, DB-09 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-09/sync-watcher-check.json` / sha256:ae1785fcf4074bd3994ff1fb72fec3b031aad84e97814837297ea47805fa346a; `docs/evidence/qui-12/live-qb504-integration-summary.json` / sha256:4bd28967aea8907bd619b86fdf1d6e0f3fdf09a5c02cb777a1dd905fc98c9d21; `docs/evidence/qui-12/live-qb504-go-recovery-summary.json` / sha256:f96286c471fb44ffce912c8f5dd1fb55b7b9546593eee683453a326592be9bc1 |
| `QUI-10` | Capability/Health 聚合 | QUI-02,04,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/qui-10/capability-health-check.json` / sha256:8a3cc5966d42c08decd91a1734547af372f86257f60e14aa71f26d9dcb5fa362 |
| `QUI-11` | 协议探针工具 | QUI-03..07, BOOT-09 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/qui-11/protocol-probe-check.json` / sha256:d54c7853dc377d657b6033a5966c7fdde579e6ac49d8d5a3fb265517f3df70e1; `docs/evidence/qui-12/live-qb504-readonly-probe.json` / sha256:39297223c91e68c3117fc6c6e3f0f6038fc6b6fe004eaf68d0f91851908168c5 |
| `QUI-12` | G1 实验室基线 | BOOT-04,08, QUI-11 | IN_PROGRESS | Codex | `a314d42` revalidation PASS / publish controls open | `docs/evidence/qui-12/g1-baseline-check.json` / sha256:2d2ef86e795bfe0b7d510cd317bc319cf05732235b936549c338575314753828; three-profile summary: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e; archived matrix: `docs/evidence/qui-12/g1-three-profile-matrix-summary.json` / sha256:9c8b048e2b437169586ca0ae3c3fe11c51ac93321692d9181f766f60a3bcada6; current-source rerun 2026-09-02: matrix revalidated PASS on `a314d42`; readiness: `docs/evidence/qui-12/g1-lab-readiness-live.json` / sha256:727c532359ad9270c8e79a66645a58350343e49e53f144e31f8eec841ef5c70f |

### E. RSS、Torrent 与并行准备

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `RSS-01` | Site/Feed 应用服务 | DB-03, CTR-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-01/site-feed-service-check.json` / sha256:ab19d16ac0fc873358f30b98a11c59fc3306f3908e0e03801c4a0bab2678ba50 |
| `RSS-02` | Feed 测试命令 | RSS-01, DB-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-02/feed-preview-check.json` / sha256:47cd55337f32b120ce1d6ad689073f045e69c88cf44df77da084b12bee3fe3f4 |
| `RSS-03` | HTTP Fetcher | RSS-01, QUI-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-03/http-fetcher-check.json` / sha256:2e039ced9c13aa72156ffe67af7e783803ba820fdc282b5840461be75e23a63b |
| `RSS-04` | Scheduler | RSS-03, DB-09 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-04/scheduler-check.json` / sha256:175d8b5d41a8ce683b85d90510f79d2ed481c79971ab1cfadecda90372664c1d |
| `RSS-05` | 首次 Baseline | RSS-03,04 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-05/baseline-check.json` / sha256:7627bf756fe72846b845767de32ed0d2ae6818ba137eec0b72643d65c1222ea0 |
| `RSS-06` | Source 去重 | RSS-03, DB-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/rss-06/source-dedupe-check.json` / sha256:6ea89d650f2646fce1bebdaf85b7ce3a05e87a6f287376e128a358a94c2baf34 |
| `TOR-01` | Artifact downloader | RSS-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/tor-01/artifact-downloader-check.json` / sha256:f70fdc73a66d194c4d798e8b127a9461670a34e28593c43495b2f2eea5c43cb6 |
| `TOR-02` | bencode 安全解析 | TOR-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/tor-02/bencode-parser-check.json` / sha256:95725efdd79816adfc7386fc9c9242fe77a83f8cf8423a6d3557826ea02fe5a1 |
| `TOR-03` | Identity 计算 | TOR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/tor-03/identity-check.json` / sha256:7853264771ce04af09491a7f9987fb8abb82cc5eed96762f629c15a4bf587347 |
| `TOR-04` | Source/Identity 关联 | TOR-03, DB-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/tor-04/source-identity-link-check.json` / sha256:2a598d0c903150a9c31337647dba2976f3e3a74ace3507e36dbf8c5b174650b8 |
| `RACE-01` | RaceJob 创建 | RSS-06, DB-06 | IN_PROGRESS | Codex | local-only | `docs/evidence/race-01/racejob-create-check.json` / sha256:78cefca344854ef981e1716420eab0c4c8fdab4c9b832c6cf9a895ec12727b4e |
| `RACE-02` | 并行准备 Supervisor | RACE-01, TOR-01, RULE-03, RULE-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/race-02/supervisor-check.json` / sha256:3e6d4751a85a3715518ea7b6512daf9a0d901714a81b6089830004db9fdb39a2 |
| `RACE-03` | 聚合门槛 | RACE-02, CTR-02 | IN_PROGRESS | Codex | local-only | `docs/evidence/race-03/aggregation-gate-check.json` / sha256:f56296f55f052b0b4c1d2a5ec04ac557047c0a8d7785cbfcabd8bcc0d8874c32 |

### F. 规则引擎与 Query Builder

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `RULE-01` | 条件 AST | CTR-01,02 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-01/condition-ast-check.json` / sha256:6abe244743ddc582ec644b469a35b85612e4b2d8e1c38b60e234d6bdea8c4866 |
| `RULE-02` | 字段与运算符目录 | RULE-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-02/field-operator-catalog-check.json` / sha256:80a98a712bffef5b3bf0241fd3fd22845c354bd6dcdf3a167e925a2dba0bb039 |
| `RULE-03` | 三值 Evaluator | RULE-01,02 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-03/evaluator-check.json` / sha256:3b405e237bf62b1fe8637bd252bd9de71a6c81c2c7e382cc4edd5f937e293a21 |
| `RULE-04` | RuleVersion | RULE-01, DB-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-04/rule-version-check.json` / sha256:191763b4c85b0981b2a22d5ef3ee8ab4288fcc5743e677a7d69c30834449713b |
| `RULE-05` | 多规则聚合 | RULE-03,04 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-05/multi-rule-aggregation-check.json` / sha256:6c39106a8788b6c16dcf6e94eeeb1dd7dfbbb28b625c2f5c53216512c65f68c3 |
| `RULE-06` | Dry-run API | RULE-03,05, CTR-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-06/dry-run-check.json` / sha256:74fe8b53555641e7e1b8b645f8687bd882e3838880b023e46f32d7b00dff3192 |
| `RULE-07` | 导入 qui 组件 | BOOT-04,07 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-07/qui-query-builder-import-check.json` / sha256:bb027d2b2b60725c05d0ce69f7c86cbe5f68cb995666b222deba09327c7a13e6 |
| `RULE-08` | Vertex 字段适配 | RULE-02,07 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-08/vertex-field-adapter-check.json` / sha256:404404acd4810538efda2952e0984a01f3aaaf54f3c791e39f99dfa988db4468 |
| `RULE-09` | 简单/高级模式 | RULE-01,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-09/simple-advanced-mode-check.json` / sha256:2d3e37521070b10f1e3a5f2b1b009a06650877438759cc055980acecb0afb082 |
| `RULE-10` | 排序、启停与复制 | RULE-04,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-10/rule-order-toggle-duplicate-check.json` / sha256:3361e45f914ad94d908d64d230edbe8f1867cd802d1697b17db94927c131377b |
| `RULE-11` | 聚合预览 UI | RULE-05,06,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-11/aggregation-preview-check.json` / sha256:531eda37f13095628fa5314bfb37954a43a1fbbb38d2478f698192acc644ac44 |
| `RULE-12` | 上游差异记录 | RULE-07..11, BOOT-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/rule-12/upstream-diff-check.json` / sha256:060df30420ab73bede78f95df7ab1762ad3554e8a5a52de10a02dfd4b3abc989 |

### G. Placement 与容量账本

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `CAP-01` | StorageTarget 与证据 | DB-05, QUI-10 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/cap-01/storage-target-check.json` / sha256:b614c335c3543990fa7d7104ebc0ad64bdb2784b42b72f34daf90739680d81d1; live operator device/mount verification: `docs/evidence/qui-12/live-qb504-storage-readonly.json` / sha256:19711b22b561db666f0e5ed643d600d81974cbf2198c5d08721224e0b940c716 |
| `CAP-02` | 容量快照 | CAP-01, QUI-09 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/cap-02/capacity-snapshot-check.json` / sha256:cce142eb0e4f136f79820f86c8377c7389716ca96b9464cc82139f27dbc0367b; live physical-free validation: `docs/evidence/qui-12/live-qb504-storage-readonly.json` / sha256:19711b22b561db666f0e5ed643d600d81974cbf2198c5d08721224e0b940c716 |
| `CAP-03` | 容量投影 | CAP-02, DB-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-03/capacity-projection-check.json` / sha256:1024fe9837727d56466930ddc70e5c83c6aa07f6a9f310a3a74871cffd61c432 |
| `CAP-04` | amount_left 证据 | QUI-07, DB-05 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/cap-04/amount-left-check.json` / sha256:d2895cf73ee6aa64704f116a6c5c01cb3050928881f3dbfd169b605e69abb10c; retained live task amount_left known: `docs/evidence/qui-12/live-qb504-go-already-present-transaction.json` / sha256:ff166f517620f9edad06406c90cfd37ac82181312d738624cf97119feafb1e2a |
| `CAP-05` | Task binding owner | CAP-04, DB-05,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-05/task-binding-owner-check.json` / sha256:4e8025ec1d119f1924837689f0b1ef7d5223bd346b41bb45995489ced287d6d7 |
| `CAP-06` | CandidateFilter | CAP-01, QUI-10, RULE-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-06/candidate-filter-check.json` / sha256:1a568c6f5b450268b5ad19b301981c21f7bda4abf86ecbf9034186ff4fd1f248 |
| `CAP-07` | AVAILABLE 判定 | CAP-02,03,06 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-07/available-check.json` / sha256:ad2cfc0a7943e81114b473a321db8e2be5ce2bb8b69fb9df21105dbe729eb8d9 |
| `CAP-08` | Reservation 事务 | CAP-03,05,07, DB-06,09 | IN_PROGRESS | Codex | live-identity/isolated-db | `docs/evidence/cap-08/reservation-transaction-check.json` / sha256:199bca81550fc5cd43004562b1bca091ca0e3b38fe0295448d7b6101ef573986; live Identity ReserveAtomic → AlreadyPresent: `docs/evidence/qui-12/live-qb504-go-recovery-summary.json` / sha256:f96286c471fb44ffce912c8f5dd1fb55b7b9546593eee683453a326592be9bc1 |
| `CAP-09` | Final recheck | CAP-08, RACE-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-09/final-recheck-check.json` / sha256:5e49cbde898054f867b0e1ccabc01e61cac49cce519adb3b8c0d131c3ac139af |
| `CAP-10` | Placement 评分 | CAP-06,07 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-10/placement-score-check.json` / sha256:c053bfb23ac324f9d980c744dd4ae09893160a65962a3e36139df293c5c51581 |
| `CAP-11` | 完成终态释放 | CAP-04,05,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-11/terminal-release-check.json` / sha256:805389bd31fd7f567a32eae97f11105c6432ae83a09d2b536afe8c869390e634 |
| `CAP-12` | 权威删除终态 | CAP-04,05,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-12/authoritative-removal-check.json` / sha256:5ad0129fd2b795426092d68f2e1935cb51dc8124603b73d23c910897769c4def |
| `CAP-13` | Capacity reconciler | CAP-01..12, QUI-08,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-13/capacity-reconciler-check.json` / sha256:3af47a2ead08fbb1d2f5a7592982c019b86ebbe50b0fbdf68364cd8472602786 |
| `CAP-14` | G2 oracle 工具 | CAP-01..13, DB-10 | IN_PROGRESS | Codex | local-only | `docs/evidence/cap-14/g2-oracle-check.json` / sha256:ec98de064becad57a34ad1a95c11db6eff8aae6956f961d92053d6281973f053 |

### H. Add、确认与恢复

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `ADD-01` | RaceJob StateMachine | CTR-02, DB-06 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-01/racejob-state-machine-check.json` / sha256:ced98f127c652707aeaedbf82ead15915b784c3667002641c978733074c4aeb5 |
| `ADD-02` | Attempt StateMachine | ADD-01, DB-06,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-02/attempt-state-machine-check.json` / sha256:ebd123d9901b3ea5a0d579f1ef71eecb62c757a8455390e0547ca6d12ccfc94c |
| `ADD-03` | SubmitIntent | ADD-02, CAP-08 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-03/submit-intent-check.json` / sha256:9daf11cb19caabc02e342eb61051cec6c0ad79b7dec7ca65d3244af0bbcb4f0c |
| `ADD-04` | Sync waiter | ADD-03, QUI-09, DB-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-04/sync-waiter-check.json` / sha256:055055d2ab2abd32069ad395d0638fe1233dbd11acec7ef178d82475ba7a0420 |
| `ADD-05` | Dispatch worker | ADD-03,04, QUI-05 | IN_PROGRESS | Codex | `0de553c` / `ec84241` | `docs/evidence/add-05/dispatch-worker-check.json` / sha256:a80ca917a77416c42ec3c3fe2144d97c30dfb43299bfb55ccdd7710b6dc5a119; pre-send durable commit: `docs/evidence/add-05/dispatch-commit-check.json` / sha256:ad9e3ab8a18157fb94937e53c323f99ff4026da77e15a452773b696164030304; three-profile archived dispatch: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `ADD-06` | 响应事实分类 | ADD-05, QUI-06 | IN_PROGRESS | Codex | `0de553c` / `ec84241` | `docs/evidence/add-06/response-facts-check.json` / sha256:eb960b662e736a4398de78994eb66d208070c3467794c72a61dae3b6391114e9; archived legacy/json/transport facts: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `ADD-07` | IdentityMatcher | TOR-03, QUI-07 | IN_PROGRESS | Codex | `ec84241` | `docs/evidence/add-07/identity-matcher-check.json` / sha256:329def9fd3bd572ef844450f3e5f2a0f8d91566ef3ae9a11ff9cc8c065e010ca; three-profile Add→Sync Identity: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `ADD-08` | 首次确认原子事务 | ADD-01..07, CAP-05,08 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-08/first-confirm-transaction-check.json` / sha256:049ef9afe9bdcc85bd376465c6f6e0d979bac23c19483c1a78df781ca51561ec |
| `ADD-09` | Already-present 分支 | ADD-03,07, CAP-05 | IN_PROGRESS | Codex | live-read-only | `docs/evidence/add-09/already-present-check.json` / sha256:c03c153b881df079cb29961f3ceb1f70d717b058a50f62a04cee9b43104728f2; live Identity + ReserveAtomic/AlreadyPresent isolated DB transaction: `docs/evidence/qui-12/live-qb504-go-recovery-summary.json` / sha256:f96286c471fb44ffce912c8f5dd1fb55b7b9546593eee683453a326592be9bc1 |
| `ADD-10` | UNKNOWN reconciler | ADD-06..08, DB-09 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-10/unknown-reconciler-check.json` / sha256:5858835125d84c0d129f64ab8b9d7233810bb1f03f57ae5134a24e3a4153d8b5 |
| `ADD-11` | EXPIRED_UNKNOWN 命令 | ADD-08, ADD-10, CTR-01, CTR-03 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-11/expired-unknown-commands-check.json` / sha256:85b67014361f1119ee91549432a40b96a1d6879d949e0db2ac4de359d351e6ea |
| `ADD-12` | 两阶段安全重试 | ADD-01,02,10, CAP-09 | IN_PROGRESS | Codex | local-only | `docs/evidence/add-12/safe-retry-check.json` / sha256:51f13958f5e414e2b090b711504deb3c7d15b7cafd45165e99208f86ed7b1704 |
| `ADD-13` | 启动恢复 | ADD-03..12, CAP-13 | IN_PROGRESS | Codex | `0de553c` / `ec84241` | `docs/evidence/add-13/startup-recovery-check.json` / sha256:62ff4bd32c1f2303a33d0856db134a7036d5cee7b7a90baa2796cd895ce58703; interrupted ADD_SENT→UNKNOWN/no replay: `docs/evidence/add-05/dispatch-commit-check.json` / sha256:ad9e3ab8a18157fb94937e53c323f99ff4026da77e15a452773b696164030304; pinned qui restart/already-present: `docs/evidence/qui-12/g1-three-profile-summary.json` / sha256:a45e3a5c817acb1e7379e5f53c58dba806f8b74390356815a30affa17413840e |
| `ADD-14` | 故障矩阵 | ADD-01..13, CAP-14 | IN_PROGRESS | Codex | `a314d42` current rerun PASS / kill -9 + P0×3 open | local matrix: `docs/evidence/add-14/fault-matrix-check.json` / sha256:6e29e4c62025219d244fb5f1f304f99666bbe3f46b9e1ac9231e78c390660765; archived 3×6 matrix: `docs/evidence/qui-12/g1-three-profile-matrix-summary.json` / sha256:9c8b048e2b437169586ca0ae3c3fe11c51ac93321692d9181f766f60a3bcada6; current blocker: `docs/evidence/qui-12/g1-current-revalidation-blocked.json` / sha256:ddee119973fa361b3109a239d3d33e1cbee4b4684619321277875a926e14fab7 |

### I. API、认证、命令与 SSE

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `API-01` | HTTP 基础中间件 | BOOT-06, CTR-01,03 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-01/http-middleware-check.json` / sha256:80ba62f623ffd635aa920c6bf8d684b0d106770f7bef2fab0528d5b77a892ec3 |
| `API-02` | 单管理员认证 | API-01, DB-09 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-02/admin-auth-check.json` / sha256:8e2b8be8c7a9557de4f563cdf70ccc2f22ac277ffc90cc629031a54fa47042ac |
| `API-03` | Web 防护 | API-01,02 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-03/web-protection-check.json` / sha256:4dbdfa6e64364f41b0b2d0ea387193debe73f3d76f7b8fbe4f5ff383a45a68a5 |
| `API-04` | 资源 API | API-01..03, QUI-10, RSS-01, RULE-04, ADD-11, HIS-01, STAT-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-04/resources-api-check.json` / sha256:dcfdd06d59ef25537eb72207ef6e007e69cf43e1fbe99d4ac801d70014c7d0e3 |
| `API-05` | Revision 与 cursor | API-04, DB-09 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-05/paging-revision-check.json` / sha256:704557ff549804738cbe6f18e5caaa2e85e6d5794fdba710cc444668f3413306 |
| `API-06` | POST 幂等 | API-04, DB-07,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-06/idempotency-check.json` / sha256:211f9f8af513ed9b88e9e0557ea4d7ddc38f05ae7182340f9da82e0e47d3047d |
| `API-07` | 异步命令 | API-04,06, DB-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-06/idempotency-check.json` / sha256:211f9f8af513ed9b88e9e0557ea4d7ddc38f05ae7182340f9da82e0e47d3047d |
| `API-08` | 领域事件原子写入 | DB-07,09, CTR-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-08/domain-events-check.json` / sha256:1c510a55f4b8cd4df0409b7a71917b47d4bf1129b5d8adcb66febd5b386061b9 |
| `API-09` | SSE 服务 | API-08, CTR-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-09/sse-server-check.json` / sha256:b7703cc3ed0bd3e198a8873b229d930941632bc81dd637b4fc214a72fa729e61 |
| `API-10` | 实时瞬时数据 | API-09, STAT-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-10/realtime-cards-check.json` / sha256:cbfd77f2c8941dcedc734bbf4d518863ca1d42315e9b85b0b9f7688fe2867498 |
| `API-11` | Data quality API | API-04, QUI-10, CAP-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-11/data-quality-check.json` / sha256:78696350fa5b57629a38fe098f68264ae2234dc4388b7dd03c57c083fc2e6cf0 |
| `API-12` | 生成 TS Client | CTR-06, API-04..11 | IN_PROGRESS | Codex | local-only | `docs/evidence/api-12/ts-client-check.json` / sha256:2d1de2d02183f3a9355f607a0362c7688f94c2232ce281e3eadfa3b654d5df4c |

### J. 首版前端页面

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `UI-01` | App Shell | BOOT-07, API-02,12 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-01/app-shell-check.json` / sha256:a11a01d088c42b448600aadf76f66d61f08eb8eb2612d58ba3b34c0eb8555c52 |
| `UI-02` | Dashboard | UI-01, API-10,11 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-02/dashboard-check.json` / sha256:e422aca39a47a9e3abf32a2085d1a104150a69d6575eb0f9d01ebd3dc2766a84 |
| `UI-03` | Sites & RSS | UI-01, RSS-01,02, API-04 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-03/sites-rss-check.json` / sha256:cedddfb7aea705f1ef68c141ed93269b48c56c6a8460c8a11cd5e4c496ff465a |
| `UI-04` | Rules 列表 | UI-01, RULE-04,10 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-04/rules-list-check.json` / sha256:7ac2efca80f6a8c205ead9a8798491631381ab44b2660da885a607568fba29e9 |
| `UI-05` | Rule Editor | UI-04, RULE-08,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-05/rule-editor-check.json` / sha256:2803ad6103498d6f479f229b19c8c3671d30a46a60bcd94f6d5b040e3c160746 |
| `UI-06` | Dry-run/聚合预览 | UI-05, RULE-06,11 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-06/dry-run-aggregation-check.json` / sha256:28aa3b55206e8546fe9694ee2b2c6af04db80e21fcbed234e94071133c44ff32 |
| `UI-07` | History | UI-01, HIS-01, API-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-07/history-check.json` / sha256:d17c3f8db713c2f703338e0466ec4d0ae11aa7d574acfc19e5169b35581fbff9 |
| `UI-08` | History Detail | UI-07, HIS-02,03 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-08/history-detail-check.json` / sha256:d8726eddd414106020d1c17878cb33cb99e71fb7077f6cbed2e8a4b556806aff |
| `UI-09` | Downloader Detail | UI-02, STAT-01..05 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-09/downloader-detail-check.json` / sha256:aa0e77694157b119f05828a2bc65aa23065047db93505ff3dfe20f2d05493894 |
| `UI-10` | Exceptions | UI-01, ADD-10,11, API-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/ui-10/exceptions-check.json` / sha256:c9a357deb58f01ae6af5f686bbbbf36003d6e85f26be4947a05031b9698816a0 |
| `UI-11` | Settings | UI-01, API-04 | PENDING | — | — | — |
| `UI-12` | Accessibility/性能 | UI-01..11 | PENDING | — | — | — |
| `UI-13` | 主流程 E2E | UI-01..12, CTR-08 | PENDING | — | — | — |

### K. 历史、统计与可观测性

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `HIS-01` | History projection | ADD-01,02, CTR-05 | IN_PROGRESS | Codex | local-only | `docs/evidence/his-01/history-projection-check.json` / sha256:06df026d3a6af73d1a3941949385b91fd3b3abdd1b75c94520d0e6488892639a |
| `HIS-02` | 删除事实持久化 | HIS-01, CAP-12 | IN_PROGRESS | Codex | local-only | `docs/evidence/his-02/history-deletion-check.json` / sha256:a1d8386bf22721f9c74d5c90d99fe5e657045bf8a4fbb31f48bd9be8e7d61642 |
| `HIS-03` | 详情时间线 | HIS-01, API-08 | PENDING | — | — | — |
| `STAT-01` | StatsSampler | QUI-02,07,09 | IN_PROGRESS | Codex | local-only | `docs/evidence/stat-01/check.json` / sha256:2f14738aa29f8b95eb9f806fd9078148a8781a4f514c5e837d127cad176bebdb |
| `STAT-02` | Counter delta | STAT-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/stat-02/check.json` / sha256:917169788a1033e07b6224d51ff1634707e41826b93a1317f246e3039e0a9607 |
| `STAT-03` | 小时桶 | STAT-02, DB-07 | IN_PROGRESS | Codex | local-only | `docs/evidence/stat-03/check.json` / sha256:faeef2b06c6a0c4bfcc52102a7486998575cfaec00480d1510d9616d9a0804d7 |
| `STAT-04` | 汇总查询 | STAT-03, DB-09, CTR-01 | IN_PROGRESS | Codex | local-only | `docs/evidence/stat-04/check.json` / sha256:fca79b77b0ec46218930f9654fdfe980d00754b2971959a050d96a4c56ba6a97 |
| `STAT-05` | 实时卡片样本 | STAT-01, CTR-01 | PENDING | — | — | — |
| `OBS-01` | 结构化日志 | BOOT-09 | PENDING | — | — | — |
| `OBS-02` | Metrics/health | QUI-10, CAP-07, ADD-10 | PENDING | — | — | — |
| `OBS-03` | 保留与清理 | DB-07, QUI-08, STAT-03 | PENDING | — | — | — |
| `OBS-04` | 诊断包 | OBS-01..03, QUI-11 | PENDING | — | — | — |

### L. 安全、性能与运维加固

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `HARD-01` | Secret/隐私扫描 | BOOT-03,09, OBS-04 | PENDING | — | — | — |
| `HARD-02` | 依赖与许可证 | BOOT-02..04, RULE-12 | PENDING | — | — | — |
| `HARD-03` | API 安全 | API-01..07 | PENDING | — | — | — |
| `HARD-04` | SSRF 与出站边界 | RSS-03, TOR-01, QUI-03 | PENDING | — | — | — |
| `HARD-05` | Parser fuzz | TOR-02, QUI-07, RULE-01 | PENDING | — | — | — |
| `HARD-06` | 并发加固 | QUI-09, RACE-02, CAP-08, ADD-08 | PENDING | — | — | — |
| `HARD-07` | 性能基线 | UI-13, STAT-05, HARD-06 | PENDING | — | — | — |
| `HARD-08` | 备份恢复 | DB-11, ADD-13, OBS-03 | PENDING | — | — | — |
| `HARD-09` | 升级回滚 | DB-11, HARD-08 | PENDING | — | — | — |
| `HARD-10` | 运维命令 | BOOT-06, DB-10, OBS-04 | PENDING | — | — | — |
| `HARD-11` | 数据保留 | OBS-03, HARD-08 | PENDING | — | — | — |
| `HARD-12` | 发布镜像 | BOOT-08, HARD-01..11 | PENDING | — | — | — |

### M. RC、证据与冻结

| ID | 内容 | Depends on | Status | Owner | PR/Merge | Evidence |
|---|---|---|---|---|---|---|
| `REL-01` | G1 最终执行 | QUI-12, ADD-14, HARD-12 | PENDING | — | — | — |
| `REL-02` | G2 最终执行 | CAP-14, ADD-14, HARD-08 | PENDING | — | — | — |
| `REL-03` | C1 最终执行 | CTR-02..08, DB-11, API-12 | PENDING | — | — | — |
| `REL-04` | A24 追踪 | ADD-14, CAP-14, UI-13, OBS-04, HARD-12, REL-01..03 | PENDING | — | — | — |
| `REL-05` | 可重复构建 | HARD-02,12 | PENDING | — | — | — |
| `REL-06` | RC soak | REL-01..05 | PENDING | — | — | — |
| `REL-07` | G3 规范切换 | REL-01..06 | PENDING | — | — | — |

## 4. Gate 记录

| Gate | Status | Required evidence | Commit | Reviewer | Notes |
|---|---|---|---|---|---|
| Gate A | PENDING | 可重建骨架、工具链、许可证、单镜像 | — | — | — |
| Gate B | PENDING | 合同源、生成链、C1 不伪 PASS | — | — | — |
| Gate C | PENDING | fresh/upgrade schema、触发器和事务负测 | — | — | — |
| Gate D | PENDING | 三 Profile fixture 确定性 Sync；未验证 fail closed | — | — | — |
| Gate E | PENDING | RSS/Identity/并行准备乱序与恢复 | — | — | — |
| Gate F | PENDING | 简单/高级规则、dry-run、许可追踪 | — | — | — |
| Gate G | PENDING | 并发容量、Reflection、G2 oracle | — | — | — |
| Gate H | PENDING | Add/UNKNOWN/首次绑定/恢复故障矩阵 | — | — | — |
| Gate I | PENDING | OpenAPI、幂等、SSE 与安全 | — | — | — |
| Gate J | PENDING | Dashboard/RSS/Rules/History 用户闭环 | — | — | — |
| Gate K | PENDING | 历史、删除事实、统计口径、诊断 | — | — | — |
| Gate L | PENDING | 安全、性能、备份、升级与镜像 | — | — | — |
| G1 | PENDING | 固定 qui + qB 4.3.8/5.0.4/5.2.2 真实链路 | — | — | 核心与归档 3×6 矩阵 PASS；2026-09-02 在 `a314d42` 完成当前源码重跑 18/18 PASS，并新增三 Profile 外部 kill -9 用例 3/3 PASS（durable marker、UNKNOWN 提升、禁重放、对账确认均有证据）；golden fixtures 已归档、harness 固化为 pinned 容器镜像、clean-commit P0×3 三连跑全 PASS 并持久化 9 条 VERIFIED runs + activation bindings；G1 阻塞已全部闭合，Gate 待正式评审确认 |
| G2 | PENDING | 容量与恢复故障验证 | — | — | — |
| C1 | PENDING | `make verify-contracts` 机器报告 | — | — | — |
| A24 | PENDING | RC8 第 24 节逐项 PASS | — | — | — |
| G3 | PENDING | 前四项 PASS 后单一规范切换 | — | — | — |

## 5. 单项详细记录模板

### BOOT-01 单仓库和目录合同

- Status: `DONE`
- Owner: Codex
- Branch: `main`
- Started commit: `ec684501e57bf7d300dcfc8a368901c47a7b14ab`
- Merge commit: `f741663a5f27820915c8161201acfb3831bd7369`
- Depends on: —
- Test commands:
  - `powershell -NoProfile -ExecutionPolicy Bypass -File tools/contractcheck/check-repository-layout.ps1`
  - `powershell -NoProfile -ExecutionPolicy Bypass -File tools/contractcheck/check-boot-01-readiness.ps1`
- Evidence:
  - `docs/evidence/boot-01/layout-check.json` / `sha256:588d2fa5a8654dacc796a8d7dc90e8743ea7724b04ac35343af5a7475362c37e`
  - `docs/evidence/boot-01/readiness-check.json` / `sha256:a431ac74ad91ab93527629d868b9cc8e911262cd17c08563518e381d9cab931a`
- Updated at: `2026-09-02T07:02:59Z`

### ADD-05 / ADD-13 Dispatch durable boundary 与启动恢复

- Status: `IN_PROGRESS`
- Owner: Codex
- Branch: `main`
- Implementation commit: `0de553ca20876ef1532f63c819febd4d4e25a08f`
- Lab probe commit: `ec84241a5539a0f3b5b5b9b01db08046e30ccfe2`
- Depends on: `ADD-03,04`, `QUI-05`, `CAP-13`
- Transaction boundary:
  - 发送前：本地校验完成后，`dispatchcommit.Begin` 在 SQLite immediate transaction 中写入 Intent `STARTED`、Attempt/Job `ADD_SENT`、`POSSIBLE_EFFECT` 和唯一启动事件。
  - 网络：`DispatchWorker` 只接受已建 durable marker 的事实，单进程内每个 Attempt 最多调用一次 sender。
  - 发送后：`dispatchcommit.Record` 独立原子写入 `response_fact_at`，进入 `AWAITING_CONFIRMATION`、`UNKNOWN` 或 `ADD_FAILED`。
- Failure/idempotency behavior:
  - 发送前校验失败不创建不确定链。
  - 发送后、Record 前崩溃由启动恢复提升 `ADD_SENT -> UNKNOWN`，禁止同一 Attempt 自动重放。
  - 明确 not-sent 关闭为 `ADD_FAILED/FAILED_NO_EFFECT`；若要重试必须两阶段关闭旧链并创建新 Attempt。
  - Begin 并发竞争只有一个调用方成功；Record 重放不重复事件。
- Test commands:
  - `go test ./internal/dispatchcommit ./internal/dispatchflow ./internal/dispatchworker ./internal/startuprecover ./internal/app -count=1`
  - `powershell -NoProfile -ExecutionPolicy Bypass -File tools/dev/preflight.ps1`
- Evidence:
  - `docs/evidence/add-05/dispatch-commit-check.json` / `sha256:ad9e3ab8a18157fb94937e53c323f99ff4026da77e15a452773b696164030304`
  - `docs/evidence/qui-12/g1-three-profile-matrix-summary.json` / `sha256:9c8b048e2b437169586ca0ae3c3fe11c51ac93321692d9181f766f60a3bcada6`
- Blockers: clean-commit P0×3 正式证据、golden fixtures、固定 harness image 与生产调度入口尚未完成；外部 kill -9 已由三 Profile lab `external_kill` 用例覆盖。
- Updated at: `2026-09-02T08:06:00Z`

复杂实施项在本节追加记录，不覆盖清单行：

```markdown
### <ID> <title>

- Status:
- Owner:
- Branch:
- Started commit:
- PR:
- Merge commit:
- Depends on:
- RC8 refs:
- A24 refs:
- Inputs:
- Outputs:
- Transaction boundary:
- Failure/idempotency behavior:
- Test commands:
- Evidence:
  - path:
  - digest: sha256:<64-hex>
- Blocker/ADR:
- Updated at: <UTC-RFC3339>
```

## 6. 变更日志

| Date | Change | Author |
|---|---|---|
| 2026-08-25 | 按施工手册建立 149 项初始清单，全部 PENDING | Specification |
| 2026-08-30 | 从归档中的干净 UTF-8 基线恢复状态表，回填 149 项状态并重算证据摘要；校准构建与生成链结果，正式 Gate 仍停在 BOOT-01，功能开发触达 UI-10 | Codex |
| 2026-08-30 | 完成 UI-03 Sites & RSS 的 local-only 页面、响应式布局与浏览器交互验收；新增可复验证据，状态保持 IN_PROGRESS | Codex |
| 2026-08-30 | 补齐 UI-04 Rules 列表必需字段、确定性排序、复制语义和 revision 409 恢复路径；新增可复验证据，状态保持 IN_PROGRESS | Codex |
| 2026-08-30 | 完成 UI-05 Rule Editor 的共享条件 AST、嵌套编辑、目标互斥、受控动作参数和本地不可变 revision；新增可复验证据，状态保持 IN_PROGRESS | Codex |
| 2026-08-30 | 刷新 15 份历史证据中因共享源码持续演进而过期的 28 个输出摘要，并同步状态表证据 SHA-256 | Codex |
| 2026-08-30 | 完成 UI-06 local-only Dry-run/聚合预览工作流，覆盖全部规则与条件原因、UNKNOWN fail-closed、Winner 和最终受控参数 | Codex |
| 2026-08-30 | 将证据校验扩展到任意 path/sha256 对，刷新 25 份历史证据中的 42 个输入/输出摘要并同步状态引用 | Codex |
| 2026-08-31 | 完成 UI-08 local-only History Detail：最后状态与传输数据、规则聚合、删除确认、用户语言时间线及折叠高级诊断；新增响应式浏览器验收与证据 | Codex |
| 2026-08-31 | 完成 RSS -> Rule local-only 纵向切片的 Feed 轮询周期持久化、POST /api/v1/feeds、Sites Site -> Feed 两阶段提交与 Feed/CSRF/重复资源回归；刷新受影响 evidence 和镜像验收结果，官方状态保持 0/149 DONE | Codex |
| 2026-09-01 | 接通真实 qui -> qB 5.0.4 只读链路，修正 X-API-Key、数字实例 ID、纯文本版本响应与 Sync adapter，加入可选只读 runtime 和脱敏探针；只读阶段任务集合保持不变 | Codex |
| 2026-09-01 | 经用户明确授权，通过 qui Proxy 新增并保留一个无 Tracker、1 字节、停止状态测试任务，完成 legacy Add 响应、POSSIBLE_EFFECT 分类与 Sync Identity 匹配；原有任务未修改或删除，因生产编排/G1 未闭合 Profile 仍为 EXPECTED，官方状态保持 0/149 DONE | Codex |
| 2026-09-01 | 复用已保留的停止状态测试任务完成真实 pre-add PRESENT 只读复核；重复 Sync 保持同一 Identity 集合并得出 SKIP_DISPATCH，不再新增外部任务 | Codex |
| 2026-09-01 | 生产 Go 客户端经 sandbox loopback bridge 完成真实 already-present 不 dispatch 和双启动恢复验证；仅 GET、任务集合不变。确认当前 qui Proxy 对 RID follow-up 仍返回 full_update，真实增量 delta 保持未验证 | Codex |
| 2026-09-01 | 真实 qB 默认保存路径和 physical free 只读采集通过；管理员提供 Linux mount/device 证据且 managed root 采用 qB 默认路径，Storage 验证提升为 VERIFIED。QB_504 Profile 仍为 EXPECTED，因此 Placement 和 Profile 提升继续 fail closed | Codex |
| 2026-09-01 | 盘点本机 G1 实验室：Docker/Compose 可用，精确 qB 5.0.4 镜像已存在；registry/release 大文件下载受阻，先记录实验室准备阻塞 | Codex |
| 2026-09-01 | 从上游 v1.0.0 源码本地构建 qui 隔离镜像，与精确 qB 5.0.4 组成内部网络；生产 Go DispatchWorker Add→stoppedDL 通过，并完成 duplicate/invalid/Tracker/transfer-info/增量删除生命周期后清空 lab。因 qui commit 非冻结 lock 且故障矩阵未全，Profile 保持 EXPECTED | Codex |
| 2026-09-01 | 将 Makefile/preflight 的 integration、e2e、protocol、fault 主动失败占位入口替换为实际 Go 测试套件；完整 preflight 含 11 个目标全部 PASS，发布级外部 G1 仍单独受控 | Codex |
| 2026-09-01 | 获取并校验精确 qB 4.3.8 与 5.2.2 静态发行二进制，从冻结 commit 构建 qui；三 Profile 完成生产 Go inactive Add、核心协议生命周期、增量删除与 qui 重启 Already-present，最终 lab 清空并移除。剩余故障/事务用例未全，Profile 保持 EXPECTED | Codex |
| 2026-09-01 | 新增 dispatchcommit/dispatchflow 与 startuprecover 装配；后续由 `0de553c` 修正为发送前 durable marker、发送后响应事实提交、ADD_SENT 崩溃提升 UNKNOWN，旧 Attempt 永不回退重放 | Codex |
| 2026-09-02 | 配置真实 GitHub CODEOWNER，基于 `ec68450` 刷新目录与 readiness 证据并由 `f741663` 收口 BOOT-01；Gate A 仍等待 BOOT-02～09 独立完成 | Codex |
| 2026-09-02 | `0de553c` 修复 Dispatch 发送前未持久化 STARTED 的崩溃窗口，并补齐并发、发送前/后、提交后与启动恢复不可重放测试 | Codex |
| 2026-09-02 | `ec84241` 固化三 Profile compose/探针并归档 18/18 事务故障报告；因当前代码重跑被 Docker daemon 阻塞及发布控制未齐，Profile 继续 EXPECTED、G1 继续 PENDING | Codex |
| 2026-09-02 | `a314d42` 落地 profile verification runs 持久化与构建身份；Docker 恢复后同源码重跑 3×6 矩阵 18/18 PASS，G1 阻塞收敛为 kill -9、P0×3、固定 harness 与 clean-commit 证据 | Codex |
| 2026-09-02 | `qutransactionprobe` 新增 `external_kill` 故障模式并在三 Profile lab 全 PASS（真实子进程发送后强杀、恢复 UNKNOWN、禁重放、对账 RECONCILED_CONFIRMED）；矩阵汇总扩展为 21 用例 | Codex |
| 2026-09-02 | `qutransactionprobe` 新增 `fixture_capture` 模式，归档三 Profile 共 12 份 Add/Sync/tracker/transfer-info 金样本并逐文件校验 SHA-256；剩余清单收敛为 harness 固定与 P0×3 | Codex |
| 2026-09-02 | harness 固化为 `vertex-pro-g1/harness:pinned` 容器镜像（vcs 身份内嵌、non-root 只读根文件系统），21 用例 + 3 fixture 采集容器化全 PASS；剩余清单收敛为 clean-commit P0×3 | Codex |
| 2026-09-02 | clean-commit P0×3 三连跑全 PASS；9 条 verification runs 持久化（3 Profile × 3 repetition 全 VERIFIED）并产出 activation binding，G1 阻塞全部闭合 | Codex |
| 2026-09-03 | 新增 `profileverify.ListRuns` 与 `tools/profileverification -replay-from`：campaign 数据库 runs 可幂等重放进生产数据库（重复重放 0 改写），重放后 `profileverify.Apply` 将 Profile 判定为 VERIFIED，生产装配 `ProfileVerificationBinding` 链路就此打通 | Codex |
| 2026-09-03 | 新增 `internal/dispatchorchestrator`：把 Placement 判定（fail closed，含 PROFILE_NOT_VERIFIED 拒绝）、ReserveAtomic、sync waiter 注册、dispatchflow 恰好一次发送编排为单一生产入口，Prepare 钩子承接上游 race job/容量参考行创建；端到端测试覆盖 VERIFIED 全链与 EXPECTED 拒绝（0 网络调用、0 预留行） | Codex |
| 2026-09-03 | 补齐链路前半段的生产持久化：`internal/racejobstore`（race_jobs 表 DB Store，重启后创建去重/身份冲突保持）与 `internal/rsssourcedb`（新迁移 `0012_rss_sources.sql`，RSS 来源去重重启存续、字段更新识别）；全量测试通过 | Codex |
| 2026-09-03 | 新增 `internal/matchpipeline`：deduped 来源 → 启用规则三值评估（UNKNOWN 永不匹配、fail closed）→ ruleaggregate 聚合 → race job 创建/复用（racejobstore 持久化）；并完成 RSS→Dispatch 全链端到端测试（规则匹配建链 → Prepare 推进 job 至 READY/PREPARED → 编排器恰好一次派发 → job_state=AWAITING_CONFIRMATION、恰好一次网络发送） | Codex |
