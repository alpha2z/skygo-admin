#!/usr/bin/env python3
"""Combine matching per-platform provenance before publishing mutable aliases."""
import argparse
import json
from pathlib import Path


def combine(manifests):
    if len(manifests) != 2:
        raise ValueError('Both platform manifests are required')
    first = manifests[0]
    expected = {(service, platform) for service in ('admin-api', 'admin-web', 'ops-agent')
                for platform in ('linux/amd64', 'linux/arm64')}
    images = []
    seen = set()
    for manifest in manifests:
        if any(manifest.get(k) != first.get(k) for k in ('version', 'id', 'build')):
            raise ValueError('Build identities differ')
        for image in manifest['images']:
            key = (image['service'], image['platform'])
            if key not in expected or key in seen:
                raise ValueError('Unexpected or duplicate component/platform')
            seen.add(key)
            images.append(image)
    if seen != expected:
        raise ValueError('Incomplete multi-platform release')
    return {**first, 'images': images}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('manifests', nargs=2)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    result = combine([json.loads(Path(p).read_text()) for p in args.manifests])
    Path(args.out).write_text(json.dumps(result, separators=(',', ':')) + '\n')
