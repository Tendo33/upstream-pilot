#!/usr/bin/env bash
# Discover a running Sub2API container and write a read-only database URL into .env.
# Manual override: set SUB2API_CONTAINER, or fill PILOT_SUB2API_DATABASE_URL yourself.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${ENV_FILE:-$root/.env}"
mode="docker"
force=0
print_only=0

usage() {
  printf '%s\n' "Usage: scripts/detect-sub2api.sh [--for docker|host] [--force] [--print]" \
    "  --for docker  Rewrite hosts for Pilot running in Compose (default)" \
    "  --for host    Rewrite hosts for a binary on the Docker host" \
    "  --force       Overwrite a non-empty PILOT_SUB2API_DATABASE_URL" \
    "  --print       Print values without writing .env"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --for) mode="${2:-}"; shift 2 ;;
    --for=*) mode="${1#--for=}"; shift ;;
    --force) force=1; shift ;;
    --print) print_only=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 1 ;;
  esac
done
if [[ "$mode" != docker && "$mode" != host ]]; then
  printf 'Unknown --for value: %s\n' "$mode" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  for candidate in /usr/local/bin/docker /opt/homebrew/bin/docker /Applications/Docker.app/Contents/Resources/bin/docker; do
    if [[ -x "$candidate" ]]; then
      PATH="$(dirname "$candidate"):$PATH"
      break
    fi
  done
fi
command -v docker >/dev/null 2>&1 || { printf '需要 Docker 才能自动探测 Sub2API 数据库\n' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { printf '需要 python3 才能写入数据库连接串\n' >&2; exit 1; }

inspect_env() {
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$1" 2>/dev/null | awk -F= -v k="$2" '$1==k{print substr($0, index($0,"=")+1); exit}'
}

inspect_label() {
  docker inspect -f "{{ index .Config.Labels \"$2\" }}" "$1" 2>/dev/null || true
}

inspect_networks() {
  docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{println $k}}{{end}}' "$1" 2>/dev/null || true
}

container_name() {
  docker inspect -f '{{.Name}}' "$1" 2>/dev/null | sed 's#^/##'
}

published_port() {
  docker port "$1" 5432 2>/dev/null | awk -F: 'NF{print $NF; exit}'
}

is_app_container() {
  local name="$1" image="$2"
  case "$image" in
    *postgres*|*redis*|*caddy*|*nginx*) return 1 ;;
  esac
  case "$image" in
    *sub2api*|weishaw/sub2api*) return 0 ;;
  esac
  case "$name" in
    sub2api|sub2api-app|sub2api-backend) return 0 ;;
  esac
  return 1
}

detect_container() {
  if [[ -n "${SUB2API_CONTAINER:-}" ]]; then
    docker inspect "${SUB2API_CONTAINER}" >/dev/null 2>&1 || { printf '找不到容器 %s\n' "$SUB2API_CONTAINER" >&2; return 1; }
    printf '%s\n' "$SUB2API_CONTAINER"
    return 0
  fi
  local id name image matches=()
  while read -r id; do
    [[ -z "$id" ]] && continue
    name="$(container_name "$id")"
    image="$(docker inspect -f '{{.Config.Image}}' "$id")"
    if is_app_container "$name" "$image"; then
      matches+=("$id")
    fi
  done < <(docker ps -q)
  if [[ ${#matches[@]} -eq 0 ]]; then
    printf '找不到运行中的 Sub2API 容器。可设置 SUB2API_CONTAINER=<容器名>，或手动填写 PILOT_SUB2API_DATABASE_URL。\n' >&2
    return 1
  fi
  if [[ ${#matches[@]} -gt 1 ]]; then
    printf '找到多个 Sub2API 容器，请设置 SUB2API_CONTAINER 指定一个：\n' >&2
    local cid
    for cid in "${matches[@]}"; do
      printf '  %s (%s)\n' "$(container_name "$cid")" "$(docker inspect -f '{{.Config.Image}}' "$cid")" >&2
    done
    return 1
  fi
  printf '%s\n' "${matches[0]}"
}

choose_network() {
  local container="$1" mode_net
  mode_net="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$container")"
  if [[ "$mode_net" == host ]]; then
    printf '\n'
    return 0
  fi
  local preferred="" other=""
  while read -r net; do
    [[ -z "$net" || "$net" == bridge || "$net" == host || "$net" == none ]] && continue
    case "$net" in
      *sub2api*) preferred="$net" ;;
      *) [[ -z "$other" ]] && other="$net" ;;
    esac
  done < <(inspect_networks "$container")
  printf '%s\n' "${preferred:-$other}"
}

postgres_on_network() {
  local network="$1" cid name image
  while read -r cid; do
    [[ -z "$cid" ]] && continue
    name="$(container_name "$cid")"
    image="$(docker inspect -f '{{.Config.Image}}' "$cid")"
    case "$image" in
      *postgres*) printf '%s\n' "$name"; return 0 ;;
    esac
    case "$name" in
      *postgres|*postgres-*|*-postgres) printf '%s\n' "$name"; return 0 ;;
    esac
  done < <(docker ps -q --filter "network=$network")
  return 1
}

build_dsn_from_fields() {
  python3 - "$1" "$2" "$3" "$4" "$5" "$6" <<'PY'
import sys, urllib.parse
user, password, host, port, dbname, sslmode = sys.argv[1:]
user = urllib.parse.quote(user, safe="")
password = urllib.parse.quote(password, safe="")
host = host or "127.0.0.1"
port = port or "5432"
dbname = dbname or "sub2api"
sslmode = sslmode or "disable"
print(f"postgres://{user}:{password}@{host}:{port}/{dbname}?sslmode={sslmode}")
PY
}

rewrite_dsn() {
  python3 - "$1" "$2" "$3" "$4" "$5" <<'PY'
import sys, urllib.parse
dsn, mode, container_host, published_port, docker_host = sys.argv[1:]
parsed = urllib.parse.urlparse(dsn)
if parsed.scheme not in ("postgres", "postgresql"):
    sys.exit("Sub2API 数据库地址不是 PostgreSQL URL")
hostname = parsed.hostname or ""
port = parsed.port or 5432
if mode == "docker":
    if hostname in {"127.0.0.1", "localhost", "::1"}:
        hostname = docker_host or "host.docker.internal"
    elif container_host:
        hostname = container_host
elif mode == "host":
    if hostname in {"127.0.0.1", "localhost", "::1", "host.docker.internal"}:
        hostname = "127.0.0.1"
    elif published_port:
        hostname = "127.0.0.1"
        port = int(published_port)
    else:
        sys.exit("Sub2API PostgreSQL 没有发布到宿主机，请用 Docker Compose 启动 Pilot，或把库端口映射出来")
user = urllib.parse.quote(parsed.username or "", safe="")
password = urllib.parse.quote(parsed.password or "", safe="")
dbname = (parsed.path or "/sub2api").lstrip("/")
query = parsed.query or "sslmode=disable"
print(f"postgres://{user}:{password}@{hostname}:{port}/{dbname}?{query}")
PY
}

upsert_env() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import pathlib, sys
path, key, value, force = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] == "1"
p = pathlib.Path(path)
lines = p.read_text().splitlines() if p.exists() else []
out, found = [], False
for line in lines:
    if line.startswith(key + "="):
        found = True
        current = line[len(key) + 1 :]
        if current.strip() and not force:
            out.append(line)
        else:
            out.append(f"{key}={value}")
    else:
        out.append(line)
if not found:
    if lines and lines[-1] != "":
        out.append("")
    out.append(f"{key}={value}")
p.write_text("\n".join(out) + "\n")
PY
}

current_value() {
  python3 - "$1" "$2" <<'PY'
import pathlib, sys
path, key = sys.argv[1], sys.argv[2]
p = pathlib.Path(path)
if not p.exists():
    raise SystemExit
for line in p.read_text().splitlines():
    if line.startswith(key + "="):
        print(line[len(key) + 1 :])
        break
PY
}

container="$(detect_container)"
name="$(container_name "$container")"
network="$(choose_network "$container")"
network_mode="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$container")"

raw_dsn="$(inspect_env "$container" DATABASE_URL)"
[[ -z "$raw_dsn" ]] && raw_dsn="$(inspect_env "$container" SQL_DSN)"
host="$(inspect_env "$container" DATABASE_HOST)"
port="$(inspect_env "$container" DATABASE_PORT)"
user="$(inspect_env "$container" DATABASE_USER)"
password="$(inspect_env "$container" DATABASE_PASSWORD)"
dbname="$(inspect_env "$container" DATABASE_DBNAME)"
sslmode="$(inspect_env "$container" DATABASE_SSLMODE)"

db_container=""
if [[ -n "$network" ]]; then
  db_container="$(postgres_on_network "$network" || true)"
fi
if [[ -z "$user" && -n "$db_container" ]]; then
  user="$(inspect_env "$db_container" POSTGRES_USER)"
  password="$(inspect_env "$db_container" POSTGRES_PASSWORD)"
  dbname="$(inspect_env "$db_container" POSTGRES_DB)"
fi
if [[ -z "$raw_dsn" ]]; then
  [[ -n "$user" && -n "$password" ]] || { printf '已找到 Sub2API 容器 %s，但读不到数据库账号。请手动填写 PILOT_SUB2API_DATABASE_URL。\n' "$name" >&2; exit 1; }
  raw_dsn="$(build_dsn_from_fields "$user" "$password" "${host:-postgres}" "${port:-5432}" "${dbname:-sub2api}" "${sslmode:-disable}")"
fi

rewrite_host=""
if [[ "$network_mode" != host ]]; then
  if [[ -n "$db_container" ]]; then
    rewrite_host="$db_container"
  elif [[ -n "$host" && "$host" != postgres && "$host" != db ]]; then
    rewrite_host="$host"
  fi
fi
published=""
if [[ -n "$db_container" ]]; then
  published="$(published_port "$db_container" || true)"
fi
docker_host="host.docker.internal"
dsn="$(rewrite_dsn "$raw_dsn" "$mode" "$rewrite_host" "$published" "$docker_host")"

log_dsn="$(inspect_env "$container" LOG_SQL_DSN)"
[[ -z "$log_dsn" ]] && log_dsn="$(inspect_env "$container" PILOT_SUB2API_LOG_DATABASE_URL)"
rewritten_log=""
if [[ -n "$log_dsn" ]]; then
  rewritten_log="$(rewrite_dsn "$log_dsn" "$mode" "$rewrite_host" "$published" "$docker_host" || true)"
fi

if [[ "$print_only" -eq 1 ]]; then
  printf 'container=%s\n' "$name"
  printf 'network=%s\n' "$network"
  printf 'PILOT_SUB2API_DATABASE_URL=%s\n' "$dsn"
  [[ -n "$rewritten_log" ]] && printf 'PILOT_SUB2API_LOG_DATABASE_URL=%s\n' "$rewritten_log"
  exit 0
fi

existing="$(current_value "$env_file" PILOT_SUB2API_DATABASE_URL 2>/dev/null || true)"
if [[ -n "${existing// }" && "$force" -ne 1 ]]; then
  printf '已保留 .env 中的 PILOT_SUB2API_DATABASE_URL。需要覆盖时加 --force。\n'
else
  upsert_env "$env_file" PILOT_SUB2API_DATABASE_URL "$dsn" "$force"
  printf '已写入 Sub2API 只读库地址（来自容器 %s）\n' "$name"
fi
if [[ -n "$rewritten_log" ]]; then
  upsert_env "$env_file" PILOT_SUB2API_LOG_DATABASE_URL "$rewritten_log" "$force"
fi
if [[ "$mode" == docker && -n "$network" ]]; then
  upsert_env "$env_file" PILOT_SUB2API_NETWORK "$network" 1
  upsert_env "$env_file" COMPOSE_FILE "docker-compose.yml:docker-compose.sub2api.yml" 1
  printf '已加入 Sub2API Docker 网络 %s\n' "$network"
fi
printf '打开控制台后只需添加 Sub2API 管理地址和 API Key。\n'
