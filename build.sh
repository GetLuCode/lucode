#!/usr/bin/env bash
set -euo pipefail

GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

echo "==> Building web (npm run build)..."
(cd web && npm run build)

echo "==> Building lucodehook (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags "-s -w" -o cmd/lucode/embedded/lucodehook ./cmd/lucodehook

echo "==> Building lucode with embedded hook and web UI..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags "-s -w" -o lucode ./cmd/lucode

echo ""
echo "Done!  lucode $(du -sh lucode | cut -f1)  (includes web UI + lucodehook)"
echo ""
echo "First-time setup:"
echo "  ./lucode install"
echo ""
echo "Run:"
echo "  ~/.lucode/lucode [--port PORT] [--working-dir DIR]"
