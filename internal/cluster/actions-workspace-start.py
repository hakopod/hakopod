"""Check the selected shared workspace before any GitHub registration."""
import http.client
import json
import os
import platform
import signal
import stat
import sys


def _hakopod_workspace_preflight():
    def require(condition, message):
        if not condition:
            raise RuntimeError(message)

    def expired(_signal, _frame):
        raise RuntimeError('workspace startup check exceeded its deadline')

    def read(path):
        connection = http.client.HTTPConnection('127.0.0.1', 2375, timeout=5)
        try:
            connection.request('GET', path)
            response = connection.getresponse()
            require(response.status == 200, 'Docker did not answer its workspace check')
            encoded = response.read(131073)
            require(0 < len(encoded) <= 131072, 'Docker workspace response exceeded its bound')
            value = json.loads(encoded)
            require(isinstance(value, dict), 'Docker workspace response was invalid')
            return value
        finally:
            connection.close()

    previous = signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, 25)
    try:
        descriptor = os.open('/home/runner/.hakopod-shared-prepare', os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, 'rb') as marker:
            require(stat.S_ISREG(os.fstat(marker.fileno()).st_mode) and marker.read(64) == b'shared-workspace-v1\n',
                    'the prepare workspace is not shared with the runner')
        info, version = read('/info'), read('/version')
        require(info.get('Driver') == 'overlay2' and info.get('DockerRootDir') == '/home/runner/.docker-data',
                'Docker did not use the selected workspace driver and data root')
        require(version.get('Version') == '29.8.1' and version.get('GitCommit') == '464cd50' and
                version.get('Os') == 'linux' and version.get('Arch') in ('amd64', 'arm64') and
                version['Arch'] == {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine()),
                'Docker does not match the pinned native daemon')
        require(all(type(info.get(key)) is int and info[key] == 0 for key in ('Images', 'Containers')),
                'the new runner requires a fresh Docker store')
        pairs = info.get('DriverStatus')
        require(isinstance(pairs, list) and len(pairs) <= 16 and
                all(isinstance(pair, list) and len(pair) == 2 and all(isinstance(item, str) for item in pair) for pair in pairs),
                'Docker storage driver status was invalid')
        status = dict(pairs)
        require(len(status) == len(pairs) and status.get('Backing Filesystem') == 'tmpfs' and
                status.get('Supports d_type') == 'true' and status.get('Native Overlay Diff') == 'true',
                'Docker does not support the qualified shared workspace')
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)


try:
    _hakopod_workspace_preflight()
except Exception as _workspace_error:
    _workspace_reason = str(_workspace_error) if isinstance(_workspace_error, RuntimeError) else type(_workspace_error).__name__
    print('Actions workspace startup check failed: ' + _workspace_reason, file=sys.stderr)
    sys.exit(1)
