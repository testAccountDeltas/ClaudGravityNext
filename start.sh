#!/usr/bin/env bash
# Start the gateway from the directory this script lives in.
set -euo pipefail
cd "$(dirname "$0")"

BIN=""
for c in ./gateway ./claudgravitynext-linux-amd64 ./claudgravitynext-darwin-arm64; do
  [ -x "$c" ] && { BIN="$c"; break; }
done
[ -n "$BIN" ] || { echo "No gateway binary found. Build one with: go build -o gateway ./cmd/gateway" >&2; exit 1; }
[ -f config.json ] || { echo "config.json is missing. Copy config.example.json to config.json first." >&2; exit 1; }

exec "$BIN" -config config.json -auths auths -ca-dir ca
