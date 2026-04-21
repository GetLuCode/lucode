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
    WIN="CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64"

    echo "→ Building desktop.exe..."
    eval "$WIN go build -tags 'production nativewebview' -o ./tmp/desktop.exe ./cmd/desktop"
    echo "✓ Done: ./tmp/desktop.exe"

    echo "→ Building authapi.exe..."
    eval "$WIN go build -o ./tmp/authapi.exe ./cmd/authapi"
    echo "✓ Done: ./tmp/authapi.exe"

    echo "→ Building aiapi.exe..."
    (cd /home/homebox/projects/aiapi && eval "$WIN go build -o /home/homebox/projects/claude-orchestrator/tmp/aiapi.exe ./cmd/server")
    echo "✓ Done: ./tmp/aiapi.exe"
    ;;
  *)
    echo "Usage: $0 [linux|windows]"
    exit 1
    ;;
esac
