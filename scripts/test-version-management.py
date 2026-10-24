#!/usr/bin/env python3
"""Exercise PR2 against an isolated dev API; creates one private expert per run."""
import argparse
import json
import urllib.error
import urllib.parse
import urllib.request


def request(base, method, route, body=None):
    payload = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base.rstrip('/') + route, payload, method=method)
    req.add_header('Content-Type', 'application/json')
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            data = response.read()
            return json.loads(data) if response.headers.get_content_type() == 'application/json' else data
    except urllib.error.HTTPError as error:
        raise RuntimeError(f'{method} {route}: HTTP {error.code}: {error.read().decode()}') from error


def document(label, text, plugin_id=None):
    plugin = {
        'plugin_name': 'PR2 Local Test Expert', 'plugin_type': 'expert',
        'visibility': 'private', 'tags': [], 'version': label,
        'manifest_json': {'$schema': 'cowork-plugin-manifest-2.0.json',
                          'plugin_name': 'PR2 Local Test Expert', 'plugin_type': 'expert',
                          'name': 'pr2-local-test-expert', 'description': 'Local version management test.',
                          'labels': [], 'examples': []},
        'plugin_json': {'$schema': 'cowork-plugin-package-2.0.json', 'attachments': [
            {'path': 'AGENTS.md', 'content_type': 'raw', 'mime_type': 'text/markdown', 'raw_content': text}]}}
    if plugin_id:
        plugin['plugin_id'] = plugin_id
    return {'plugin': plugin, 'relations': []}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:8092')
    args = parser.parse_args()
    base = args.base_url
    created = request(base, 'POST', '/api/v1/plugins/upsert', document('1.0.0', 'original v1'))['data']['plugin']
    pid, v1 = created['plugin_id'], created['current_version_id']
    request(base, 'POST', '/api/v1/plugins/publish', {'plugin_id': pid})
    print('1/4: saved and published v1')
    saved = request(base, 'POST', '/api/v1/plugins/upsert', document('2.0.0', 'unpublished v2', pid))['data']['plugin']
    detail = request(base, 'GET', '/api/v1/plugins/detail?plugin_id=' + urllib.parse.quote(pid))['data']['plugin']
    draft = request(base, 'GET', f'/api/v1/plugins/{pid}/draft')['data']['plugin']
    assert 'original v1' in json.dumps(detail['plugin_json']), detail
    assert 'unpublished v2' in json.dumps(draft['plugin_json']), draft
    print('2/4: saving v2 kept the public content at v1')
    history = request(base, 'GET', '/api/v1/plugins/versions?plugin_id=' + urllib.parse.quote(pid))['data']
    assert len(history) == 2, history
    version = request(base, 'GET', f'/api/v1/plugins/{pid}/versions/{v1}')['data']['version']
    assert version['release']['version_label'] == '1.0.0', version
    original = request(base, 'GET', f'/api/v1/plugins/{pid}/versions/{v1}/file?path=AGENTS.md')
    assert original == b'original v1', original
    print('3/4: historical release metadata and original file match v1')
    detail = request(base, 'GET', '/api/v1/plugins/detail?plugin_id=' + urllib.parse.quote(pid))['data']['plugin']
    assert 'original v1' in json.dumps(detail['plugin_json'])
    print('4/4: history reads did not publish or overwrite the release')
    print(json.dumps({'base_url': base, 'plugin_id': pid, 'v1': v1, 'current_draft': saved['current_version_id']}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
