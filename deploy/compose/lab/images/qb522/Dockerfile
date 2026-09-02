# syntax=docker/dockerfile:1.7
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
ARG QB_URL=https://github.com/userdocs/qbittorrent-nox-static/releases/download/release-5.2.2_v2.0.13/x86_64-qbittorrent-nox
ADD --checksum=sha256:0d219aa16905d75161e5eaee82288358c5d63947ffd48623997fc5dd481664e3 ${QB_URL} /usr/local/bin/qbittorrent-nox
RUN chmod 0755 /usr/local/bin/qbittorrent-nox
ENV HOME=/config
ENTRYPOINT ["/usr/local/bin/qbittorrent-nox","--profile=/config","--webui-port=8080","--confirm-legal-notice"]
