#!/bin/bash
set -e

TARGET="${1:-linux}"  # linux | windows

echo "→ Building web frontend..."
cd web && npm run build && cd ..

echo "→ Copying frontend dist to cmd/desktop/frontend/dist..."
rm -rf cmd/desktop/frontend/dist
mkdir -p cmd/desktop/frontend/dist
cp -r web/dist/. cmd/desktop/frontend/dist/

case "$TARGET" in
  linux)
    echo "→ Building desktop binary for Linux..."
    go build -tags "webkit2_41,production" -o ./tmp/desktop ./cmd/desktop
    echo "✓ Done: ./tmp/desktop"
    ;;
  windows)
    if ! command -v x86_64-w64-mingw32-gcc &>/dev/null; then
      echo "✗ mingw-w64 not found. Install with:"
      echo "    sudo apt install gcc-mingw-w64-x86-64"
      exit 1
    fi
    echo "→ Building desktop binary for Windows..."
    CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
      go build -tags production -o ./tmp/desktop.exe ./cmd/desktop
    echo "✓ Done: ./tmp/desktop.exe"
    ;;
  *)
    echo "Usage: $0 [linux|windows]"
    exit 1
    ;;
esac
