#!/usr/bin/env bash
set -euo pipefail

GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

echo "==> Building web (npm run build)..."
(cd web && npm run build)

echo "==> Building lucode (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags "-s -w" -o lucode ./cmd/lucode

echo "==> Building lucodehook (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags "-s -w" -o lucodehook ./cmd/lucodehook

echo ""
echo "Done!  lucode $(du -sh lucode | cut -f1)   lucodehook $(du -sh lucodehook | cut -f1)"
echo ""
echo "First-time setup (installs to ~/.lucode/):"
echo "  ./lucode install"
echo ""
echo "Run:"
echo "  ~/.lucode/lucode [--port PORT] [--data DIR] [--working-dir DIR]"
