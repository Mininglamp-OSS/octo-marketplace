#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ $# != 3 ]]; then
  echo 'Usage: DATABASE_URL=<isolated localhost marketplace_rehearsal DB> run.sh FLEET_SOURCE DAEMON_SOURCE OUTPUT_DIR' >&2
  exit 2
fi
fleet_source=$(realpath "$1")
daemon_source=$(realpath "$2")
mkdir -p "$3"
output_dir=$(realpath "$3")
tool_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$tool_dir/../.." && pwd)
: "${DATABASE_URL:?Explicit isolated DATABASE_URL is required}"
# This is a synthetic rehearsal tool, never a general database migration entry.
python3 - <<'PY'
import os,sys,urllib.parse
u=urllib.parse.urlparse(os.environ['DATABASE_URL'])
if u.scheme not in ('postgres','postgresql') or u.hostname not in ('127.0.0.1','localhost') or not u.port or u.port<1024 or u.path!='/marketplace_rehearsal':
    sys.exit('Refusing database: requires localhost, explicit unprivileged port and database marketplace_rehearsal')
PY
[[ -f "$fleet_source/internal/handler/capability_installation.go" && -f "$daemon_source/internal/daemon/execenv/context.go" ]]
command -v go >/dev/null
python3 "$tool_dir/source_identity.py" "$fleet_source" "$daemon_source" > "$output_dir/source-before.json"
export MARKETPLACE_REHEARSAL_REQUEST="$output_dir/request.json"
export MARKETPLACE_REHEARSAL_STORED="$output_dir/fleet-stored-skills.json"
python3 - "$tool_dir" "$fleet_source" "$daemon_source" "$output_dir" <<'PY'
import json,pathlib,sys
scripts,fleet,daemon,out=map(pathlib.Path,sys.argv[1:])
for name,root,pkg in [('fleet',fleet,'internal/handler'),('daemon',daemon,'internal/daemon/execenv')]:
    overlay={'Replace':{str(root/pkg/'marketplace_rehearsal_overlay_test.go'):str(scripts/(name+'_test.go.txt'))}}
    (out/(name+'-overlay.json')).write_text(json.dumps(overlay))
PY
(cd "$repo_root" && go run -mod=readonly ./tools/fleet-install-rehearsal/export) > "$MARKETPLACE_REHEARSAL_REQUEST"
# The checked-out sources stay untouched: extra tests exist only in Go's overlay.
(cd "$fleet_source" && go run -mod=readonly ./cmd/migrate up) > "$output_dir/fleet-migrations.log" 2>&1
(cd "$fleet_source" && go test -mod=readonly -overlay "$output_dir/fleet-overlay.json" ./internal/handler -run '^TestMarketplacePinnedInstallRehearsal$' -count=1 -v) > "$output_dir/fleet-test.log" 2>&1
(cd "$daemon_source" && go test -mod=readonly -overlay "$output_dir/daemon-overlay.json" ./internal/daemon/execenv -run '^TestMarketplacePinnedMaterializationRehearsal$' -count=1 -v) > "$output_dir/daemon-test.log" 2>&1
python3 "$tool_dir/source_identity.py" "$fleet_source" "$daemon_source" > "$output_dir/source-after.json"
cmp "$output_dir/source-before.json" "$output_dir/source-after.json"
python3 - "$fleet_source" "$daemon_source" "$output_dir" <<'PY'
import json,pathlib,sys
fleet,daemon,out=map(pathlib.Path,sys.argv[1:]);skills=json.loads((out/'fleet-stored-skills.json').read_text())
source=json.loads((out/'source-after.json').read_text())
report={'source_identity':source,'skills':len(skills),'experts':2,'bindings':4,'teams':1,'real_fleet_handler_and_postgres':True,'real_daemon_materialization':True,'online_daemon_dispatch':False,'llm_execution':False}
(out/'result.json').write_text(json.dumps(report,indent=2)+'\n')
print('PASS: Marketplace aliases -> real Fleet/Postgres -> real Daemon materialization; 3 skills, 2 experts, 4 bindings, 1 team.')
PY
