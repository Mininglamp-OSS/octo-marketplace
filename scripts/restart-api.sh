#!/bin/bash
# Rebuild + restart marketplace-api. Idempotent: kills any process holding :8092
# then rebuilds and relaunches with the dev env vars from CLAUDE session.
#
# Usage:
#   ./scripts/restart-api.sh
#
# Requires the mysql container to already be up (docker compose up -d mysql).

set -e
cd "$(dirname "$0")/.."

echo "[restart] killing any listener on :8092..."
lsof -tiTCP:8092 -sTCP:LISTEN 2>/dev/null | xargs -r kill -9 2>/dev/null || true
sleep 1

echo "[restart] building..."
go build -o /tmp/marketplace-api ./cmd/marketplace-api

echo "[restart] starting..."
export MYSQL_DSN='marketplace:marketplace@tcp(127.0.0.1:3306)/octo_marketplace?charset=utf8mb4&parseTime=true'
export API_PORT=8092
export AUTH_ENABLED=false
export DEV_AUTH_UID=dev-user
export DEV_AUTH_NAME=Developer
export DEV_SPACE_ID=dev-space
export MARKETPLACE_ADMIN_TOKEN=dev-admin-token

nohup /tmp/marketplace-api > /tmp/marketplace.log 2>&1 &
PID=$!
sleep 2

if kill -0 "$PID" 2>/dev/null; then
  echo "[restart] up on PID $PID"
  echo "[restart] tail: tail -f /tmp/marketplace.log"
  curl -sS -w "\n[healthz: %{http_code}]\n" http://127.0.0.1:8092/healthz || true
else
  echo "[restart] FAILED — see /tmp/marketplace.log:" >&2
  tail /tmp/marketplace.log >&2
  exit 1
fi
