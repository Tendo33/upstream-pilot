#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$task_root"
task_env="${1:-.env}"
if [[ ! -f "$task_env" ]]; then
  printf 'Environment file not found: %s. See README.md.\n' "$task_env" >&2
  exit 1
fi
set -a
source "$task_env"
set +a
exec ./bin/upstream-manager
