#!/usr/bin/env python3
"""Repair the credential namespace label on a verified installer-owned cluster."""
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys

KUBE = ['/opt/hakopod/tools/kubectl', '--kubeconfig=/etc/hakopod/admin-kubeconfig', '--request-timeout=15s']
INSTALLATION = 'hakopod.com/installation'
MANAGED_BY = 'app.kubernetes.io/managed-by'


def repair_namespace(installation, kube=KUBE):
    if not re.fullmatch(r'[0-9a-f]{32}', installation or ''):
        raise ValueError('Invalid installation identity; no changes made')
    result = subprocess.run([*kube, 'get', 'namespace', 'hakopod-system', '--ignore-not-found', '-o', 'json'],
                            check=True, capture_output=True, text=True, timeout=20)
    if not result.stdout.strip():
        # The API can create its own namespace on the first credential write.
        return False
    namespace = json.loads(result.stdout)
    metadata = namespace['metadata']
    labels = metadata.get('labels', {})
    owner = labels.get(INSTALLATION)
    manager = labels.get(MANAGED_BY)
    if owner not in (None, installation) or manager not in (None, 'hakopod'):
        raise ValueError('Credential namespace belongs to another installation or manager; no changes made')
    if manager == 'hakopod':
        return False
    if owner != installation:
        raise ValueError('Credential namespace ownership cannot be verified; no changes made')
    # Test the exact object we inspected. Never overwrite a concurrent ownership change.
    patch = [
        {'op': 'test', 'path': '/metadata/uid', 'value': metadata['uid']},
        {'op': 'test', 'path': '/metadata/resourceVersion', 'value': metadata['resourceVersion']},
        {'op': 'test', 'path': '/metadata/labels/hakopod.com~1installation', 'value': installation},
        {'op': 'add', 'path': '/metadata/labels/app.kubernetes.io~1managed-by', 'value': 'hakopod'},
    ]
    subprocess.run([*kube, 'patch', 'namespace', 'hakopod-system', '--type=json', '--patch-file=/dev/stdin'],
                   input=json.dumps(patch), check=True, capture_output=True, text=True, timeout=20)
    return True


def main():
    if os.geteuid() != 0:
        raise ValueError('Run credential repair as root')
    with open('/run/lock/hakopod-install.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        config = json.loads(Path('/etc/hakopod/config.json').read_text())
        marker = json.loads(Path('/etc/hakopod/installation.json').read_text())
        if config.get('deployment_mode', 'self-hosted') != 'self-hosted' or not marker.get('completed'):
            raise ValueError('A completed self-hosted installation is required')
        changed = repair_namespace(marker['id'])
    print('Credential namespace label repaired. Retry saving the connection.' if changed else
          'Credential namespace needs no label repair. Check API-to-Kubernetes access if saving still fails.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        message = str(error) if isinstance(error, ValueError) else 'Could not verify or repair the namespace; no credentials were read.'
        print('Hakopod credential repair: ' + message, file=sys.stderr)
        sys.exit(1)
