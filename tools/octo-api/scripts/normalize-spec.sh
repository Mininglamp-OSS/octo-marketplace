#!/usr/bin/env bash
#
# normalize-spec.sh — post-process swag output to fix known quirks.
#
# Why: swag v2.0.0-rc5 unconditionally emits an empty `externalDocs`
# block (description: "", url: ""), which violates the OpenAPI 3.x
# schema rule that `url` must be a uri. spectral lint then fails with
# "url property must match format uri".
#
# This script removes the empty externalDocs from both swagger.yaml and
# swagger.json post-generation. When swag fixes this upstream, remove the
# normalization step from openapi.mk.
#
# Usage:
#   bash normalize-spec.sh <openapi-out-dir>

set -euo pipefail

OUT_DIR="${1:?usage: normalize-spec.sh <openapi-out-dir>}"

if [ ! -d "$OUT_DIR" ]; then
  echo "❌ output dir not found: $OUT_DIR" >&2
  exit 1
fi

command -v python3 >/dev/null 2>&1 || {
  echo "⚠  python3 not found — skipping spec normalization" >&2
  exit 0
}

python3 - "$OUT_DIR" <<'PY'
import json, os, sys

out_dir = sys.argv[1]

# --- swagger.yaml: line-based delete of empty externalDocs block ---
yaml_path = os.path.join(out_dir, 'swagger.yaml')
if os.path.exists(yaml_path):
    with open(yaml_path) as f:
        lines = f.read().split('\n')
    out = []
    i = 0
    while i < len(lines):
        if (lines[i] == 'externalDocs:'
                and i + 2 < len(lines)
                and lines[i + 1] == '  description: ""'
                and lines[i + 2] == '  url: ""'):
            i += 3
            continue
        out.append(lines[i])
        i += 1
    with open(yaml_path, 'w') as f:
        f.write('\n'.join(out))

    # swag applies @Produce to every response and emits `type: file`, which is
    # not valid OpenAPI 3.1. Keep byte-stream success bodies explicit while
    # restoring JSON error envelopes for the two approved download operations.
    import yaml
    with open(yaml_path) as f:
        spec = yaml.safe_load(f)
    # swag v2 also wraps body parameters in an unconstrained `oneOf: [object,
    # $ref]`. That makes `{}` valid even when the referenced request schema has
    # required fields. Collapse this known generator artifact for strict PATCH
    # bodies so the published contract matches runtime validation.
    strict_patch_paths = (
        '/plugin_review_policies',
        '/admin/plugins/{plugin_id}/rating',
    )
    for path_name in strict_patch_paths:
        operation = spec.get('paths', {}).get(path_name, {}).get('patch')
        if not operation:
            continue
        schema = (operation.get('requestBody', {}).get('content', {})
                  .get('application/json', {}).get('schema', {}))
        choices = schema.get('oneOf') if isinstance(schema, dict) else None
        if (isinstance(choices, list) and len(choices) == 2
                and choices[0] == {'type': 'object'}
                and isinstance(choices[1], dict)
                and '$ref' in choices[1]):
            operation['requestBody']['content']['application/json']['schema'] = {
                '$ref': choices[1]['$ref']
            }

    # OpenAPI 3.1 uses JSON Schema nullability. swag emits the legacy vendor
    # extension for pointer scalars, which strict clients ignore. Convert every
    # x-nullable schema mechanically so request and response contracts accept
    # the null values the runtime reads and writes.
    def normalize_nullable(node):
        if isinstance(node, dict):
            if node.pop('x-nullable', False):
                value_type = node.get('type')
                if isinstance(value_type, str):
                    node['type'] = [value_type, 'null']
                elif isinstance(value_type, list) and 'null' not in value_type:
                    node['type'] = value_type + ['null']
            for value in node.values():
                normalize_nullable(value)
        elif isinstance(node, list):
            for value in node:
                normalize_nullable(value)

    normalize_nullable(spec)

    streams = {
        '/plugins/download': 'application/zip',
        '/admin/plugins/{plugin_id}/download': 'application/zip',
    }
    for path_name, success_media in streams.items():
        operation = spec.get('paths', {}).get(path_name, {}).get('get')
        if not operation:
            continue
        for status, response in operation.get('responses', {}).items():
            if str(status).startswith('2'):
                response['content'] = {success_media: {'schema': {'type': 'string', 'format': 'binary'}}}
            elif str(status).startswith(('4', '5')):
                content = response.get('content', {})
                media = next(iter(content.values()), {})
                response['content'] = {'application/json': media}
    with open(yaml_path, 'w') as f:
        yaml.safe_dump(spec, f, sort_keys=False, allow_unicode=True)

# --- swagger.json: parse + delete empty externalDocs ---
json_path = os.path.join(out_dir, 'swagger.json')
if os.path.exists(json_path):
    with open(json_path) as f:
        data = json.load(f)
    e = data.get('externalDocs')
    if isinstance(e, dict) and not e.get('url'):
        del data['externalDocs']
        with open(json_path, 'w') as f:
            json.dump(data, f, indent=4, ensure_ascii=False)
            f.write('\n')

print(f"✓ normalized spec in {out_dir}", file=sys.stderr)
PY
