#!/usr/bin/env bash
# Builds lucode-runner for one or more systems into tmp/runner/ from the working tree:
# no clone, no overlay, nothing to swap (each OS embeds its own hook, see cmd/lucode-runner/hook_*.go).
#   ./build-runner.sh              windows and linux
#   ./build-runner.sh linux        just linux
# An executable that is running cannot be overwritten on Windows: stop it first.
set -euo pipefail

targets=("$@"); [ ${#targets[@]} -eq 0 ] && targets=(windows linux)
mkdir -p tmp/runner cmd/lucode-runner/embedded

for os in "${targets[@]}"; do
  out="tmp/runner/lucode-runner"; [ "$os" = windows ] && out="$out.exe" || out="$out-$os"
  echo "==> [$os] lucodehook"
  CGO_ENABLED=0 GOOS="$os" GOARCH=amd64 go build -ldflags "-s -w" -o "cmd/lucode-runner/embedded/lucodehook-$os" ./cmd/lucodehook
  echo "==> [$os] lucode-runner -> $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH=amd64 go build -o "$out" ./cmd/lucode-runner
done
echo "done:"; ls -la tmp/runner | grep lucode-runner
