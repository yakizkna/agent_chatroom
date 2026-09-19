#!/usr/bin/env bash
# agent_chatroom 本地管理脚本：start / stop / restart / status
# 配置**唯一来源 = config.yaml**（模板 config.example.yaml）；另可用 -c <路径> / CHATROOM_CONFIG 指定。
# 端口也来自配置（port:）；这里只是读出来给「按端口找孤儿进程」用。
set -e
cd "$(dirname "$0")"

BIN=agent_chatroom
PIDFILE=agent_chatroom.pid
CONFIG_FILE="${CHATROOM_CONFIG:-config.yaml}"
# 从配置里取 port（只认顶层 `port: <数字>`；取不到时回落 8093）
PORT="$(sed -n 's/^[[:space:]]*port:[[:space:]]*\([0-9]\{1,\}\)[[:space:]]*$/\1/p' "$CONFIG_FILE" 2>/dev/null | head -1)"
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

# 按端口找监听进程（「没有 pidfile 的孤儿实例」兜底用；macOS 用 lsof -t）
port_pids() {
  lsof -nP -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null || true
}

do_start() {
  if is_running; then
    echo "agent_chatroom 已在运行（pid $(cat "$PIDFILE")）"
    return 0
  fi
  # 端口被占但不在 pidfile 里 ⇒ 多半是孤儿实例（历史上手动 nohup 启动、无 pidfile）：
  # 直接说清原因并让用户先 stop，避免「构建成功却静默 bind 失败」这种难查的假象。
  local orphans
  orphans=$(port_pids)
  if [ -n "$orphans" ]; then
    echo "端口 ${PORT} 已被占用（pid: $(echo "$orphans" | tr '\n' ' ')），但不在 ${PIDFILE} 中 ⇒ 无 pidfile 的孤儿实例。" >&2
    echo "请先执行：$0 stop （它会按端口兜底结束该实例）" >&2
    return 1
  fi
  go build -o "$BIN" .
  # 不再 source .env：房间 / 鉴权 / 端口一律来自 config.yaml
  nohup ./"$BIN" -c "$CONFIG_FILE" >agent_chatroom.log 2>&1 &
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
  if is_running; then
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
    return 0
  fi
  # 无 pidfile（或 pidfile 已过期）：按端口兜底 —— 否则「孤儿实例占着端口」会让 restart 报
  # 「未在运行」却杀不掉旧的，紧接着 start 又因 bind 失败而退出（2026-09-17 踩过）。
  local orphans
  orphans=$(port_pids)
  if [ -n "$orphans" ]; then
    echo "未找到 ${PIDFILE}，但端口 ${PORT} 被占用（pid: $(echo "$orphans" | tr '\n' ' ')）⇒ 按端口结束孤儿实例"
    kill $orphans 2>/dev/null || true
    for _ in $(seq 1 20); do
      if [ -z "$(port_pids)" ]; then break; fi
      sleep 0.2
    done
    orphans=$(port_pids)
    if [ -n "$orphans" ]; then
      echo "强制结束 $(echo "$orphans" | tr '\n' ' ')"
      kill -9 $orphans 2>/dev/null || true
    fi
    rm -f "$PIDFILE"
    echo "agent_chatroom 已停止（按端口兜底）"
    return 0
  fi
  rm -f "$PIDFILE"
  echo "agent_chatroom 未在运行"
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
