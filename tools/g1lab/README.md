# 精确 QB_504 的 GitHub Actions 验证

`g1-profile.yml` 只验证当前生产对应的 qB 5.0.4 / WebAPI 2.11.2 /
libtorrent 1.2.20.0 和 qui commit
`bfe94b5e4d63f213af747fab39719d47aae99d22` 组合。它不会升级或操作生产服务，
通过也不代表三个 Profile 的新版 G1 全部完成。

启动 workflow 前需要把代码固化为干净提交，并提供：

- `qb_binary_url`：上游或本项目 GitHub Release 中的精确静态二进制 URL。
  SHA-256 必须为
  `57f2ce4fe3abcc0c0e8476c4d0fcc00e9657a48fbcfecdad8b4120498060173a`。
  2026-10-23 查询的上游 `release-5.0.4_v1.2.20` 已替换为 revision 4、
  Qt 6.9.0，文件 SHA-256 为 `c38bb5fa…`，与生产不符。
- `qui_image`：`ghcr.io/autobrr/qui@sha256:<digest>` 或 `ghcr.io/ebichuu/qui@sha256:<digest>`，必须是生产使用的
  不可变镜像引用。工作流检查镜像 revision 标签与运行时版本。
- `qb_build_fingerprint`：生产通过只读 `qbprofile.Probe` 得到的完整构建
  指纹。必须与实验室实测完全相同，不能以二进制摘要替代该字段。

runner 从干净 checkout 编译 probe 和 recorder，校验 Go 二进制 VCS 身份。
镜像只在 GitHub Actions 中构建。实验室使用随机 Compose 项目名、内部网络、
空 tmpfs 配置和下载目录；仅 qui 管理端口绑定 runner loopback。probe 只加入
qui 所在的内部网络，没有 qB 网络或任何生产地址、密钥、挂载。

先采集一套真实 golden fixture 并冻结其摘要；随后三轮分别执行完整协议用例和
七种事务/故障模式，包括对 probe 自身子进程的外部 kill。每轮原始报告都保留，
同时引用同一套 golden fixture；不会改写捕获时间或把不同载荷伪装成相同摘要。
成功后 recorder 写入三条不可变 run，并生成 `binding-qb504.json`。

Actions artifact 包含 `campaign.db`、clean-built `profileverification`、
binding、三轮原始报告、golden fixture、构建身份、摘要清单及清理结果。
不得公开上传容器完整日志、Compose 临时配置或实验室密码。失败时不会生成
PASS manifest；部分中间证据只用于诊断。

部署需使用同一提交构建的产品程序，并确认实时 qB 完整构建指纹与 binding
匹配。离线备份后可用归档 recorder 的 `-database <target> -replay-from
<campaign.db>` 导入记录，再配置绑定文件。证据默认 30 天有效，过期须重新验证。
Actions artifact 下载后可能不保留可执行权限，Linux 上需对归档 recorder 执行
`chmod +x profileverification`。存储验证、容量状态和真实自动投递验收仍分别完成。
