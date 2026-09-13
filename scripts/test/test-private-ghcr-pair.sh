#!/usr/bin/env bash
set -euo pipefail

: "${AGENTDOCK_IMAGE:?set AGENTDOCK_IMAGE to an exact private GHCR image tag or digest}"
: "${NEXUS_IMAGE:?set NEXUS_IMAGE to an exact private GHCR image tag or digest}"

for command_name in docker curl jq python3; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "error: required command not found: $command_name" >&2
    exit 2
  }
done

suffix="r5-$RANDOM-$$"
nexus_container="nexus-${suffix}"
agent_container="agent-${suffix}"
nexus_data="nexus-data-${suffix}"
nexus_recall="nexus-recall-${suffix}"
agent_home="agent-home-${suffix}"
agent_workspace="agent-workspace-${suffix}"
node_name="R5ContainerSmoke-${suffix}"
nexus_token="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
nexus_secret="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
agent_token="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"

free_port() {
  python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
}

nexus_port="$(free_port)"
agent_port="$(free_port)"
while [[ "$agent_port" == "$nexus_port" ]]; do
  agent_port="$(free_port)"
done

cleanup() {
  docker rm -f "$agent_container" "$nexus_container" >/dev/null 2>&1 || true
  docker volume rm "$nexus_data" "$nexus_recall" "$agent_home" "$agent_workspace" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

container_logs() {
  echo "===== NexusDock logs =====" >&2
  docker logs "$nexus_container" >&2 2>/dev/null || true
  echo "===== AgentDock logs =====" >&2
  docker logs "$agent_container" >&2 2>/dev/null || true
}

fail() {
  echo "error: $*" >&2
  container_logs
  exit 1
}

wait_http() {
  local url=$1 attempts=${2:-60}
  local i
  for ((i=0; i<attempts; i++)); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

wait_container_healthy() {
  local container=$1 attempts=${2:-60}
  local i status
  for ((i=0; i<attempts; i++)); do
    status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container" 2>/dev/null || true)"
    case "$status" in
      healthy|running) return 0 ;;
      unhealthy|exited|dead) return 1 ;;
    esac
    sleep 1
  done
  return 1
}

start_nexus() {
  docker rm -f "$nexus_container" >/dev/null 2>&1 || true
  docker run -d \
    --name "$nexus_container" \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,noexec,nosuid,size=64m,uid=10001,gid=10001,mode=0700 \
    -p "127.0.0.1:$nexus_port:18777" \
    -v "$nexus_data:/var/lib/nexus" \
    -v "$nexus_recall:/recall" \
    -e "NEXUS_AUTH_TOKEN=$nexus_token" \
    -e "NEXUS_SECRET_KEY=$nexus_secret" \
    -e NEXUS_REQUIRE_AUTH=true \
    -e NEXUS_AUTH_ALLOW_INSECURE_HTTP=true \
    "$NEXUS_IMAGE" >/dev/null
  wait_http "http://127.0.0.1:$nexus_port/health" 90 || fail "NexusDock health endpoint did not become ready"
  wait_container_healthy "$nexus_container" 90 || fail "NexusDock container did not become healthy"
}

api_get() {
  local path=$1
  curl -fsS \
    -H "Authorization: Bearer $nexus_token" \
    "http://127.0.0.1:$nexus_port$path"
}

api_post() {
  local path=$1 payload=${2:-'{}'}
  curl -fsS \
    -X POST \
    -H "Authorization: Bearer $nexus_token" \
    -H 'Content-Type: application/json' \
    --data "$payload" \
    "http://127.0.0.1:$nexus_port$path"
}

assert_retired_route() {
  local path=$1 status
  status="$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $nexus_token" \
    "http://127.0.0.1:$nexus_port$path")"
  [[ "$status" == 404 ]] || fail "retired route $path returned HTTP $status instead of 404"
}

wait_node_online() {
  local expected_id=${1:-} attempts=${2:-90}
  local i nodes id online
  for ((i=0; i<attempts; i++)); do
    nodes="$(api_get /v1/runtime/nodes 2>/dev/null || true)"
    if [[ -n "$nodes" ]]; then
      id="$(jq -r --arg name "$node_name" '.nodes[]? | select(.name == $name) | .id' <<<"$nodes" | head -1)"
      online="$(jq -r --arg name "$node_name" '.nodes[]? | select(.name == $name) | .online' <<<"$nodes" | head -1)"
      if [[ -n "$id" && "$online" == true && ( -z "$expected_id" || "$id" == "$expected_id" ) ]]; then
        printf '%s' "$id"
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}

start_agent() {
  docker rm -f "$agent_container" >/dev/null 2>&1 || true
  docker run -d \
    --name "$agent_container" \
    --network host \
    -v "$agent_home:/home/agentdock/.agentdock" \
    -v "$agent_workspace:/home/agentdock/AgentDock" \
    -e "AGENTDOCK_AUTH_TOKEN=$agent_token" \
    -e AGENTDOCK_HOST=0.0.0.0 \
    -e "AGENTDOCK_PORT=$agent_port" \
    "$AGENTDOCK_IMAGE" >/dev/null
  wait_container_healthy "$agent_container" 90 || fail "AgentDock container did not become healthy"
}

for volume in "$nexus_data" "$nexus_recall" "$agent_home" "$agent_workspace"; do
  docker volume create "$volume" >/dev/null
done

start_nexus

pairing_json="$(api_post /v1/runtime/nodes/pairing-codes)"
pairing_code="$(jq -r '.pairing.code // empty' <<<"$pairing_json")"
[[ -n "$pairing_code" ]] || fail "NexusDock did not issue a pairing code"

docker run --rm \
  --network host \
  -v "$agent_home:/home/agentdock/.agentdock" \
  -v "$agent_workspace:/home/agentdock/AgentDock" \
  -e "AGENTDOCK_AUTH_TOKEN=$agent_token" \
  "$AGENTDOCK_IMAGE" \
  agentdock nexus pair \
    --endpoint "http://127.0.0.1:$nexus_port" \
    --code "$pairing_code" \
    --name "$node_name" >/dev/null

start_agent
node_id="$(wait_node_online '' 90)" || fail "paired AgentDock did not establish a Bridge connection"
[[ -n "$node_id" ]] || fail "paired AgentDock node_id is empty"
echo "Bridge online: node_id=$node_id"

stage3_value="r5-stage3-${suffix}"

for retired_path in \
  /v1/settings/instructions \
  "/v1/runtime/nodes/$node_id/instructions" \
  "/v1/runtime/nodes/$node_id/direct-instructions" \
  "/v1/runtime/nodes/$node_id/file-access"; do
  assert_retired_route "$retired_path"
done

ai_before="$(api_get /v1/settings/ai)"
ai_revision="$(jq -r '.settings.revision' <<<"$ai_before")"
ai_replace_payload="$(jq -nc \
  --argjson current "$(jq '.settings' <<<"$ai_before")" \
  --arg revision "$ai_revision" \
  --arg prompt "$stage3_value" '
  {
    expected_revision:$revision,
    embedding:{
      enabled:$current.embedding.enabled,
      endpoint:$current.embedding.endpoint,
      model:$current.embedding.model,
      timeout_seconds:$current.embedding.timeout_seconds,
      api_key:{action:"keep"}
    },
    stage3:{
      enabled:$current.stage3.enabled,
      endpoint:$current.stage3.endpoint,
      model:$current.stage3.model,
      timeout_seconds:$current.stage3.timeout_seconds,
      interval_minutes:$current.stage3.interval_minutes,
      api_key:{action:"keep"},
      system_prompt:{action:"replace",value:$prompt},
      review_node_id:$current.stage3.review_node_id
    }
  }
')"
ai_after="$(curl -fsS \
  -X PUT \
  -H "Authorization: Bearer $nexus_token" \
  -H 'Content-Type: application/json' \
  -H "If-Match: \"$ai_revision\"" \
  --data "$ai_replace_payload" \
  "http://127.0.0.1:$nexus_port/v1/settings/ai")"
[[ "$(jq -r '.settings.stage3.system_prompt' <<<"$ai_after")" == "$stage3_value" ]] || fail "Stage3 custom prompt round trip failed"
[[ "$(jq -r '.settings.stage3.system_prompt_source' <<<"$ai_after")" == custom ]] || fail "Stage3 custom prompt source is not custom"

echo 'retired guidance/FileAccess routes rejected; Stage3 write-read smoke passed'

# Restart AgentDock on the same home/workspace volumes. Stable node identity must
# survive and reconnect without re-pairing; retired routes must stay unavailable.
docker rm -f "$agent_container" >/dev/null
start_agent
reconnected_id="$(wait_node_online "$node_id" 90)" || fail "AgentDock did not reconnect with the same node_id after restart"
[[ "$reconnected_id" == "$node_id" ]] || fail "AgentDock node identity changed across restart"
for retired_path in \
  /v1/settings/instructions \
  "/v1/runtime/nodes/$node_id/instructions" \
  "/v1/runtime/nodes/$node_id/direct-instructions" \
  "/v1/runtime/nodes/$node_id/file-access"; do
  assert_retired_route "$retired_path"
done

echo 'AgentDock identity and retired-route boundary persisted across restart'

# Restart NexusDock on the same data/recall volumes while AgentDock stays up. The
# Bridge client must reconnect, Stage3 state must persist, and retired routes must
# not reappear from old database/config state.
docker rm -f "$nexus_container" >/dev/null
start_nexus
reconnected_id="$(wait_node_online "$node_id" 120)" || fail "AgentDock did not reconnect after NexusDock restart"
[[ "$reconnected_id" == "$node_id" ]] || fail "NexusDock restored a different node identity"
for retired_path in \
  /v1/settings/instructions \
  "/v1/runtime/nodes/$node_id/instructions" \
  "/v1/runtime/nodes/$node_id/direct-instructions" \
  "/v1/runtime/nodes/$node_id/file-access"; do
  assert_retired_route "$retired_path"
done

ai_persisted="$(api_get /v1/settings/ai)"
[[ "$(jq -r '.settings.stage3.system_prompt' <<<"$ai_persisted")" == "$stage3_value" ]] || fail "Stage3 custom prompt did not survive NexusDock volume restart"
ai_revision="$(jq -r '.settings.revision' <<<"$ai_persisted")"
ai_reset_payload="$(jq -nc \
  --argjson current "$(jq '.settings' <<<"$ai_persisted")" \
  --arg revision "$ai_revision" '
  {
    expected_revision:$revision,
    embedding:{
      enabled:$current.embedding.enabled,
      endpoint:$current.embedding.endpoint,
      model:$current.embedding.model,
      timeout_seconds:$current.embedding.timeout_seconds,
      api_key:{action:"keep"}
    },
    stage3:{
      enabled:$current.stage3.enabled,
      endpoint:$current.stage3.endpoint,
      model:$current.stage3.model,
      timeout_seconds:$current.stage3.timeout_seconds,
      interval_minutes:$current.stage3.interval_minutes,
      api_key:{action:"keep"},
      system_prompt:{action:"reset"},
      review_node_id:$current.stage3.review_node_id
    }
  }
')"
ai_reset="$(curl -fsS \
  -X PUT \
  -H "Authorization: Bearer $nexus_token" \
  -H 'Content-Type: application/json' \
  -H "If-Match: \"$ai_revision\"" \
  --data "$ai_reset_payload" \
  "http://127.0.0.1:$nexus_port/v1/settings/ai")"
[[ "$(jq -r '.settings.stage3.system_prompt_source' <<<"$ai_reset")" == bundled_default ]] || fail "Stage3 prompt reset did not restore bundled source"
[[ "$(jq -r '.settings.stage3.system_prompt' <<<"$ai_reset")" == "$(jq -r '.settings.stage3.bundled_system_prompt' <<<"$ai_reset")" ]] || fail "Stage3 prompt reset did not restore bundled content"

echo 'NexusDock volume restart / Bridge reconnect / Stage3 persistence-reset passed'

echo "R5 paired container smoke passed: AgentDock=$AGENTDOCK_IMAGE NexusDock=$NEXUS_IMAGE node_id=$node_id"
