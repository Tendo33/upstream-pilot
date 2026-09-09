#!/usr/bin/env bash
set -euo pipefail
dir=${PILOT_INSTALL_DIR:-$PWD}; version=${1:?usage: rollback.sh <git-ref>}
cd "$dir"; git fetch --tags origin; git checkout --detach "$version"
./scripts/validate-deploy.sh; docker compose build app; docker compose up -d --no-deps app
curl --retry 30 --retry-delay 2 --fail http://127.0.0.1:33777/readyz >/dev/null
echo "rolled back to $(git rev-parse --short HEAD)"
