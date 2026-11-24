#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="dist/release/projectmanager-deploy"
rm -rf dist/release
mkdir -p "$OUT"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o dist/projectmanager .

install -m 755 dist/projectmanager "$OUT/projectmanager"
install -m 755 install.sh "$OUT/install.sh"
cp README.md "$OUT"/
cp config.yaml "$OUT/config.example.yaml"
cp -r deploy docs "$OUT"/

tar -czf dist/projectmanager-linux-amd64.tar.gz -C dist/release projectmanager-deploy
echo "dist/projectmanager-linux-amd64.tar.gz"
