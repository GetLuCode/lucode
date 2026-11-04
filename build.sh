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

  # The runner is its own, smaller binary: no web UI, no HTTP port. It embeds the same
  # approval hook.
  local runner_out="${out/lucode/lucode-runner}"
  mkdir -p cmd/lucode-runner/embedded
  cp cmd/lucode/embedded/lucodehook "cmd/lucode-runner/embedded/lucodehook-$goos"
  echo "==> [$goos/$goarch] Building lucode-runner → dist/$runner_out"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -ldflags "-s -w" -o "dist/$runner_out" ./cmd/lucode-runner
}

build_platform linux  amd64 lucode-linux
build_platform darwin amd64 lucode-mac
build_platform windows amd64 lucode.exe

# Clean up the embedded artifacts (they are build-time only, not for commits).
rm -f cmd/lucode/embedded/lucodehook cmd/lucode-runner/embedded/lucodehook-*

echo ""
echo "────────────────────────────────────────"
echo "  Build complete"
echo "────────────────────────────────────────"
ls -lh dist/
echo ""
echo "  lucode-*         the hub: web UI + API (self-contained, hook included)."
echo "  lucode-runner-*  runs claude for a hub: no UI, no port. Put it on every machine"
echo "                   that runs agents:  lucode-runner --nats-url nats://hub:4222"
echo ""
echo "  First-time setup:"
echo "    ./dist/lucode-linux install"
echo ""
