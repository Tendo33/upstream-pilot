#!/usr/bin/env bash
set -euo pipefail

PG_VERSION="${PG_VERSION:-16}"
PG_PORT="${PG_PORT:-55432}"
PG_USER="${PG_USER:-pilottest}"
PG_DATABASE="${PG_DATABASE:-pilot_test}"
ROOT="${PILOT_TEST_PG_ROOT:-$HOME/.local/share/pilot-pg${PG_VERSION}}"
PACKAGE_DIR="$ROOT/packages"
RUNTIME_DIR="$ROOT/runtime"
DATA_DIR="$ROOT/data"
LOG_FILE="$ROOT/postgres.log"

mkdir -p "$PACKAGE_DIR" "$RUNTIME_DIR"
if [[ ! -x "$RUNTIME_DIR/usr/lib/postgresql/$PG_VERSION/bin/postgres" ]]; then
  cd "$PACKAGE_DIR"
  apt download "postgresql-$PG_VERSION" "postgresql-client-$PG_VERSION" postgresql-common libpq5
  for package in ./*.deb; do
    dpkg-deb -x "$package" "$RUNTIME_DIR"
  done
fi

export PATH="$RUNTIME_DIR/usr/lib/postgresql/$PG_VERSION/bin:$PATH"
export LD_LIBRARY_PATH="$RUNTIME_DIR/usr/lib/x86_64-linux-gnu:${LD_LIBRARY_PATH:-}"

if [[ ! -f "$DATA_DIR/PG_VERSION" ]]; then
  mkdir -p "$DATA_DIR"
  initdb -D "$DATA_DIR" -U "$PG_USER" -A trust --encoding=UTF8 --no-locale
fi

if ! pg_ctl status -D "$DATA_DIR" >/dev/null 2>&1; then
  pg_ctl start -D "$DATA_DIR" -l "$LOG_FILE" -o "-p $PG_PORT -h 127.0.0.1 -k $ROOT" -w
fi

if [[ "$(psql -h 127.0.0.1 -p "$PG_PORT" -U "$PG_USER" -d postgres -Atc "SELECT 1 FROM pg_database WHERE datname='$PG_DATABASE'")" != "1" ]]; then
  createdb -h 127.0.0.1 -p "$PG_PORT" -U "$PG_USER" "$PG_DATABASE"
fi

echo "postgres://$PG_USER@127.0.0.1:$PG_PORT/$PG_DATABASE?sslmode=disable"
