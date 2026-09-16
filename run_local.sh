#!/usr/bin/env bash
# 本地启动沟通室独立服务（端口见 .env 的 PORT，缺省 8093）。
# 依赖环境变量：CHATROOM_DIR / CHATROOM_NOAUTH_WHITELIST / AUTH_JWT_SECRET / ADMIN_USER / ADMIN_PASSWORD。
set -e
cd "$(dirname "$0")"
if [ -f .env ]; then
  set -a; . ./.env; set +a
fi
go build -o agent_chatroom ./...
exec ./agent_chatroom