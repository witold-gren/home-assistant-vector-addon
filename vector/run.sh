#!/bin/bash
# vector-bridge: options from the app, API tokens in /data, discovery for the integration.
set -euo pipefail

OPTIONS=/data/options.json
WS_PORT=3012
log() { echo "[vector-addon] $*"; }
opt() { jq -r ".$1 // empty" "${OPTIONS}"; }

# Random on the first start, then kept (the integration and wire-pod know them).
secret() {
    local file="/data/$1"
    [ -s "${file}" ] || head -c 24 /dev/urandom | base64 | tr -d '/+=\n' > "${file}"
    chmod 600 "${file}"
    cat "${file}"
}
WS_TOKEN=$(secret ws_token)
HOOK_TOKEN=$(secret hook_token)

# The GUID only when given by hand (normally it comes from wire-pod); in a file, not on the command line.
GUID_FILE=""
if [ -n "$(opt guid)" ]; then
    GUID_FILE=/data/guid
    opt guid > "${GUID_FILE}"
    chmod 600 "${GUID_FILE}"
else
    rm -f /data/guid
fi

announce() {
    [ -n "${SUPERVISOR_TOKEN:-}" ] || return 0
    for _ in $(seq 1 60); do
        curl -fsS "http://127.0.0.1:${WS_PORT}/healthz" >/dev/null 2>&1 && break
        sleep 1
    done
    local host
    host=$(curl -fsS -H "Authorization: Bearer ${SUPERVISOR_TOKEN}" http://supervisor/addons/self/info | jq -r '.data.hostname')
    jq -n --arg host "${host}" --arg token "${WS_TOKEN}" --argjson port "${WS_PORT}" \
        '{service: "vector", config: {host: $host, port: $port, token: $token}}' \
      | curl -fsS -X POST -H "Authorization: Bearer ${SUPERVISOR_TOKEN}" -H "Content-Type: application/json" \
          --data @- http://supervisor/discovery >/dev/null \
      && log "Announced to the Vector integration: ${host}:${WS_PORT}" \
      || log "Could not announce to the integration (add it by hand)"
}
announce &

# Hook address for wire-pod: the HA host IP (or hook_host) and the port published for 8790/tcp.
HOOK_URL=""
if [ -n "${SUPERVISOR_TOKEN:-}" ]; then
    sv() { curl -fsS -H "Authorization: Bearer ${SUPERVISOR_TOKEN}" "http://supervisor$1"; }
    HOOK_HOST="$(opt hook_host)"
    if [ -z "${HOOK_HOST}" ]; then
        HOOK_HOST=$(sv /network/info | jq -r '[.data.interfaces[] | select(.primary) | .ipv4.address[0]][0] // empty' | cut -d/ -f1)
    fi
    HOOK_PORT=$(sv /addons/self/info | jq -r '.data.network["8790/tcp"] // empty')
    if [ -n "${HOOK_HOST}" ] && [ -n "${HOOK_PORT}" ]; then
        HOOK_URL="http://${HOOK_HOST}:${HOOK_PORT}"
    fi
fi
if [ -n "${HOOK_URL}" ]; then
    log "Voice → HA: hook ${HOOK_URL} (wire-pod intent ha_router)"
else
    log "Voice → HA off: no host address or port 8790/tcp is not published"
fi

log "wire-pod: $(opt wirepod_url), robot: $(opt esn)${GUID_FILE:+ (GUID set by hand)}"
exec /usr/local/bin/vector-bridge \
    -listen ":${WS_PORT}" \
    -token-file /data/ws_token \
    -wirepod "$(opt wirepod_url)" \
    -esn "$(opt esn)" \
    -name "$(opt name)" \
    -ip "$(opt robot_ip)" \
    -guid-file "${GUID_FILE}" \
    -log-level "$(opt log_level)" \
    -hook-url "${HOOK_URL}" \
    -hook-token-file /data/hook_token \
    -voice-router="$(jq -r '.voice_router' "${OPTIONS}")" \
    -state-dir /data
