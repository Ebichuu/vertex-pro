# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
ARG QUI_COMMIT=8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896
RUN apk add --no-cache git ca-certificates
WORKDIR /src
RUN git clone --filter=blob:none --no-checkout https://github.com/autobrr/qui.git . \
    && git fetch --depth=1 origin ${QUI_COMMIT} \
    && git checkout --detach ${QUI_COMMIT} \
    && test "$(git rev-parse HEAD)" = "${QUI_COMMIT}"
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOPROXY=https://proxy.golang.org,direct
RUN go build -trimpath -ldflags "-s -w -X github.com/autobrr/qui/internal/buildinfo.Version=${QUI_COMMIT}" -o /out/qui ./cmd/qui

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN adduser -D -u 10001 qui
COPY --from=build /out/qui /usr/local/bin/qui
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/qui"]
