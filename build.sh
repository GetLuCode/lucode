#!/usr/bin/env bash
set -euo pipefail

mkdir -p dist

echo "==> Building web (npm run build)..."
(cd web && npm run build)

# ── Helper: build lucodehook for a target, embed it, then build lucode ────────
# Usage: build_platform <GOOS> <GOARCH> <output-name>
build_platform() {
  local goos=$1
  local goarch=$2
  local out=$3

  echo ""
  echo "==> [$goos/$goarch] Building lucodehook..."
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -ldflags "-s -w" -o cmd/lucode/embedded/lucodehook ./cmd/lucodehook

  echo "==> [$goos/$goarch] Building lucode → dist/$out"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -ldflags "-s -w" -o "dist/$out" ./cmd/lucode
}

build_platform linux  amd64 lucode-linux
build_platform darwin amd64 lucode-mac
build_platform windows amd64 lucode.exe

# Clean up the embedded artifact (it's build-time only, not for commits).
rm -f cmd/lucode/embedded/lucodehook

echo ""
echo "────────────────────────────────────────"
echo "  Build complete"
echo "────────────────────────────────────────"
ls -lh dist/
echo ""
echo "  Each binary is self-contained (web UI + hook included)."
echo ""
echo "  First-time setup:"
echo "    ./dist/lucode-linux install"
echo ""
