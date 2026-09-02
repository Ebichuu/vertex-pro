# syntax=docker/dockerfile:1.7
# The transaction harness binary is built on the host from a clean git
# worktree so the Go toolchain stamps vcs.revision/vcs.modified into the
# executable; the image only wraps that binary in a pinned minimal runtime.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN adduser -D -u 10001 harness
COPY probe /usr/local/bin/probe
COPY probe /usr/local/bin/qutransactionprobe
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/probe"]
