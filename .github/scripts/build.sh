#!/usr/bin/env bash
# build.sh — Build Caddy binaries with nacos adapter via xcaddy
# Called by the release workflow before GoReleaser packaging.
set -euo pipefail

VERSION="${1:-v0.0.1}"
PLUGIN_PATH="github.com/Hoverhuang-er/caddy-nacos-adapter@${VERSION}"

# Platforms to build for
PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

echo "==> Installing xcaddy…"
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

mkdir -p dist

for PLATFORM in "${PLATFORMS[@]}"; do
  GOOS="${PLATFORM%%/*}"
  GOARCH="${PLATFORM##*/}"

  EXT=""
  [ "$GOOS" = "windows" ] && EXT=".exe"

  OUTPUT="dist/caddy_${GOOS}_${GOARCH}${EXT}"

  echo "==> Building for ${GOOS}/${GOARCH}…"
  GOOS="$GOOS" GOARCH="$GOARCH" GOEXPERIMENT=jsonv2 CGO_ENABLED=0 \
    xcaddy build \
      --with "${PLUGIN_PATH}" \
      --output "${OUTPUT}" \
      2>&1 | tail -3

  echo "  -> ${OUTPUT}"
done

echo "==> All builds complete."
ls -lh dist/
