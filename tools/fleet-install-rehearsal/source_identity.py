#!/usr/bin/env python3
"""Identify exactly the tested source tree without inheriting a parent Git repo."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


def identity(root):
    root = Path(root).resolve()
    commit = None
    try:
        git_root = subprocess.check_output(
            ['git', '-C', str(root), 'rev-parse', '--show-toplevel'],
            text=True, stderr=subprocess.DEVNULL).strip()
        if Path(git_root).resolve() == root:
            commit = subprocess.check_output(
                ['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True).strip()
    except subprocess.CalledProcessError:
        pass
    tree = hashlib.sha256()
    for current, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d != '.git')
        for name in sorted(files):
            path = Path(current) / name
            relative = path.relative_to(root).as_posix()
            if relative == '.git':
                continue
            tree.update(relative.encode() + b'\0')
            tree.update(hashlib.sha256(path.read_bytes()).digest())
    return {'git_commit': commit, 'source_tree_sha256': tree.hexdigest()}


if __name__ == '__main__':
    print(json.dumps({'fleet': identity(sys.argv[1]), 'daemon': identity(sys.argv[2])}, indent=2))
