#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="${PILOT_INSTALL_DIR:-$PWD/upstream-pilot}"
REPO_URL="${PILOT_REPO_URL:-https://github.com/Tendo33/upstream-pilot.git}"

command -v docker >/dev/null 2>&1 || { echo "需要先安装 Docker" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "需要 Docker Compose v2" >&2; exit 1; }

if [[ -d "$PROJECT_DIR/.git" ]]; then
  git -C "$PROJECT_DIR" pull --ff-only
else
  git clone "$REPO_URL" "$PROJECT_DIR"
fi
cd "$PROJECT_DIR"
[[ -f .env ]] || cp .env.docker.example .env

if grep -q '^POSTGRES_PASSWORD=replace-with-' .env; then
  password=$(openssl rand -hex 24)
  if command -v sed >/dev/null 2>&1; then
    sed -i.bak "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$password/" .env
    rm -f .env.bak
  fi
  echo "已生成 PostgreSQL 密码并写入 .env"
fi
if grep -q '^PILOT_MASTER_KEY=replace-with-' .env; then
  key=$(openssl rand -base64 32)
  sed -i.bak "s|^PILOT_MASTER_KEY=.*|PILOT_MASTER_KEY=$key|" .env
  rm -f .env.bak
  echo "已生成 Pilot 主密钥并写入 .env，请备份该文件"
fi

if ./scripts/detect-sub2api.sh --for docker; then
  :
else
  echo "未自动连上 Sub2API 数据库。本机有正在运行的 Sub2API 容器时可再执行 ./scripts/detect-sub2api.sh"
fi

docker compose up -d --build
echo "Upstream Pilot 已启动：${PILOT_PUBLIC_URL:-http://localhost:33777}"
echo "打开控制台创建管理员，然后添加站点：管理地址、API Key 和 Sub2API 数据库。"
echo "查看状态：cd '$PROJECT_DIR' && docker compose ps"
