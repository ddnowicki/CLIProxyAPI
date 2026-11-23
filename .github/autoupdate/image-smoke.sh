#!/usr/bin/env bash
# Boot a candidate image the way production runs it and probe it without real OAuth accounts.
#
#   image-smoke.sh <candidate-image> [<previous-image>]
#
# 1. The candidate starts as UID 1001 with HOME=/data and a mounted auth dir, answers /healthz,
#    rejects /v1/models without a key and lists models with the key.
# 2. Backward compatibility: the previous image (the image production would roll back to) must start
#    on the auth dir the candidate has just initialised (kv.db and friends), so a rollback cannot be
#    blocked by a store format change.
set -euo pipefail

candidate="${1:?candidate image}"
previous="${2:-}"
work="$(mktemp -d)"
key="ci-smoke-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
port=18317
trap 'docker rm -f cliproxy-smoke >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

mkdir -p "$work/auths"
chmod 0777 "$work/auths"
cat > "$work/config.yaml" <<EOF
config-version: 8
server:
  host: ""
  port: 8317
access:
  api-keys:
    - "${key}"
oauth:
  auth-dir: /data/auths
EOF
chmod 0644 "$work/config.yaml"

start() {
  docker rm -f cliproxy-smoke >/dev/null 2>&1 || true
  docker run -d --name cliproxy-smoke --user 1001:1001 -e HOME=/data \
    -p "127.0.0.1:${port}:8317" \
    -v "$work/config.yaml:/CLIProxyAPI/config.yaml:ro" -v "$work/auths:/data/auths" \
    "$1" >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS -m 2 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "::error::$1 did not answer /healthz within 60 s"
  docker logs cliproxy-smoke 2>&1 | tail -n 80
  return 1
}

start "$candidate"
code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:${port}/v1/models")"
[[ "$code" == 401 ]] || { echo "::error::/v1/models without a key returned ${code}, expected 401"; exit 1; }
body="$(curl -fsS -m 10 -H "Authorization: Bearer ${key}" "http://127.0.0.1:${port}/v1/models")"
python3 -c 'import json,sys; d=json.loads(sys.argv[1]); assert d.get("object")=="list" and isinstance(d.get("data"),list), d' "$body" \
  || { echo "::error::/v1/models did not return an OpenAI model list"; exit 1; }
echo "candidate ${candidate}: healthz, auth and model list OK"

if [[ -n "$previous" ]]; then
  docker stop -t 30 cliproxy-smoke >/dev/null
  if docker pull -q "$previous" >/dev/null 2>&1; then
    start "$previous"
    echo "previous ${previous}: starts on the candidate's auth dir"
  else
    echo "::warning::cannot pull ${previous}; backward-compatibility check skipped"
  fi
fi
