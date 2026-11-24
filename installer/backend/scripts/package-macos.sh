#!/bin/sh
# Builds "98Reborn Tool.app" (universal) and zips it for release. Runs on
# macOS (codesign and ditto). Output: dist/98Reborn-Tool-macOS.zip
set -eu
cd "$(dirname "$0")/.."
VERSION="${VERSION:-0.1.0-dev}"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o dist/98reborn-tool-darwin-$arch ./cmd/98reborn
done
rm -rf dist/macapp
go run ./cmd/98reborn-macapp --arm64 dist/98reborn-tool-darwin-arm64 --amd64 dist/98reborn-tool-darwin-amd64 \
  --version "$VERSION" --out dist/macapp
# Ad-hoc signature for the whole bundle (not notarized: Gatekeeper asks once).
codesign --force --deep --sign - "dist/macapp/98Reborn Tool.app"
codesign --verify --deep --strict "dist/macapp/98Reborn Tool.app"
rm -f dist/98Reborn-Tool-macOS.zip
ditto -c -k --sequesterRsrc --keepParent "dist/macapp/98Reborn Tool.app" dist/98Reborn-Tool-macOS.zip
echo "built dist/98Reborn-Tool-macOS.zip"
