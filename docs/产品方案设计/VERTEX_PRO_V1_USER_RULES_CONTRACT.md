# Vertex Pro V1 用户与规则合同

文档状态：`PRODUCT-CONTRACT-V1 / REQUIRED-BY-RC8`  
最后修订：2026-08-25  
适用范围：Vertex Pro V1 首版用户体验、RSS 自动化规则和多规则聚合  
Vertex Pro V1 项目许可证：`GPL-2.0-or-later`  
qui 复用模式：`DIRECT_GPL_REUSE`

## 0. 一句话定义

> **Vertex Pro = qui 风格的多 qB 总览 + 原版 Vertex 风格的 RSS 自动刷流 + 删除后仍可查询的历史记录。**

Vertex Pro V1 以 qui 为唯一 qB 接入层。首页用于清晰查看 A、B、C、D、E 等全部 qB 的当前状态和实时数据；RSS 页面提供原版 Vertex 风格的自动投递规则；历史详情保留种子去过哪个下载器、最后状态、删除时间和累计上传数据，即使 qB 中的种子已经被 qui 删除也不会消失。

用户的核心操作应与原版 Vertex 一样直接：

```text
添加站点 RSS
  -> 设置种子大小等筛选条件
  -> 选择 qui 中的目标 qB
  -> 启用规则
  -> Vertex Pro 自动添加到该 qB
```

## 1. 用户首先看到什么

首页优先显示 qui 中已经配置好的全部 qB，例如 A、B、C、D、E。每个实例卡片至少显示：

- 在线、离线或异常。
- 当前下载速度和上传速度。
- 下载中、做种中、暂停和错误任务数。
- 当前可用空间及容量是否可信。
- 今日累计下载与上传。
- 最后成功刷新时间。

点击实例后再进入种子列表和统计详情。首页不显示 Candidate、Placement、Reservation、Reflection、SubmitIntent 或 Capacity Epoch。

## 2. 添加站点和 RSS

用户只配置 PT 站点与 RSS 所需信息。目标 qB 必须从 qui 已登记实例中选择，Vertex Pro 不要求用户再次填写 qB 地址、用户名或密码。

RSS 页面至少提供：

- 站点名称和 RSS 地址。
- 启用/停用。
- 轮询周期。
- 最近成功、最近错误和最近发现数量。
- “测试 RSS”操作，只读取和预览，不添加种子。

## 3. qui-style 自动化规则编辑器

### 3.1 交互目标

规则页采用与 qui Automation 类似的“条件 + 动作”可视化构建方式。用户不需要写 JSON；简单规则几步即可完成，复杂规则可通过嵌套条件组自由表达。

界面必须支持：

- 增加、删除和复制条件。
- 创建嵌套 AND/OR 条件组。
- 拖拽调整条件和规则顺序。
- 根据字段类型切换合适的运算符和输入控件。
- 设置规则名称、启用状态和优先级。
- 复制规则。
- dry-run 最近 RSS 项目。
- 保存时生成不可变规则版本。

首版同时提供简单入口：

```text
RSS / 站点
最小种子大小
最大种子大小
标题包含或排除
目标 qB
```

点击“高级条件”后展开完整 Query Builder；简单入口和高级入口必须读写同一棵条件树，不能维护两套规则引擎。

### 3.2 条件模型

每个叶子条件使用统一结构：

```text
field + operator + value
```

条件组使用：

```text
AND(children...)
OR(children...)
```

V1 字段至少包括：

- 站点、RSS。
- 标题、标题正则、包含词和排除词。
- Torrent 总大小。
- RSS 分类。
- 发布时间、发现延迟。
- 免费、促销或 RSS 标签（站点实际提供时）。
- 发布者、制作组（站点可可靠解析时）。
- 可用 qB Profile、实例标签和 Storage 标签。

V1 运算符至少包括：

- 等于、不等于。
- 包含、不包含。
- 正则匹配。
- 大于、大于等于、小于、小于等于、区间。
- 在集合中、不在集合中。
- 字段存在、字段不存在。

字段缺失属于 `UNKNOWN`，不能自动当作空字符串、false 或 0。规则预览必须说明因字段缺失而未命中。

### 3.3 动作模型

Vertex Pro 的动作发生在 Add 之前，V1 只允许：

- `FIXED_INSTANCE`：选择一台明确的 qB。
- `INSTANCE_GROUP`：选择一个 qB 组，由 Vertex Pro 在该组内选一台可用实例。
- qB 分类。
- Tags。
- 保存路径策略。
- 经过白名单校验的 Add 参数。
- 参与窗口。

如果用户明确选择 `FIXED_INSTANCE=qB-A`，qB-A 离线或空间不足时只能显示等待、过期或失败；系统不能暗中改投 qB-B。

删除、暂停、恢复、移动已下载内容、限速、reannounce 和长期保种属于 qui Automation，不在 Vertex Pro V1 规则动作中重复实现。

## 4. “多规则”必须分成两个概念

### 4.1 一条规则内自由组合

例如：

```text
站点 = M-Team
AND 大小在 20 GiB 到 80 GiB
AND（标题包含 2160p OR 标题包含 1080p）
AND 标题不包含 XXX
```

这仍是一条规则，只产生一次 `MATCHED` 或 `REJECTED`。

### 4.2 同一个 RSS 项命中多条规则

同一 RSS 项可以同时命中多条独立规则，但 V1 对一个 Torrent Identity 只投递一份。聚合固定为：

| 项目 | 处理方式 |
| --- | --- |
| winner | `priority DESC, rule_revision DESC, rule_id ASC` |
| 固定 qB / qB 组 | 取 winner |
| category | 取 winner |
| 保存路径和单值 Add 参数 | 取 winner |
| Tags | 全部命中规则去重并集，系统保留标签不可覆盖 |
| Profile、Storage 等安全约束 | 取交集；交集为空则拒绝投递 |
| 参与窗口 | 取 winner 的版本快照 |

dry-run 和历史详情必须明确展示：

```text
命中：Rule A、Rule B、Rule C
胜出：Rule A
原因：优先级 100
最终下载器：qB-A
最终分类：race
最终标签：vertex-pro, free
```

不得按 map、数据库未排序结果或最后写入顺序决定 winner。

## 5. Dry-run 与安全预览

规则保存前和保存后都可执行 dry-run。dry-run 只读取近期 RSS 历史，不下载或 Add 种子。

每个预览项至少显示：

- 命中或未命中的规则。
- 每个条件的判定和缺失字段。
- 多规则 winner。
- 最终目标 qB。
- 最终 category、Tags、保存路径和 Add 参数摘要。
- 无可用目标、约束冲突或 Profile 未验证等阻止原因。

## 6. RSS 与种子历史

历史列表默认只显示用户关心的摘要：

- 种子名称。
- 来源站点。
- 目标下载器。
- 当前用户状态。

点击详情后才显示命中规则、聚合结果、添加确认时间线、失败原因、累计上传量、分享率、发现/添加时间、删除发现时间和高级诊断。

当 qB 种子被 qui 删除后，历史记录继续保留：

- 最后已知 qB 状态。
- 最后累计上传量和分享率。
- 删除被 Sync 确认的时间。
- `REMOVED_EXTERNALLY` 原因；Vertex Pro 不自动重加。

普通用户状态固定映射为：

```text
等待规则 | 准备种子 | 等待下载器 | 正在添加 | 下载中
做种中 | 已完成 | 已删除 | 失败 | 需确认
```

内部状态只在详情的“高级诊断”中按需出现。

## 7. qui 代码复用合同

参考上游：[autobrr/qui](https://github.com/autobrr/qui)。qui 官方文档把 Automations 定义为基于条件和动作的规则引擎，并支持嵌套 AND/OR、拖拽排序与 dry-run。

建议重点参考或复用：

```text
web/src/components/query-builder/
  QueryBuilder.tsx
  ConditionGroup.tsx
  LeafCondition.tsx
  FieldCombobox.tsx
  constants.ts

web/src/pages/Automations.tsx
internal/models/automation.go
documentation/docs/features/automations.md
```

边界如下：

1. Query Builder 的组件结构、交互和条件树思路可以复用。
2. qui 的 Automation 面向“已经位于 qB 中的种子”；Vertex Pro 面向“RSS 项投递前”。因此字段目录、动作目录和后端执行器必须按 Vertex Pro 语义适配，不能整套照搬。
3. 开发时必须固定一个上游 tag 或 commit，禁止持续从 `develop` 无版本拷贝。
4. 复用前先做依赖清单、组件边界和测试用例映射；不得复制与 Vertex Pro 无关的删种、限速或生命周期代码。

qui 仓库标注为 `GPL-2.0-or-later`，相关源文件也带有该 SPDX 标识。Vertex Pro V1 已选择同一许可证，并将规则编辑器复用方式冻结为 `DIRECT_GPL_REUSE`。

当前状态固定为：**计划复用，尚未导入代码**。在首次导入前不得把计划复用描述为已包含的 qui 源代码。

直接改造必须同时满足：

- 保留上游版权和 SPDX 标识。
- 项目根目录提供完整 `GPL-2.0-or-later` 许可证文本。
- 对外发布时履行适用的源码提供义务。
- 记录 Vertex Pro 所做修改，不把改造代码表述为 qui 原版。
- 首次导入前固定 qui tag/commit；升级必须以明确的上游版本变更执行，不跟随浮动 `develop`。
- 维护规划通知 `docs/产品方案设计/THIRD_PARTY_NOTICES.md`；代码仓库初始化后，同时维护根目录 `THIRD_PARTY_NOTICES.md`，记录上游 URL、固定 commit、复用文件、修改范围和许可证。
- 只默认复用 Query Builder 相关前端组件；新增复用文件必须先进入清单并完成语义、依赖和安全审查。

## 8. V1 验收

以下全部通过，才算用户与规则合同完成：

1. 新用户可在不理解内部状态机的情况下完成“RSS + 大小范围 + 目标 qB”规则。
2. 规则可创建嵌套 AND/OR 条件，并可拖拽、复制和启停。
3. dry-run 不产生任何 Add 副作用，并能解释每个条件结果。
4. 同一 RSS 项命中多规则时，预览、执行和重启恢复得到同一 winner 与最终参数。
5. 固定 qB 不可用时不会静默改投。
6. 首页可同时看清全部 qB 的实时状态和最后刷新时间。
7. qB 种子被 qui 删除后，历史仍保留最后上传量、分享率和删除确认时间。
8. 普通页面不暴露 Candidate、Placement、Reservation、Reflection、SubmitIntent 或 Capacity Epoch。
9. qui Query Builder 直接复用的版本固定、文件清单、许可声明、修改记录和第三方通知检查通过。

## 9. 首版范围控制

“像 qui 一样自由设置”在 V1 指可视化条件树、字段/运算符扩展点、动作配置、排序和 dry-run，不代表把 qui 的全部 Automation 能力复制一遍。

V1 必做：

- RSS/站点、大小、标题、分类和可用站点元数据条件。
- 固定 qB 或 qB 组动作。
- category、Tags、保存路径和白名单 Add 参数。
- 嵌套 AND/OR、拖拽、复制、启停、优先级和 dry-run。
- 多规则确定性 winner 与聚合预览。

V1 不做：

- Vertex Pro 自己执行删种、暂停、限速、移动、reannounce 或保种策略。
- 动态脚本、任意代码执行或第三方插件动作。
- 为了规则自由度建立第二套 qB 管理平台。

这样既保留 qui-style 的自由配置体验，也不会让首版范围失控。
