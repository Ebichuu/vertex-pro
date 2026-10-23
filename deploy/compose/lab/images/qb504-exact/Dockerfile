# syntax=docker/dockerfile:1.7
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
# Downloaded and SHA-256 verified by the GitHub Actions campaign runner.
COPY --chmod=0555 qbittorrent-nox /usr/local/bin/qbittorrent-nox
ENV HOME=/config
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/qbittorrent-nox","--profile=/config","--webui-port=8080","--confirm-legal-notice"]
