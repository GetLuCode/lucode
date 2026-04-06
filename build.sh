#!/usr/bin/env bash
set -euo pipefail

GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"
BUILD_FLAGS="-ldflags=-s -w"

echo "==> Building web (npm run build)..."
cd web
npm run build
cd ..

echo "==> Building lucode (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build $BUILD_FLAGS -o lucode ./cmd/lucode

echo "==> Building lucodehook (CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH)..."
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build $BUILD_FLAGS -o lucodehook ./cmd/lucodehook

echo ""
echo "Done!"
echo "  ./lucode      — server (embeds web UI)"
echo "  ./lucodehook  — Claude hook (place next to lucode)"
echo ""
echo "Usage: ./lucode [--port PORT] [--data DIR] [--working-dir DIR] [--claude-config DIR]"
echo "  lucode auto-registers lucodehook in --claude-config/settings.json on first run."
