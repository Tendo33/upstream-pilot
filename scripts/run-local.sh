#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$task_root"
task_env="${1:-.env}"
if [[ ! -f "$task_env" ]]; then
  printf 'Environment file not found: %s. See README.md.\n' "$task_env" >&2
  exit 1
fi
if [[ -x "$task_root/scripts/detect-sub2api.sh" ]]; then
  ENV_FILE="$task_env" "$task_root/scripts/detect-sub2api.sh" --for host || true
fi
set -a
source "$task_env"
set +a
exec ./bin/upstream-pilot
