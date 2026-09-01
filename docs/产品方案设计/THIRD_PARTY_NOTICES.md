# Vertex Pro 第三方软件通知

最后修订：2026-08-25  
项目许可证：`GPL-2.0-or-later`

本文件用于记录 Vertex Pro 直接包含、修改或派生使用的第三方源代码。任何新增或升级必须在合并代码前更新本文件。

## autobrr/qui

- 上游项目：[autobrr/qui](https://github.com/autobrr/qui)
- 上游许可证：`GPL-2.0-or-later`
- Vertex Pro 复用模式：`DIRECT_GPL_REUSE`
- 当前导入状态：五个 Query Builder 文件已原样导入（`IMPORTED_UNMODIFIED`）；Vertex 适配仍待 RULE-08
- 固定上游 commit：`8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896`
- 首次导入日期：`2026-08-25`
- 差异记录：`third_party/qui/UPSTREAM_CHANGELOG.md`

实际导入文件：

```text
third_party/qui/imported/web/src/components/query-builder/QueryBuilder.tsx
third_party/qui/imported/web/src/components/query-builder/ConditionGroup.tsx
third_party/qui/imported/web/src/components/query-builder/LeafCondition.tsx
third_party/qui/imported/web/src/components/query-builder/FieldCombobox.tsx
third_party/qui/imported/web/src/components/query-builder/constants.ts
```

用途：改造为 Vertex Pro RSS 投递前规则编辑器，提供嵌套 AND/OR 条件、字段/运算符编辑、拖拽排序和 dry-run 交互。

默认不复用：

- qui Automation 的删种、暂停、限速、移动、reannounce 和生命周期执行逻辑。
- 与 Vertex Pro RSS 投递无关的 Automation 后端服务。
- 未列入上面清单并未经审查的其他 qui 源文件。

许可证边界：

1. 五个导入文件保留原版权声明和 `SPDX-License-Identifier: GPL-2.0-or-later`。
2. 根目录 `LICENSE` 提供完整 `GPL-2.0-or-later` 许可证文本。
3. 导入副本位于 `third_party/qui/imported/`，当前不接入生产路由。
4. 依赖许可证、构建产物通知和源码发布流程继续由后续 Gate F / HARD-02 审查。

每次导入或升级必须同时更新：

- `third_party/qui/UPSTREAM_LOCK.yaml`
- `third_party/qui/UPSTREAM_CHANGELOG.md`
- `third_party/qui/IMPORTED_QUERY_BUILDER.md`
- 本文件及根目录 `THIRD_PARTY_NOTICES.md`