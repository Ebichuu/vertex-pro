# syntax=docker/dockerfile:1.7

FROM node:24.14.0-bookworm-slim AS web-build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY web web
RUN pnpm --dir web build

FROM golang:1.26.3-bookworm AS go-build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd cmd
COPY internal internal
COPY --from=web-build /src/web/dist internal/webembed/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/vertex-pro ./cmd/vertex-pro

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=go-build /out/vertex-pro /app/vertex-pro
ENV VERTEX_PRO_DATABASE=/tmp/vertex-pro.db
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/vertex-pro"]
CMD ["serve", "--address", ":8080"]
