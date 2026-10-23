#!/usr/bin/env bash
set -euo pipefail

if [[ "${GITHUB_ACTIONS:-}" != true || "${RUNNER_OS:-}" != Linux ]]; then
  echo 'This release build must run in a Linux GitHub Actions runner.' >&2
  exit 1
fi

root="$(git rev-parse --show-toplevel)"
cd "$root"
revision="$(git rev-parse HEAD)"
if [[ "$revision" != "${GITHUB_SHA:?GITHUB_SHA is required}" ]]; then
  echo '::error::Checkout revision does not match the workflow revision.' >&2
  exit 1
fi

assert_clean() {
  if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
    echo '::error::Release compilation requires a clean checkout, including untracked files.' >&2
    exit 1
  fi
}
assert_clean

# Rebuild the frontend outside the checkout. The committed embed assets are
# part of the Go source identity, so they must exactly match this fresh build;
# never replace them and then hide the resulting dirty state from Go.
web_root="$(mktemp -d "${RUNNER_TEMP:?RUNNER_TEMP is required}/vertex-pro-web.XXXXXX")"
git archive --format=tar HEAD package.json pnpm-lock.yaml pnpm-workspace.yaml web |
  tar -xf - -C "$web_root"
(
  cd "$web_root"
  pnpm install --frozen-lockfile --ignore-scripts
  pnpm --dir web build
)
if ! diff --recursive --brief --no-dereference "$web_root/web/dist" "$root/internal/webembed/dist"; then
  echo '::error::Fresh frontend assets differ from internal/webembed/dist. Refresh and commit the embedded assets before publishing.' >&2
  exit 1
fi
assert_clean

context="$(mktemp -d "$RUNNER_TEMP/vertex-pro-image.XXXXXX")"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -buildvcs=true -trimpath -ldflags='-s -w' \
  -o "$context/vertex-pro" ./cmd/vertex-pro
assert_clean

go version -m "$context/vertex-pro" > "$context/vertex-pro.buildinfo.txt"
for fact in "vcs.revision=$revision" 'vcs.modified=false' 'GOOS=linux' 'GOARCH=amd64' 'CGO_ENABLED=0'; do
  if ! awk -v fact="$fact" '$1 == "build" && $2 == fact { found = 1 } END { exit !found }' "$context/vertex-pro.buildinfo.txt"; then
    echo "::error::Compiled binary is missing required build identity: $fact" >&2
    exit 1
  fi
done
(
  cd "$context"
  sha256sum vertex-pro > SHA256SUMS
)
printf 'context=%s\n' "$context" >> "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
