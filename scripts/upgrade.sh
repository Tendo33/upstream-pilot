#!/usr/bin/env bash
set -euo pipefail
dir=${PILOT_INSTALL_DIR:-$PWD}; version=${1:?usage: upgrade.sh <git-ref>}
cd "$dir"
./scripts/validate-deploy.sh
stamp=$(date -u +%Y%m%dT%H%M%SZ); mkdir -p .deploy-backups
cp docker-compose.yml ".deploy-backups/compose.$stamp.yml"; cp .env ".deploy-backups/env.$stamp"
git fetch --tags origin; git checkout --detach "$version"
docker compose build app
docker compose up -d --no-deps app
for i in {1..30}; do curl -fsS http://127.0.0.1:33777/readyz >/dev/null && break; sleep 2; done
curl -fsS http://127.0.0.1:33777/healthz >/dev/null
echo "upgraded to $(git rev-parse --short HEAD)"
