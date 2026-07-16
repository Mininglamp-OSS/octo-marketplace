#!/usr/bin/env bash
set -euo pipefail

# Start the octo-marketplace development environment.
# Usage: ./scripts/start-dev.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_DIR"

echo "==> Starting MySQL via docker compose..."
docker compose up -d mysql

echo "==> Waiting for MySQL to be healthy..."
for i in $(seq 1 30); do
  if docker compose exec mysql mysqladmin ping -h localhost --silent 2>/dev/null; then
    echo "    MySQL is ready."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "    ERROR: MySQL did not become ready in 30 seconds."
    exit 1
  fi
  sleep 1
done

# Export development environment variables (override with .env if present)
export MYSQL_DSN="${MYSQL_DSN:-marketplace:marketplace@tcp(127.0.0.1:3306)/octo_marketplace?charset=utf8mb4&parseTime=true}"
export API_PORT="${API_PORT:-8092}"
export AUTH_ENABLED="${AUTH_ENABLED:-false}"
export DEV_AUTH_UID="${DEV_AUTH_UID:-dev-user}"
export DEV_AUTH_NAME="${DEV_AUTH_NAME:-Developer}"
export DEV_SPACE_ID="${DEV_SPACE_ID:-dev-space}"
export STORAGE_DRIVER="${STORAGE_DRIVER:-local}"
export LOCAL_STORAGE_DIR="${LOCAL_STORAGE_DIR:-/tmp/marketplace-uploads}"

# Source .env if it exists (allows local overrides)
if [ -f "$PROJECT_DIR/.env" ]; then
  echo "==> Loading .env overrides..."
  set -a
  # shellcheck disable=SC1091
  source "$PROJECT_DIR/.env"
  set +a
fi

echo "==> Starting marketplace API on :${API_PORT}..."
exec go run ./cmd/marketplace-api
