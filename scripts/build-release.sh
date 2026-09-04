#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
task_arch="${TARGET_ARCH:-amd64}"
case "$task_arch" in amd64|arm64) ;; *) echo 'TARGET_ARCH must be amd64 or arm64' >&2; exit 1;; esac
task_version="${RELEASE_VERSION:-0.2.0-preview.1}"
task_commit="$(git -C "$task_root" rev-parse --short=12 HEAD)"
if [[ -n "$(git -C "$task_root" status --porcelain --untracked-files=normal)" ]]; then task_commit="${task_commit}-dirty"; fi
task_time="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
cd "$task_root"
npm --prefix web ci --prefer-offline --fetch-timeout=20000 --fetch-retries=1
npm --prefix web run build
go test ./...
node scripts/third-party-licenses.mjs
mkdir -p dist
task_output="dist/upstream-pilot-linux-${task_arch}"
CGO_ENABLED=0 GOOS=linux GOARCH="$task_arch" go build -trimpath -ldflags "-s -w -X github.com/Tendo33/upstream-pilot/internal/version.Version=$task_version -X github.com/Tendo33/upstream-pilot/internal/version.Commit=$task_commit -X github.com/Tendo33/upstream-pilot/internal/version.BuildTime=$task_time" -o "$task_output" ./cmd/upstream-pilot
if command -v sha256sum >/dev/null; then sha256sum "$task_output" > "$task_output.sha256"; else shasum -a 256 "$task_output" > "$task_output.sha256"; fi
task_package="dist/upstream-pilot-linux-${task_arch}.tar.gz"
task_stage="$(mktemp -d)"
trap 'rm -rf "$task_stage"' EXIT
cp "$task_output" "$task_stage/upstream-pilot"
cp LICENSE THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt "$task_stage/"
tar -czf "$task_package" -C "$task_stage" .
if command -v sha256sum >/dev/null; then sha256sum "$task_package" > "$task_package.sha256"; else shasum -a 256 "$task_package" > "$task_package.sha256"; fi
printf 'Built %s and %s\n' "$task_output" "$task_package"
