#!/usr/bin/env bash
# agent_chatroom 本地管理脚本：start / stop / restart / status
# 依赖环境变量：CHATROOM_DIR / CHATROOM_NOAUTH_WHITELIST / AUTH_JWT_SECRET / ADMIN_USER / ADMIN_PASSWORD（见 .env）
set -e
cd "$(dirname "$0")"

BIN=agent_chatroom
PIDFILE=agent_chatroom.pid
PORT="${PORT:-8093}"

usage() {
  echo "用法: $0 {start|stop|restart|status}" >&2
  exit 1
}

is_running() {
  [ -f "$PIDFILE" ] || return 1
  local pid
  pid=$(cat "$PIDFILE" 2>/dev/null || true)
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

do_start() {
  if is_running; then
    echo "agent_chatroom 已在运行（pid $(cat "$PIDFILE")）"
    return 0
  fi
  go build -o "$BIN" .
  if [ -f .env ]; then
    set -a; . ./.env; set +a
  fi
  PORT="$PORT" nohup ./"$BIN" >agent_chatroom.log 2>&1 &
  echo $! >"$PIDFILE"
  sleep 1
  if is_running; then
    echo "agent_chatroom 已启动（pid $(cat "$PIDFILE")，端口 ${PORT}，日志 agent_chatroom.log）"
  else
    echo "启动失败，请查看 agent_chatroom.log" >&2
    rm -f "$PIDFILE"
    return 1
  fi
}

do_stop() {
  if ! is_running; then
    echo "agent_chatroom 未在运行"
    rm -f "$PIDFILE"
    return 0
  fi
  local pid
  pid=$(cat "$PIDFILE")
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 20); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.2
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "强制结束 $pid"
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$PIDFILE"
  echo "agent_chatroom 已停止"
}

case "${1:-}" in
  start)   do_start ;;
  stop)    do_stop ;;
  restart) do_stop; do_start ;;
  status)
    if is_running; then
      echo "运行中（pid $(cat "$PIDFILE")，端口 ${PORT}）"
    else
      echo "未运行"
    fi
    ;;
  *) usage ;;
esac
