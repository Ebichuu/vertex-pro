# qui 上游差异记录

## 当前基线

- 上游仓库：`https://github.com/autobrr/qui`
- 固定 commit：`8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896`
- tag：未使用浮动 tag，按 commit 固定
- 上游许可证：`GPL-2.0-or-later`
- 基线捕获时间：`2026-08-24T00:00:00Z`
- Vertex Pro 审查范围：Query Builder 前端组件，不包含 qui Automation 后端和生命周期动作。

## 导入文件清单

| 文件 | 导入路径 | 状态 |
|---|---|---|
| `QueryBuilder.tsx` | `third_party/qui/imported/web/src/components/query-builder/QueryBuilder.tsx` | 原样导入 |
| `ConditionGroup.tsx` | `third_party/qui/imported/web/src/components/query-builder/ConditionGroup.tsx` | 原样导入 |
| `LeafCondition.tsx` | `third_party/qui/imported/web/src/components/query-builder/LeafCondition.tsx` | 原样导入 |
| `FieldCombobox.tsx` | `third_party/qui/imported/web/src/components/query-builder/FieldCombobox.tsx` | 原样导入 |
| `constants.ts` | `third_party/qui/imported/web/src/components/query-builder/constants.ts` | 原样导入 |

未导入文件：无。未删除上游文件：无。

## Vertex Pro 修改记录

| 日期 | 上游基线 | 文件范围 | 修改内容 | 依赖变化 |
|---|---|---|---|---|
| `2026-08-25` | `8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896` | 五个 Query Builder 文件 | 当前导入副本不改动，保留原版权和 SPDX；Vertex 字段适配、规则模型映射和产品接入留在 RULE-08 及后续实现中。 | 无 |

## 许可证与发布审查

- 五个导入文件均保留 `SPDX-License-Identifier: GPL-2.0-or-later`。
- 根目录 `LICENSE` 提供完整 GPL-2.0 文本。
- 根目录和设计文档目录均维护第三方通知。
- 导入副本位于 `third_party/qui/imported/`，未接入生产 Web 路由。
- 当前没有新增或改写上游依赖版本。
- 上游升级必须重新固定 commit、更新 digest、追加本文件记录并重新运行许可证检查。

## 校验入口

- `tools/licensecheck/verify-license.ps1`
- `tools/licensecheck/verify-qui-lock.ps1`
- `docs/evidence/rule-12/upstream-diff-check.json`