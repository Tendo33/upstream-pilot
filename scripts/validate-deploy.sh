#!/usr/bin/env bash
set -euo pipefail
command -v docker >/dev/null || { echo 'docker is required' >&2; exit 1; }
docker compose config -q
env_file=${PILOT_ENV_FILE:-.env}
[[ -r "$env_file" ]] || { echo "missing $env_file" >&2; exit 1; }
grep -q '^PILOT_MASTER_KEY=' "$env_file" || { echo 'PILOT_MASTER_KEY missing' >&2; exit 1; }
grep -q '^POSTGRES_PASSWORD=' "$env_file" || { echo 'POSTGRES_PASSWORD missing' >&2; exit 1; }
state=${PILOT_STATE_DIR:-./state}
mkdir -p "$state/logs"
test -w "$state" && test -w "$state/logs" || { echo "state is not writable: $state" >&2; exit 1; }
echo 'deployment configuration valid'
