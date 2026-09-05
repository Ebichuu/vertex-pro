# syntax=docker/dockerfile:1.7

FROM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS web-build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY web web
RUN pnpm --dir web build

FROM golang:1.26.3-bookworm@sha256:386d475a660466863d9f8c766fec64d7fdad3edac2c6a05020c09534d71edb4b AS go-build
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

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
WORKDIR /app
COPY --from=go-build /out/vertex-pro /app/vertex-pro
ENV VERTEX_PRO_DATABASE=/tmp/vertex-pro.db
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/vertex-pro"]
CMD ["serve", "--address", ":8080"]
