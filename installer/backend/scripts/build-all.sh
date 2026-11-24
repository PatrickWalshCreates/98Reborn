#!/bin/sh
# Cross-compile w9xm for every supported host. Pure Go, no cgo, so this runs
# from any OS with Go installed. Output: dist/w9xm-<os>-<arch>[.exe]
set -eu
cd "$(dirname "$0")/.."
VERSION="${VERSION:-0.1.0-dev}"
mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 linux/386 windows/amd64 windows/386 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  ext=""; [ "$os" = windows ] && ext=".exe"
  out="dist/w9xm-$os-$arch$ext"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/w9xm
  echo "built $out"
  # The PC scanner only runs on the target PC (Windows or Linux, x86).
  case "$os/$arch" in windows/amd64|windows/386|linux/amd64|linux/386)
    chk="dist/98reborn-check-$os-$arch$ext"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o "$chk" ./cmd/98reborn-check
    echo "built $chk";;
  esac
done
