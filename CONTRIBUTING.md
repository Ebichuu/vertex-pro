# 贡献说明

Vertex Pro V1 以 `GPL-2.0-or-later` 发布。提交代码前请遵守：

- 先阅读 `docs/产品方案设计/VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md`、用户合同和施工手册。
- 不绕过状态机、容量、UNKNOWN、Reservation、Identity 和恢复不变量。
- 新增或升级第三方代码前，先更新根目录 `THIRD_PARTY_NOTICES.md`。
- 复用 qui Query Builder 前，固定上游 tag/commit，保留原版权声明和 SPDX 标识。
- 本地验证优先使用锁定的 Go、Node 和 pnpm 版本。
- 当前阶段只做本地开发，不要求 Git commit 或远程推送。
