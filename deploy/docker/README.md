# Vertex Pro 容器发布

镜像只通过 GitHub Actions 构建和发布。推送 `v*` tag 或手动运行 `image.yml` 后，
工作流会构建并发布到 GitHub Container Registry；Pull Request 只执行构建验证，
不会推送镜像。当前发布目标为 `linux/amd64`。

工作流先在 runner 临时目录从当前提交重新构建前端，并逐文件比较
`internal/webembed/dist` 中已提交的嵌入资源。二者不一致时发布失败，需要更新、
清理并提交嵌入资源后重跑；前端构建不会修改用于 Go 编译的 checkout。

`tools/release/build-image-binary.sh` 随后在干净 checkout 内编译 Go 程序，显式启用
`-buildvcs=true`，并验证二进制的 `vcs.revision` 等于工作流提交、
`vcs.modified=false`。工作流归档 `go version -m` 输出与二进制 SHA-256。
Dockerfile 只封装这份已验证的程序，构建上下文为 runner 临时输出目录。
Profile 激活仍需同一提交、精确上游构建和未过期的 G1 验证记录。

发布后的镜像由 workflow 写入的 digest 标识。部署时应使用完整镜像引用，例如：

```text
ghcr.io/ebichuu/vertex-pro@sha256:<digest>
```

容器以非 root 用户运行，前端由 Go 二进制嵌入，默认监听 `8080`。`GOPROXY` 在
工作流的二进制构建步骤中配置，可按 CI 网络环境替换。部署时拉取发布镜像即可，
无需本机构建。构建身份检查通过不代表真实投递或 G1 验收已经通过。
