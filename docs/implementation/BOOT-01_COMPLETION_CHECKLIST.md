# BOOT-01 收口清单

状态：`IN_PROGRESS`
更新时间：2026-08-24

## 已完成

- [x] 创建施工手册第 4 节要求的目录骨架。
- [x] 创建根目录 `README.md`。
- [x] 创建根目录 `CODEOWNERS` 文件。
- [x] 创建根目录 `.gitignore`。
- [x] 初始化本地 Git 仓库，当前分支为 `main`。
- [x] 执行 `tools/contractcheck/check-repository-layout.ps1`。
- [x] 路径检查通过，证据为 `docs/evidence/boot-01/layout-check.json`。
- [x] 就绪门禁可重复执行，当前以 `BLOCKED` 退出并指出两个缺失前置条件；证据为 `docs/evidence/boot-01/readiness-check.json`。

## 尚未完成

- [ ] 确定真实 GitHub 用户或团队 owner，并将其写入 `CODEOWNERS`。就绪门禁当前报告：`codeowners_owner_missing`。
- [ ] 创建首个基线 commit，使实施状态具备真实 `started commit` 和可复验 Git 证据。就绪门禁当前报告：`baseline_commit_missing`。
- [ ] 在基线 commit 后重新运行路径检查并更新证据 digest。

## 完成条件

只有以上未完成项全部完成，且 `IMPLEMENTATION_STATUS_V1.md` 中的 owner、branch、started commit、evidence 和更新时间齐全后，才能将 `BOOT-01` 改为 `DONE`。

## 当前 Git 信息

- 分支：`main`
- 当前 HEAD：尚未存在（空仓库）
- 本地 Git 用户名：`Ebichuuuu`
- 本地 Git 邮箱：`chiongsesy@gmail.com`
- 远程仓库：未配置

本清单不把本地 Git 用户名推断为 GitHub owner，避免生成不可验证的 `CODEOWNERS` 规则。



