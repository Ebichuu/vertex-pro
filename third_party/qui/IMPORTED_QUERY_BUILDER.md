# qui Query Builder 导入记录

- 上游仓库：`https://github.com/autobrr/qui`
- 固定 commit：`8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896`
- 上游许可证：`GPL-2.0-or-later`
- 导入日期：`2026-08-25`
- 导入方式：原样导入，不做格式化、依赖改写或 Vertex 业务适配。

导入文件位于 `third_party/qui/imported/web/src/components/query-builder/`。这些文件保留原始版权与 SPDX 标识，RULE-08 再进行 Vertex 字段适配；本项不将上游组件接入生产路由。

## 文件摘要

| 文件 | SHA-256 |
|---|---|
| `QueryBuilder.tsx` | `5f8ed0d085d0c18d631ca924a204d70b93ae7eeec9e5126f5ff2316654c14c7d` |
| `ConditionGroup.tsx` | `66cd28fe616988688bfa9e78ec8b8756cb6e824f424f7f99e123e25610a3ad74` |
| `LeafCondition.tsx` | `8f3ff2f82e43741ec2c12881c381a52d457f6054f16242c3051f147dd91b7cfd` |
| `FieldCombobox.tsx` | `992ba002677c4334c1d86942d4423ccd922bfd9789c9cd2f1ad1826c77eba090` |
| `constants.ts` | `c0b4d755f2fc4c9de6d8f005e2c0b43fb9bb3b438883316005878661fd80c259` |
## RULE-12 差异记录

详细的上游基线、导入范围、Vertex Pro 修改边界和许可证审查记录见 `third_party/qui/UPSTREAM_CHANGELOG.md`。当前五个文件均为原样导入，未接入生产路由；后续字段适配不直接修改该导入副本。