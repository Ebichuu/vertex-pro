# Vertex Pro 容器本地验证

```powershell
docker build -f deploy/docker/Dockerfile --build-arg GOPROXY=https://goproxy.cn,direct -t vertex-pro:local .
docker run --rm -p 8080:8080 vertex-pro:local
```

容器以非 root 用户运行，前端由 Go 二进制嵌入，默认监听 `8080`。`GOPROXY` 是构建参数，可按 CI 或网络环境替换；Docker Desktop 可用时，建议先等待镜像构建完成再运行容器。当前验证还覆盖了 `/healthz`、`/readyz`、认证状态、未认证资源 API 和 SSE 边界。
