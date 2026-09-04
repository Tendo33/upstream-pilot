#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
task_arch="${TARGET_ARCH:-amd64}"
case "$task_arch" in amd64|arm64) ;; *) echo 'TARGET_ARCH must be amd64 or arm64' >&2; exit 1;; esac
task_version="${RELEASE_VERSION:-0.1.0-dev}"
task_commit="$(git -C "$task_root" rev-parse --short=12 HEAD)"
if [[ -n "$(git -C "$task_root" status --porcelain --untracked-files=normal)" ]]; then task_commit="${task_commit}-dirty"; fi
task_time="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
cd "$task_root"
npm --prefix web ci --prefer-offline --fetch-timeout=20000 --fetch-retries=1
npm --prefix web run build
go test ./...
mkdir -p dist
task_output="dist/upstream-manager-linux-${task_arch}"
CGO_ENABLED=0 GOOS=linux GOARCH="$task_arch" go build -trimpath -ldflags "-s -w -X sub2api-upstream-manager/internal/version.Version=$task_version -X sub2api-upstream-manager/internal/version.Commit=$task_commit -X sub2api-upstream-manager/internal/version.BuildTime=$task_time" -o "$task_output" ./cmd/upstream-manager
if command -v sha256sum >/dev/null; then sha256sum "$task_output" > "$task_output.sha256"; else shasum -a 256 "$task_output" > "$task_output.sha256"; fi
printf 'Built %s\n' "$task_output"
