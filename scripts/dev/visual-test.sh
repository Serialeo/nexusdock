#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AGENT_ROOT="${VISUAL_AGENTDOCK_REPO:-$ROOT/../agentdock}"
UI_PORT="${VISUAL_UI_PORT:-5173}"
NEXUS_PORT="${VISUAL_NEXUS_PORT:-18777}"
AGENT_PORT="${VISUAL_AGENT_PORT:-18765}"
NODE_NAME="${VISUAL_NODE_NAME:-VisualDevNode}"

INSTANCE_ID="$(printf '%s' "$ROOT" | cksum | awk '{print $1}')"
STATE_DIR="${VISUAL_STATE_DIR:-$ROOT/nexus-data/visual-dev}"
LOG_DIR="$STATE_DIR/logs"
WEB_PID_FILE="$STATE_DIR/vite.pid"

NEXUS_IMAGE="${VISUAL_NEXUS_IMAGE:-nexusdock:visual-dev-$INSTANCE_ID}"
AGENT_IMAGE="${VISUAL_AGENT_IMAGE:-agentdock:visual-dev-$INSTANCE_ID}"
NEXUS_CONTAINER="${VISUAL_NEXUS_CONTAINER:-nexusdock-visual-$INSTANCE_ID}"
AGENT_CONTAINER="${VISUAL_AGENT_CONTAINER:-agentdock-visual-$INSTANCE_ID}"
NEXUS_DATA_VOL="${VISUAL_NEXUS_DATA_VOLUME:-nexusdock-visual-$INSTANCE_ID-data}"
NEXUS_RECALL_VOL="${VISUAL_NEXUS_RECALL_VOLUME:-nexusdock-visual-$INSTANCE_ID-recall}"
AGENT_HOME_VOL="${VISUAL_AGENT_HOME_VOLUME:-nexusdock-visual-$INSTANCE_ID-agent-home}"
AGENT_WORKSPACE_VOL="${VISUAL_AGENT_WORKSPACE_VOLUME:-nexusdock-visual-$INSTANCE_ID-agent-workspace}"

REBUILD_NEXUS=0
REBUILD_AGENT=0

say() {
  printf '[visual-dev] %s\n' "$*"
}

fail() {
  printf '[visual-dev] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

random_hex() {
  python3 -c 'import secrets; print(secrets.token_hex(32))'
}

secret_path() {
  printf '%s/%s' "$STATE_DIR" "$1"
}

read_secret() {
  cat "$(secret_path "$1")"
}

write_secret_if_missing() {
  local name=$1 value=$2 path
  path="$(secret_path "$name")"
  if [[ ! -s "$path" ]]; then
    umask 077
    printf '%s\n' "$value" >"$path"
    chmod 600 "$path"
  fi
}

prepare_state() {
  mkdir -p "$LOG_DIR"
  chmod 700 "$STATE_DIR" "$LOG_DIR"
  write_secret_if_missing nexus-auth-token "$(random_hex)"
  write_secret_if_missing nexus-secret-key "$(random_hex)"
  write_secret_if_missing agent-auth-token "$(random_hex)"
}

check_prerequisites() {
  require_command docker
  require_command npm
  require_command curl
  require_command jq
  require_command python3
  docker info >/dev/null 2>&1 || fail "Docker daemon 不可用"
  [[ -d "$AGENT_ROOT" && -f "$AGENT_ROOT/Dockerfile" ]] || fail "找不到 AgentDock 源码：$AGENT_ROOT"
}

image_exists() {
  docker image inspect "$1" >/dev/null 2>&1
}

build_nexus_image() {
  say "从当前 NexusDock 源码构建测试后端镜像：$NEXUS_IMAGE"
  docker build -t "$NEXUS_IMAGE" "$ROOT"
}

build_agent_image() {
  say "从当前 AgentDock 源码构建测试节点镜像：$AGENT_IMAGE"
  docker build --target runtime -t "$AGENT_IMAGE" "$AGENT_ROOT"
}

ensure_images() {
  if [[ "$REBUILD_NEXUS" == 1 ]] || ! image_exists "$NEXUS_IMAGE"; then
    build_nexus_image
  fi
  if [[ "$REBUILD_AGENT" == 1 ]] || ! image_exists "$AGENT_IMAGE"; then
    build_agent_image
  fi
}

ensure_volumes() {
  docker volume create "$NEXUS_DATA_VOL" >/dev/null
  docker volume create "$NEXUS_RECALL_VOL" >/dev/null
  docker volume create "$AGENT_HOME_VOL" >/dev/null
  docker volume create "$AGENT_WORKSPACE_VOL" >/dev/null
}

container_running() {
  [[ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || true)" == "true" ]]
}

wait_http() {
  local url=$1 attempts=${2:-60}
  local i
  for ((i=0; i<attempts; i++)); do
    if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

start_nexus() {
  if [[ "$REBUILD_NEXUS" == 1 ]]; then
    docker rm -f "$NEXUS_CONTAINER" >/dev/null 2>&1 || true
  fi
  if container_running "$NEXUS_CONTAINER"; then
    return 0
  fi
  docker rm -f "$NEXUS_CONTAINER" >/dev/null 2>&1 || true

  local auth_token secret_key
  auth_token="$(read_secret nexus-auth-token)"
  secret_key="$(read_secret nexus-secret-key)"

  say "启动隔离 NexusDock 后端（仅监听宿主机 127.0.0.1:$NEXUS_PORT）"
  docker run -d \
    --name "$NEXUS_CONTAINER" \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,noexec,nosuid,size=64m,uid=10001,gid=10001,mode=0700 \
    -p "127.0.0.1:$NEXUS_PORT:18777" \
    -v "$NEXUS_DATA_VOL:/var/lib/nexus" \
    -v "$NEXUS_RECALL_VOL:/recall" \
    -e "NEXUS_AUTH_TOKEN=$auth_token" \
    -e "NEXUS_SECRET_KEY=$secret_key" \
    -e NEXUS_REQUIRE_AUTH=true \
    -e NEXUS_AUTH_ALLOW_INSECURE_HTTP=true \
    -e NEXUS_PUBLIC_URL= \
    -e NEXUS_DATA_DIR=/var/lib/nexus \
    -e RECALL_REPO_DIR=/recall \
    "$NEXUS_IMAGE" >/dev/null

  if ! wait_http "http://127.0.0.1:$NEXUS_PORT/health" 90; then
    docker logs "$NEXUS_CONTAINER" >&2 2>&1 || true
    fail "NexusDock 后端未就绪"
  fi
}

pid_alive() {
  local file=$1 pid
  [[ -s "$file" ]] || return 1
  pid="$(cat "$file")"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" >/dev/null 2>&1
}

ensure_web_deps() {
  if [[ ! -x "$ROOT/web/node_modules/.bin/vite" ]]; then
    say "安装前端依赖"
    (cd "$ROOT/web" && npm ci)
  fi
}

start_web() {
  ensure_web_deps
  if pid_alive "$WEB_PID_FILE"; then
    return 0
  fi
  rm -f "$WEB_PID_FILE"

  say "启动 Vite 热更新，监听 0.0.0.0:$UI_PORT（视觉模式免登录）"
  local nexus_token
  nexus_token="$(read_secret nexus-auth-token)"
  (
    cd "$ROOT/web"
    NEXUS_DEV_PROXY_TARGET="http://127.0.0.1:$NEXUS_PORT" \
    NEXUS_VISUAL_BYPASS_AUTH=true \
    NEXUS_DEV_API_TOKEN="$nexus_token" \
    nohup "$ROOT/web/node_modules/.bin/vite" \
      --host 0.0.0.0 \
      --port "$UI_PORT" \
      --strictPort \
      >"$LOG_DIR/vite.log" 2>&1 </dev/null &
    echo $! >"$WEB_PID_FILE"
  )

  if ! wait_http "http://127.0.0.1:$UI_PORT/ui/" 45; then
    tail -n 120 "$LOG_DIR/vite.log" >&2 2>/dev/null || true
    fail "Vite 开发服务器未就绪"
  fi
}

agent_identity_exists() {
  docker run --rm \
    --entrypoint sh \
    -v "$AGENT_HOME_VOL:/home/agentdock/.agentdock" \
    "$AGENT_IMAGE" \
    -c 'test -s /home/agentdock/.agentdock/nexus/device.json' \
    >/dev/null 2>&1
}

nexus_api_post() {
  local path=$1 payload=${2:-'{}'}
  local token
  token="$(read_secret nexus-auth-token)"
  curl -fsS \
    -X POST \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    --data "$payload" \
    "http://127.0.0.1:$NEXUS_PORT$path"
}

nexus_api_get() {
  local path=$1 token
  token="$(read_secret nexus-auth-token)"
  curl -fsS \
    -H "Authorization: Bearer $token" \
    "http://127.0.0.1:$NEXUS_PORT$path"
}

pair_agent_if_needed() {
  agent_identity_exists && return 0

  say "首次创建测试节点，自动执行 AgentDock ↔ NexusDock 配对"
  local pairing_json pairing_code agent_token
  pairing_json="$(nexus_api_post /v1/runtime/nodes/pairing-codes)"
  pairing_code="$(jq -r '.pairing.code // empty' <<<"$pairing_json")"
  [[ -n "$pairing_code" ]] || fail "NexusDock 未返回配对码"
  agent_token="$(read_secret agent-auth-token)"

  docker run --rm \
    --network host \
    -v "$AGENT_HOME_VOL:/home/agentdock/.agentdock" \
    -v "$AGENT_WORKSPACE_VOL:/home/agentdock/AgentDock" \
    -e "AGENTDOCK_AUTH_TOKEN=$agent_token" \
    "$AGENT_IMAGE" \
    agentdock nexus pair \
      --endpoint "http://127.0.0.1:$NEXUS_PORT" \
      --code "$pairing_code" \
      --name "$NODE_NAME" >/dev/null
}

start_agent() {
  if [[ "$REBUILD_AGENT" == 1 ]]; then
    docker rm -f "$AGENT_CONTAINER" >/dev/null 2>&1 || true
  fi
  if container_running "$AGENT_CONTAINER"; then
    return 0
  fi
  docker rm -f "$AGENT_CONTAINER" >/dev/null 2>&1 || true

  local agent_token
  agent_token="$(read_secret agent-auth-token)"

  say "启动真实 AgentDock 测试节点：$NODE_NAME"
  docker run -d \
    --name "$AGENT_CONTAINER" \
    --network host \
    -v "$AGENT_HOME_VOL:/home/agentdock/.agentdock" \
    -v "$AGENT_WORKSPACE_VOL:/home/agentdock/AgentDock" \
    -e "AGENTDOCK_AUTH_TOKEN=$agent_token" \
    -e AGENTDOCK_HOST=127.0.0.1 \
    -e "AGENTDOCK_PORT=$AGENT_PORT" \
    "$AGENT_IMAGE" >/dev/null

  if ! wait_http "http://127.0.0.1:$AGENT_PORT/healthz" 90; then
    docker logs "$AGENT_CONTAINER" >&2 2>&1 || true
    fail "AgentDock 测试节点未就绪；如 $AGENT_PORT 被占用，可设置 VISUAL_AGENT_PORT"
  fi
}

wait_node_online() {
  local i nodes
  for ((i=0; i<90; i++)); do
    nodes="$(nexus_api_get /v1/runtime/nodes 2>/dev/null || true)"
    if [[ -n "$nodes" ]] && jq -e --arg name "$NODE_NAME" '.nodes[]? | select(.name == $name and .online == true)' <<<"$nodes" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

lan_ip() {
  hostname -I 2>/dev/null | awk '{print $1}'
}

print_credentials() {
  say "视觉测试模式无需登录，也没有需要输入的管理员密码。"
}

print_access() {
  local ip
  ip="$(lan_ip)"
  printf '\n'
  say "视觉测试环境已就绪"
  printf '本机:   http://127.0.0.1:%s/ui/\n' "$UI_PORT"
  if [[ -n "$ip" ]]; then
    printf '内网:   http://%s:%s/ui/\n' "$ip" "$UI_PORT"
  fi
  print_credentials
  printf '节点:   %s（真实 AgentDock Bridge）\n' "$NODE_NAME"
  printf '热更新: %s/web/src\n' "$ROOT"
  printf '\n'
  printf '提示：5173 端口绑定到 0.0.0.0，仅应在可信内网使用。后端 18777 仍只绑定宿主机回环地址。\n'
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
    printf '注意：firewalld 正在运行；若其他内网设备无法访问 5173，请由主机管理员显式放行该端口。\n'
  fi
}

up() {
  check_prerequisites
  prepare_state
  ensure_images
  ensure_volumes
  start_nexus
  start_web
  pair_agent_if_needed
  start_agent
  if ! wait_node_online; then
    docker logs "$AGENT_CONTAINER" >&2 2>&1 || true
    fail "AgentDock 已启动，但 NexusDock 控制台中节点未上线"
  fi
  print_access
}

stop_web() {
  if pid_alive "$WEB_PID_FILE"; then
    local pid
    pid="$(cat "$WEB_PID_FILE")"
    kill "$pid" >/dev/null 2>&1 || true
    for _ in {1..20}; do
      kill -0 "$pid" >/dev/null 2>&1 || break
      sleep 0.1
    done
    kill -9 "$pid" >/dev/null 2>&1 || true
  fi
  rm -f "$WEB_PID_FILE"
}

down() {
  stop_web
  docker rm -f "$AGENT_CONTAINER" "$NEXUS_CONTAINER" >/dev/null 2>&1 || true
  say "已停止视觉测试进程；数据卷和配对身份保留"
}

reset_env() {
  down
  docker volume rm \
    "$AGENT_HOME_VOL" \
    "$AGENT_WORKSPACE_VOL" \
    "$NEXUS_DATA_VOL" \
    "$NEXUS_RECALL_VOL" \
    >/dev/null 2>&1 || true
  rm -rf "$STATE_DIR"
  say "已清空视觉测试数据和配对身份；本地镜像保留"
}

status() {
  local ip web_state nexus_state agent_state
  ip="$(lan_ip)"
  web_state="stopped"
  pid_alive "$WEB_PID_FILE" && web_state="running"
  nexus_state="$(docker inspect -f '{{.State.Status}}' "$NEXUS_CONTAINER" 2>/dev/null || printf 'missing')"
  agent_state="$(docker inspect -f '{{.State.Status}}' "$AGENT_CONTAINER" 2>/dev/null || printf 'missing')"
  printf 'Vite:       %s\n' "$web_state"
  printf 'NexusDock:  %s\n' "$nexus_state"
  printf 'AgentDock:  %s\n' "$agent_state"
  printf 'Local UI:   http://127.0.0.1:%s/ui/\n' "$UI_PORT"
  [[ -n "$ip" ]] && printf 'LAN UI:     http://%s:%s/ui/\n' "$ip" "$UI_PORT"
  printf 'Auth:       bypassed (visual-dev only)\n'
}

logs() {
  local which=${1:-all}
  case "$which" in
    nexus)
      docker logs --tail=160 "$NEXUS_CONTAINER"
      ;;
    agent)
      docker logs --tail=160 "$AGENT_CONTAINER"
      ;;
    web|vite)
      tail -n 160 "$LOG_DIR/vite.log"
      ;;
    all)
      printf '===== NexusDock =====\n'
      docker logs --tail=100 "$NEXUS_CONTAINER" 2>&1 || true
      printf '\n===== AgentDock =====\n'
      docker logs --tail=100 "$AGENT_CONTAINER" 2>&1 || true
      printf '\n===== Vite =====\n'
      tail -n 100 "$LOG_DIR/vite.log" 2>/dev/null || true
      ;;
    *)
      fail "logs 仅支持：all|nexus|agent|web"
      ;;
  esac
}

usage() {
  cat <<'EOF'
NexusDock 视觉/UI 内网测试环境

用法:
  ./scripts/dev/visual-test.sh up [--rebuild-nexus] [--rebuild-agent] [--rebuild-all]
  ./scripts/dev/visual-test.sh down
  ./scripts/dev/visual-test.sh restart
  ./scripts/dev/visual-test.sh rebuild
  ./scripts/dev/visual-test.sh status
  ./scripts/dev/visual-test.sh credentials
  ./scripts/dev/visual-test.sh logs [all|nexus|agent|web]
  ./scripts/dev/visual-test.sh reset

说明:
  - UI 使用 Vite HMR，修改 web/src 后浏览器立即刷新，不需要 release 或重新 build。
  - 视觉模式默认免登录；Vite 只向回环 NexusDock 注入内部测试 Token，生产 build 不启用。
  - NexusDock 后端和 AgentDock 节点都从当前本地源码构建成隔离镜像。
  - up 默认只在镜像不存在时构建；后端源码变化时使用 --rebuild-nexus。
  - reset 会删除隔离测试数据和配对身份，但不会删除本地 Docker 镜像。
  - 默认内网入口是 http://<本机IP>:5173/ui/，只建议在可信内网使用。
EOF
}

command_name="${1:-up}"
shift || true

while [[ $# -gt 0 ]]; do
  case "$1" in
    --rebuild-nexus) REBUILD_NEXUS=1 ;;
    --rebuild-agent) REBUILD_AGENT=1 ;;
    --rebuild-all) REBUILD_NEXUS=1; REBUILD_AGENT=1 ;;
    -h|--help) usage; exit 0 ;;
    *) break ;;
  esac
  shift
done

case "$command_name" in
  up)
    up
    ;;
  down)
    down
    ;;
  restart)
    down
    up
    ;;
  rebuild)
    REBUILD_NEXUS=1
    REBUILD_AGENT=1
    up
    ;;
  status)
    status
    ;;
  credentials)
    print_credentials
    ;;
  logs)
    logs "${1:-all}"
    ;;
  reset)
    reset_env
    ;;
  help|-h|--help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
