#!/usr/bin/env python3
"""Sync an immutable source module, or verify the checked-in source/hash manifest."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parents[1]
target = root / 'third_party/unihub-userkit'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--source', type=Path, help='UniHub checkout, required only for updates')
parser.add_argument('--check', action='store_true')
args = parser.parse_args()

def hashes(directory):
    return {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(directory.rglob('*')) if p.is_file() and p.name != 'PROVENANCE.json'}

if args.check:
    manifest = json.loads((target / 'PROVENANCE.json').read_text())
    if hashes(target) != manifest['sha256']:
        raise SystemExit('userkit snapshot differs from provenance')
    if args.source and hashes(args.source / 'packages/userkit-go') != manifest['sha256']:
        raise SystemExit('userkit snapshot differs from source')
    print('userkit exact-source verification passed at ' + manifest['commit'])
else:
    if not args.source:
        parser.error('--source is required for sync')
    source = args.source.resolve()
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=source, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--', 'packages/userkit-go'], cwd=source, text=True):
        raise SystemExit('source module must be committed before sync')
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(source / 'packages/userkit-go', target)
    (target / 'PROVENANCE.json').write_text(json.dumps({
        'source': 'https://github.com/NeoMei/UniHub', 'module': 'packages/userkit-go',
        'commit': revision, 'distribution': 'unpublished source snapshot; no declared upstream license',
        'sha256': hashes(target),
    }, indent=2, sort_keys=True) + '\n')
    print('synced userkit at ' + revision)
