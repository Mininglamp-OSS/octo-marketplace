#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
self="$PWD/$(basename "$0")"
case "${1:-}" in
  up)
    if [[ ! -f .env ]]; then
      umask 077
      python3 - <<'PY' > .env
import secrets
print('HARNESS_PASSWORD=' + secrets.token_hex(24))
print('HARNESS_ROOT_PASSWORD=' + secrets.token_hex(24))
PY
    fi
    docker compose --env-file .env up -d --wait --wait-timeout 120
    "$self" health
    ;;
  health)
    docker compose --env-file .env exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -h 127.0.0.1 -uinventory marketplace_fixture -Nse "SELECT COUNT(*) FROM plugins"' | grep -qx 8
    echo 'MySQL fixture healthy: 8 synthetic resources'
    ;;
  smoke)
    "$self" health
    # This negative probe must fail at the account privilege boundary.
    write_log=$(mktemp)
    trap 'rm -f "$write_log"' EXIT
    if docker compose --env-file .env exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -h 127.0.0.1 -uinventory marketplace_fixture -e "UPDATE plugins SET plugin_name=plugin_name WHERE 1=0"' > /dev/null 2> "$write_log"; then
      echo 'ERROR: inventory account can write' >&2; exit 1
    fi
    grep -q 'UPDATE command denied' "$write_log"
    before=$(docker compose --env-file .env exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --skip-comments --compact --skip-lock-tables --no-tablespaces marketplace_fixture' | sha256sum)
    # Credentials come exclusively from this harness, never from an ambient DSN.
    source .env
    export MARKETPLACE_INVENTORY_DSN="inventory:${HARNESS_PASSWORD}@tcp(127.0.0.1:33394)/marketplace_fixture"
    go_bin=${GO_BIN:-go}
    repo=$(cd ../.. && pwd)
    (cd "$repo" && "$go_bin" run ./cmd/marketplace-inventory --all-spaces) > report.json
    python3 - <<'PY'
import json
with open('report.json') as f: report = json.load(f)
assert len(report['resources']) == 8, report
assert report['same_name_groups'] == [['fixture-skill-a', 'fixture-skill-b']], report
assert report['finding_counts'] == {
    'attachment_metadata_unknown': 5,
    'attachment_sidecar_invalid': 1,
    'current_snapshot_missing': 1,
}, report
assert report['distribution'] == [
    {'visibility':'legacy_public','listing':'delisted','identity':'top_level','deleted':False,'count':1},
    {'visibility':'private','listing':'draft','identity':'top_level','deleted':False,'count':1},
    {'visibility':'private','listing':'published','identity':'embedded','deleted':False,'count':1},
    {'visibility':'private','listing':'published','identity':'top_level','deleted':False,'count':2},
    {'visibility':'space','listing':'published','identity':'top_level','deleted':False,'count':1},
    {'visibility':'system','listing':'draft','identity':'top_level','deleted':False,'count':1},
    {'visibility':'unknown','listing':'unknown','identity':'top_level','deleted':False,'count':1},
], report
assert sum(cell['count'] for cell in report['distribution']) == len(report['resources'])
assert report['object_verification'] == 'not_checked', report
print('M1 SQL inventory: expected resources, duplicate names and missing snapshot found')
PY
    after=$(docker compose --env-file .env exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --skip-comments --compact --skip-lock-tables --no-tablespaces marketplace_fixture' | sha256sum)
    [[ "$before" == "$after" ]] || { echo 'ERROR: fixture changed during inventory' >&2; exit 1; }
    echo 'Read-only verified: write denied and before/after database dumps identical'
    ;;
  down)
    docker compose --env-file .env down
    ;;
  *) echo 'Usage: run.sh up|health|smoke|down' >&2; exit 2 ;;
esac
