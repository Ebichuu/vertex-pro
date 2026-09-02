# syntax=docker/dockerfile:1.7
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
ARG QB_URL=https://github.com/userdocs/qbittorrent-nox-static/releases/download/release-4.3.8_v1.2.14/x86_64-cmake-qbittorrent-nox
ADD --checksum=sha256:487ac667f188df6a86623410193c8d03f14bff718223a5cd4f0a84a5bfe2a327 ${QB_URL} /usr/local/bin/qbittorrent-nox
RUN chmod 0755 /usr/local/bin/qbittorrent-nox
ENV HOME=/config
ENTRYPOINT ["/usr/local/bin/qbittorrent-nox","--profile=/config","--webui-port=8080"]
