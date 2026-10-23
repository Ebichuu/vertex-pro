# syntax=docker/dockerfile:1.7

# GitHub Actions supplies a binary built in its clean checkout, with the real
# Go VCS identity verified by tools/release/build-image-binary.sh. The build
# context is the script's temporary output directory, not the source tree.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
WORKDIR /app
COPY --chmod=0555 vertex-pro /app/vertex-pro
ENV VERTEX_PRO_DATABASE=/tmp/vertex-pro.db
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/vertex-pro"]
CMD ["serve", "--address", ":8080"]
