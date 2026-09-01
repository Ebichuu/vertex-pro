# Vertex Pro 第三方软件通知

最后修订：2026-08-25  
项目许可证：`GPL-2.0-or-later`

本文件用于发布和源码分发场景，记录 Vertex Pro 直接包含、修改或派生使用的第三方源代码。任何新增或升级必须在合并代码前更新本文件。

## autobrr/qui Query Builder

- 上游项目：[autobrr/qui](https://github.com/autobrr/qui)
- 上游许可证：`GPL-2.0-or-later`
- 复用模式：`DIRECT_GPL_REUSE`
- 固定 commit：`8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896`
- 首次导入日期：`2026-08-25`
- 当前状态：五个 Query Builder 文件已原样导入，未接入生产 Web 路由。
- 完整差异记录：`third_party/qui/UPSTREAM_CHANGELOG.md`

导入文件：

```text
third_party/qui/imported/web/src/components/query-builder/QueryBuilder.tsx
third_party/qui/imported/web/src/components/query-builder/ConditionGroup.tsx
third_party/qui/imported/web/src/components/query-builder/LeafCondition.tsx
third_party/qui/imported/web/src/components/query-builder/FieldCombobox.tsx
third_party/qui/imported/web/src/components/query-builder/constants.ts
```

边界：

- 保留每个文件原有版权声明与 `SPDX-License-Identifier: GPL-2.0-or-later`。
- Vertex 字段适配和产品接入属于后续 RULE-08，不改写上游导入副本。
- 不复用 qui Automation 的删种、暂停、限速、移动、reannounce 或生命周期执行逻辑。
- 未列入清单的其他 qui 文件不属于默认复用范围。

许可证文本位于根目录 `LICENSE`。升级上游时，必须同步更新锁定 commit、文件 digest、差异记录和设计通知。