#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="$ROOT/dist"
OUTPUT="$OUTPUT_DIR/s2am-go-linux-amd64"
CHECKSUM="$OUTPUT.sha256"
MODULE="github.com/langrenjh-alt/S2AM-GO"

command -v go >/dev/null 2>&1 || { echo "error: Go 1.24 or newer is required" >&2; exit 1; }
command -v npm >/dev/null 2>&1 || { echo "error: Node.js and npm are required to build the embedded web UI" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "error: sha256sum is required" >&2; exit 1; }

VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || printf 'dev')}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse --short=12 HEAD 2>/dev/null || printf 'unknown')}"
if [[ -n "${SOURCE_DATE_EPOCH:-}" ]]; then
  BUILD_TIME="$(date -u -d "@${SOURCE_DATE_EPOCH}" '+%Y-%m-%dT%H:%M:%SZ')"
else
  BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
fi

printf '==> Installing locked frontend dependencies\n'
npm --prefix "$ROOT/web" ci
printf '==> Building embedded frontend\n'
npm --prefix "$ROOT/web" run build
printf '==> Running Go tests\n'
(cd "$ROOT" && go test ./...)

mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT" "$CHECKSUM"

LDFLAGS="-s -w -buildid= -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT} -X ${MODULE}/internal/version.BuildTime=${BUILD_TIME}"
printf '==> Building %s (%s, %s)\n' "$(basename "$OUTPUT")" "$VERSION" "$COMMIT"
(
  cd "$ROOT"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUTPUT" ./cmd/s2am-go
)
chmod 0755 "$OUTPUT"
(
  cd "$OUTPUT_DIR"
  sha256sum "$(basename "$OUTPUT")" > "$(basename "$CHECKSUM")"
)

printf '==> Release ready\n%s\n%s\n' "$OUTPUT" "$CHECKSUM"
