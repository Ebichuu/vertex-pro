# Vertex Pro V1 实现合同一致性说明

文档状态：`IMPLEMENTATION-NOTE-V1 / REQUIRED-BY-C1`  
编制日期：2026-08-25  
对应主规范：`docs/产品方案设计/VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md`  
对应决策：`docs/产品方案设计/ADR-0001_VERTEX_PRO_V1_SPEC_SUPERSEDE.md`  
仓库规范路径：`docs/产品方案设计/CONTRACT_CONSISTENCY_V1.md`
产品定位与用户规则合同：`docs/产品方案设计/VERTEX_PRO_V1_USER_RULES_CONTRACT.md`
代码施工手册：`docs/实施流程/VERTEX_PRO_V1_CODE_IMPLEMENTATION_PLAYBOOK.md`
实施进度表：`docs/实施流程/IMPLEMENTATION_STATUS_V1.md`

## 1. 目的与边界

本说明冻结 C1 实现合同一致性检查的可执行接口，并补充 PlacementAttempt 目标事实不可变的数据库防线。它不增加产品能力、不改变 RC8 第 26 节不变量，也不能替代 G1、G2 或第 24 节验收。

C1 只回答以下问题：当前 Git commit 中的 SQLite schema、OpenAPI、状态枚举、错误码和关键数据库防线，是否与 RC8 一致且具有可复验的证据。

## 2. Attempt 目标事实不可变

### 2.1 不可变字段

`placement_attempts` 下列字段创建后禁止更新：

```text
attempt_id
job_id
identity_key
attempt_no
instance_id
storage_target_id
allocation_epoch
reserved_bytes
started_at
```

允许 StateMachine 按正式转换更新的字段仅为：

```text
attempt_state
dispatch_state
side_effect_fact
qb_torrent_id
confirmation_sync_generation
confirmation_snapshot_revision
confirmation_evidence_digest
confirmed_at
finished_at
```

Repository 必须使用显式可变列 allowlist。禁止把完整 PlacementAttempt 对象序列化为通用 UPDATE，也禁止在运行期临时关闭触发器。

### 2.2 SQLite 触发器

```sql
CREATE TRIGGER ck_placement_attempt_target_immutable
BEFORE UPDATE OF
  attempt_id,
  job_id,
  identity_key,
  attempt_no,
  instance_id,
  storage_target_id,
  allocation_epoch,
  reserved_bytes,
  started_at
ON placement_attempts
BEGIN
  SELECT RAISE(ABORT, 'ATTEMPT_TARGET_IMMUTABLE');
END;
```

SQLite 的 `UPDATE OF` 在列出现在 SET 子句时触发，即使新旧值相同。因此正常 StateMachine SQL 不得附带任何不可变列。

### 2.3 必需负向测试

每个不可变字段至少执行一次直接 SQL UPDATE，并断言：

- 事务以 SQLite constraint/trigger error 失败；
- 规范化错误包含 `ATTEMPT_TARGET_IMMUTABLE`；
- 行内容、RaceJob current pointer、SubmitIntent、Reservation 和事件 revision 均未变化；
- `PRAGMA foreign_key_check` 仍返回零行。

还必须执行以下正向测试：

- 每个允许字段可通过合法 StateMachine 转换更新；
- Repository 生成的 UPDATE 只包含该转换允许的列；
- Attempt → SubmitIntent → Reservation → RaceJob pointer 的事务仍能完整提交。

### 2.4 Migration 规则

旧表重建或列迁移必须在业务 Worker 停止、备份完成的 migration 中进行。流程固定为：

```text
验证旧数据与 RC8 不变量
-> 创建新表并显式映射列
-> 导入数据
-> 创建不可变触发器及其他索引/触发器
-> 运行一对一、owner 与外键检查
-> 原子提交
```

出现孤儿 Attempt、SubmitIntent 多配、Identity/instance 交叉指向或不可解释目标变化时必须回滚并保留原库，不得自动猜测修复。

## 3. C1 仓库接口

仓库必须提供：

```text
docs/产品方案设计/CONTRACT_CONSISTENCY_V1.md
docs/实施流程/VERTEX_PRO_V1_CODE_IMPLEMENTATION_PLAYBOOK.md
docs/实施流程/IMPLEMENTATION_STATUS_V1.md
tools/contractcheck/contract-inputs.yaml
Makefile target: verify-contracts
Makefile target: verify-implementation-plan
```

`contract-inputs.yaml` 是实现输入清单，至少列出主规范、用户合同、本说明、代码施工手册、实施进度表、supersede ADR、schema migrations、OpenAPI、状态枚举、错误码、事件类型、用户状态映射、生成代码、qui 上游锁定文件、第三方通知和所需测试入口的真实仓库相对路径。改变输入清单必须经过代码评审；工具不得静默扫描并忽略未登记的合同源。

规范执行命令固定为：

```bash
make verify-contracts
```

命令必须：

- 在仓库根目录运行，不访问公网，不读取运行时 Secret；
- 从 `git rev-parse HEAD` 获取完整 commit；
- 检查工作树和暂存区是否干净；
- 使用临时目录生成证据，最后原子发布；
- 除 `artifacts/contract-consistency/` 外不修改任何文件；
- 将所有检查结果写入机器可读报告，不能只打印终端日志。

### 3.1 实施计划检查

```bash
make verify-implementation-plan
```

该命令必须离线、确定性地验证：

- 施工手册中的正式实施项 ID 无重复，集合恰好等于进度表中的实施项集合；初版为 149 项。
- 每项状态只使用 `PENDING/IN_PROGRESS/BLOCKED/IN_REVIEW/DONE`。
- 每个 `DONE` 项都有 merge commit、测试命令、evidence path 和 `sha256:<64-hex>`。
- 每个 `IN_PROGRESS/IN_REVIEW` 项都有 owner、branch 和 started commit。
- 展开 `PREFIX-01..05` 与 `PREFIX-01,03,05` 后所有依赖 ID 均存在，且不存在依赖环；未完成依赖的并行例外带有备注。
- Gate 记录使用 `PENDING/PASS/FAIL/BLOCKED`，G3 只能在 G1/G2/C1/A24 为 PASS 时为 PASS。

该检查必须作为 `make preflight` 和 C1 的已登记输入及执行环境信息写入报告，但不增加或替换第 6 节的十一个 C1 检查 ID。验收项引用必须来自 RC8 第 24 节现有清单，不得引用不存在的编号。

## 4. 固定输出路径

干净 commit 的规范输出为：

```text
artifacts/contract-consistency/{git_commit}/report.json
artifacts/contract-consistency/{git_commit}/SHA256SUMS
```

`{git_commit}` 必须是 `git rev-parse HEAD` 返回的完整小写十六进制 commit，不得使用短 SHA、分支名、`latest` 或时间戳替代。

非干净工作树的开发检查可以输出到：

```text
artifacts/contract-consistency/dev/{run_id}/
```

但必须标记 `eligible_for_g3=false`，永远不能作为 C1 或 G3 证据。

## 5. report.json 合同

### 5.1 编码

- UTF-8，无 BOM，LF 换行，文件末尾一个换行符。
- JSON key 使用工具的固定确定性顺序。
- 时间使用 UTC RFC 3339。
- 所有 digest 字段使用 `sha256:<64 位小写十六进制>`。
- 报告不得包含 Cookie、passkey、Proxy Key、完整 announce URL 或任何运行时 Secret。

### 5.2 最小结构

```json
{
  "schema_version": 1,
  "report_kind": "VERTEX_PRO_CONTRACT_CONSISTENCY",
  "status": "PASS",
  "eligible_for_g3": true,
  "generated_at": "2026-08-25T00:00:00Z",
  "spec": {
    "name": "VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md",
    "status": "DESIGN-RC8 / FROZEN-CANDIDATE",
    "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  },
  "git": {
    "commit": "0000000000000000000000000000000000000000",
    "clean": true
  },
  "tool": {
    "name": "vertex-pro-contractcheck",
    "version": "1.0.0"
  },
  "inputs": [
    {
      "kind": "contract_inputs_manifest",
      "path": "tools/contractcheck/contract-inputs.yaml",
      "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  ],
  "checks": [
    {
      "id": "C1-SCHEMA-FRESH",
      "status": "PASS",
      "evidence_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
      "details": null
    }
  ],
  "waivers": [],
  "summary": {
    "required": 11,
    "passed": 11,
    "failed": 0,
    "skipped": 0
  }
}
```

示例中的零值 digest 和 commit 只表示格式占位，真实报告禁止使用占位值。

### 5.3 状态

```text
status: PASS | FAIL | ERROR
checks[].status: PASS | FAIL | ERROR
eligible_for_g3: true | false
```

`SKIPPED` 不属于必需检查的合法终态。出现无法运行、依赖缺失或解析错误时使用 `ERROR`，不能当作 PASS。

## 6. 必需检查 ID

每个报告必须恰好包含以下检查，不能重命名、跳过或重复：

| ID | 要求 |
|---|---|
| `C1-SCHEMA-FRESH` | 从空库应用全部 migration；SQLite DDL、索引和触发器成功 |
| `C1-SCHEMA-UPGRADE` | 从最近受支持旧 schema 升级，数据与一对一关系保持 |
| `C1-FOREIGN-KEYS` | 每连接 `foreign_keys=ON`，`foreign_key_check` 零行 |
| `C1-ATTEMPT-IMMUTABLE` | 九个不可变字段的直接更新全部被拒绝；允许状态转换通过 |
| `C1-SUBMIT-INTENT-ONE-TO-ONE` | 无孤儿、多配、交叉指向；current Attempt 发布保护通过 |
| `C1-RESERVATION-OWNER` | owner 状态、StorageTarget 和释放顺序触发器正反例通过 |
| `C1-STATE-ENUMS` | Go/DB/OpenAPI/UI 的状态枚举与 RC8 一致 |
| `C1-ERROR-CODES` | DB、Go、OpenAPI 和 UI 错误码集合及含义一致 |
| `C1-OPENAPI` | OpenAPI 3.1 可解析，路径、状态码、schema、幂等合同一致 |
| `C1-EVENT-CONTRACT` | SSE 事件类型、ID、重放/reset 与 REST 资源 revision 一致 |
| `C1-GENERATED-CODE-CLEAN` | 重新生成 schema/OpenAPI 产物后 Git diff 为空 |

若未来增加必需检查，必须更新本 implementation note、报告 `schema_version` 或新增 ADR；工具不能私自减少检查集。

## 7. SHA256SUMS 合同

`SHA256SUMS` 至少包含：

```text
<report.json 的 64 位小写 SHA-256>  report.json
```

格式与常见 `sha256sum` 输出兼容：digest 与相对路径之间两个空格，按路径字典序排序，LF 结尾。该文件自身不递归包含自己的 digest。

验证命令固定为：

```bash
cd artifacts/contract-consistency/{git_commit}
sha256sum -c SHA256SUMS
```

JSON 内的输入/证据 digest 带 `sha256:` 前缀；`SHA256SUMS` 第一列不带前缀。两种格式不得混用。

## 8. 退出码

```text
0  全部必需检查 PASS，报告与 SHA256SUMS 发布成功
1  至少一个合同检查 FAIL，或 Git 工作树不干净
2  工具、配置、输入清单或依赖错误
3  报告生成、原子发布或 digest 校验失败
```

无论退出码为何，工具都应尽可能生成 `status=FAIL/ERROR` 的诊断报告；只有退出码 0 的干净 commit 报告可以成为 G3 证据。

## 9. C1_PASS 判定

```text
C1_PASS =
  process_exit_code == 0
  && report.status == PASS
  && report.eligible_for_g3 == true
  && report.git.clean == true
  && report.git.commit == output_path.git_commit
  && report.spec.name == VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md
  && every_required_check_exists_exactly_once_and_is_PASS
  && report.waivers is empty
  && sha256sum_check == PASS
```

任何未知检查失败、缺少 required check、占位 digest、短 commit、非空 waiver、人工修改报告或 digest 不匹配都必须得到 `C1_FAIL`。

## 10. G3 使用方式

ADR-0001 的实现合同一致性证据必须记录：

```text
report_path
report_sha256
git_commit
tool_version
generated_at
```

执行 G3 前重新运行 `sha256sum -c SHA256SUMS`，并验证 ADR 记录的 commit/digest 与报告一致。C1 通过不能替代 G1、G2 或第 24 节验收：

```text
G3_ALLOWED = G1_PASS && G2_PASS && A24_PASS && C1_PASS
```

## 11. 完成定义

本 implementation note 的实现完成必须满足：

1. 仓库具有固定 Make target 和输入 manifest。
2. Attempt 不可变触发器进入正式 migration。
3. 十一个必需检查及实施计划检查均有自动化实现和正反例。
4. 干净 commit 能生成固定路径的 PASS 报告及有效 SHA256SUMS。
5. 脏工作树、缺失输入、schema 漂移、枚举漂移和任一负向测试失败都会阻止 `C1_PASS`。
6. 报告不包含 Secret，且可由另一台隔离环境复验。

在以上条件完成前，C1 保持未通过，ADR-0001 保持 `PROPOSED`。
