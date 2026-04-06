#!/usr/bin/env bash
set -euo pipefail

BINARY="lucode"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

echo "==> Building web (npm run build)..."
cd web
npm run build
cd ..

echo "==> Building $BINARY (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags="-s -w" -o "$BINARY" ./cmd/lucode

echo ""
echo "Done! Binary: ./$BINARY"
echo "Run with: ./$BINARY [--port PORT] [--data DIR] [--working-dir DIR]"
