#!/usr/bin/env python3
"""Development-only cold pull timing for two representative service images.

This runs inside one real Hakopod gVisor runner against its loopback Docker
Engine. It is intentionally standalone and is not wired into CI or runtime
acceptance.
"""
import http.client
from contextlib import contextmanager
import json
import os
import platform
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse


DOCKER_HOST = 'tcp://127.0.0.1:2375'
DEV_CONTEXT = 'k3d-hakopod-dev'
MAX_IMAGE_SECONDS = 600
MAX_STREAM_BYTES = 4 * 1024 * 1024
MAX_EVENTS = 4096
MAX_LINE_BYTES = 64 * 1024
PREFIX = 'HAKOPOD_SERVICE_PULL '
DAEMON_VERSION = '29.8.1'
DAEMON_COMMIT = '464cd50'
OWNER_LABEL = 'dev.hakopod.service-pull-benchmark'

# Generated from the bounded provider evidence for customer run 36654745374.
SERVICES = (
    {
        'name': 'postgres:16',
        'reference': 'docker.io/library/postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54',
        'binary': 'postgres',
        'platforms': {
            'amd64': {
                'config_digest': 'sha256:1b3c642526f8d274b12bdcd93b90aeb7e68a1f59eb20613adb96ed561c01d98c',
                'layer_prefixes': ('6b37362b3da7', 'b1fdda8975a0', '106e7e3cf19a', 'ba0e979d0818',
                                   'ad29720ebbb9', '3a749597affc', '483fb6941c15', '359ae0fc975d',
                                   '56d4d190ef07', '74156cf9f063', 'f7c7d4dd80ba', '749acf6dc637',
                                   '4d848243a73e', '6ad4758748c1'),
            },
            'arm64': {
                'config_digest': 'sha256:c319f2a8182bcdcbb3297d568e2a9cc9e7da3a624438e14b63921c719817e1b6',
                'layer_prefixes': ('bd36565c0fde', '09de454e7bd2', '3d6dde914ee8', 'e8401bc43ce0',
                                   '76db794b62ee', 'c9d6ac5e716b', 'ae8ae052b88d', 'ca49afcdbda4',
                                   '3672748066c3', '9a09c0d64573', '053534618826', '971b44d2c3d3',
                                   'bb122215696f', '46849de44505'),
            },
        },
    },
    {
        'name': 'redis:7',
        'reference': 'docker.io/library/redis@sha256:c6eabf748fc7a61dbb5a705c78bcf3d6377b1127a97d0ce965c11c44ba46896f',
        'binary': 'redis-server',
        'platforms': {
            'amd64': {
                'config_digest': 'sha256:64035c2c9726d90bea2a217d43da49d7a3f13dde8647c1f409059e02e0e61287',
                'layer_prefixes': ('774043ccc8cc', '428e012d30af', '630e622f045e', 'a65475d5aa6f',
                                   '855021fb4667', '4f4fb700ef54', '4b2212370930'),
            },
            'arm64': {
                'config_digest': 'sha256:24e81cffaba832bcd71068a6ff772a531076bafdbb1d684195766ae9b6511f5c',
                'layer_prefixes': ('333125b5cee9', '2e0d14ce9a6d', 'ca4fd31191b4', '81c4c860b3e4',
                                   '9454c7611e2d', '4f4fb700ef54', 'c30aae447741'),
            },
        },
    },
)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def native_architecture(machine=None):
    value = machine or platform.machine()
    architecture = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(value)
    require(architecture is not None, 'Service pull benchmark requires native amd64 or arm64')
    return architecture


@contextmanager
def absolute_deadline(seconds):
    """Bound blocking Linux I/O and restore the process alarm state."""
    require(sys.platform.startswith('linux') and threading.current_thread() is threading.main_thread(),
            'Absolute Docker deadline requires the Linux main thread')
    previous_handler = signal.getsignal(signal.SIGALRM)
    previous_delay, previous_interval = signal.getitimer(signal.ITIMER_REAL)
    started = time.monotonic()

    def expired(_signum, _frame):
        raise RuntimeError('Docker API operation exceeded its absolute deadline')

    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        if previous_delay > 0 or previous_interval > 0:
            elapsed = time.monotonic() - started
            restored_delay = max(0.000001, previous_delay - elapsed) if previous_delay > 0 else 0
            signal.setitimer(signal.ITIMER_REAL, restored_delay, previous_interval)


class DockerAPI:
    def __init__(self, connection_factory=http.client.HTTPConnection, clock=time.monotonic):
        self.connection_factory = connection_factory
        self.clock = clock

    def request(self, method, path, *, timeout=30, expected=(200,)):
        connection = self.connection_factory('127.0.0.1', 2375, timeout=timeout)
        try:
            with absolute_deadline(timeout):
                connection.request(method, path)
                response = connection.getresponse()
                body = response.read(MAX_LINE_BYTES + 1)
            require(len(body) <= MAX_LINE_BYTES, 'Docker API response exceeded its bound')
            require(response.status in expected, f'Docker API {method} {path.split("?", 1)[0]} returned HTTP {response.status}')
            return response.status, json.loads(body) if body else None
        finally:
            connection.close()

    def inspect(self, reference, *, missing_ok=False):
        path = '/images/' + urllib.parse.quote(reference, safe='') + '/json'
        status, value = self.request('GET', path, expected=(200, 404) if missing_ok else (200,))
        return None if status == 404 else value

    def pull(self, reference, expected_prefixes):
        repository, digest = reference.rsplit('@', 1)
        query = urllib.parse.urlencode({'fromImage': repository, 'tag': digest})
        connection = self.connection_factory('127.0.0.1', 2375, timeout=30)
        started = self.clock()
        observed = {prefix: {} for prefix in expected_prefixes}
        event_count = total_bytes = 0
        try:
            with absolute_deadline(MAX_IMAGE_SECONDS):
                connection.request('POST', '/images/create?' + query)
                response = connection.getresponse()
                require(response.status == 200, f'Docker image pull returned HTTP {response.status}')
                while True:
                    remaining = MAX_IMAGE_SECONDS - (self.clock() - started)
                    require(remaining > 0, 'Docker image pull exceeded 600 seconds')
                    connection.sock.settimeout(remaining)
                    try:
                        line = response.readline(MAX_LINE_BYTES + 1)
                    except (TimeoutError, socket.timeout) as error:
                        raise RuntimeError('Docker image pull exceeded 600 seconds') from error
                    if not line:
                        break
                    total_bytes += len(line)
                    event_count += 1
                    require(len(line) <= MAX_LINE_BYTES and total_bytes <= MAX_STREAM_BYTES and event_count <= MAX_EVENTS,
                            'Docker image pull stream exceeded its bound')
                    try:
                        event = json.loads(line)
                    except (UnicodeDecodeError, ValueError) as error:
                        raise RuntimeError('Docker image pull returned malformed JSON') from error
                    require(isinstance(event, dict), 'Docker image pull returned a non-object event')
                    if event.get('error') or event.get('errorDetail'):
                        raise RuntimeError('Docker image pull failed with an engine error')
                    layer = event.get('id')
                    status = event.get('status')
                    if layer in observed and status in ('Download complete', 'Pull complete') and status not in observed[layer]:
                        observed[layer][status] = round(self.clock() - started, 3)
            missing = [prefix for prefix, timings in observed.items()
                       if set(timings) != {'Download complete', 'Pull complete'}]
            require(not missing, 'Cold pull did not emit complete timings for expected layers: ' + ','.join(missing))
            for timings in observed.values():
                timings['extraction_registration_queue_seconds'] = round(
                    timings['Pull complete'] - timings['Download complete'], 3)
            return {'total_seconds': round(self.clock() - started, 3), 'event_count': event_count,
                    'stream_bytes': total_bytes, 'layers': observed}
        finally:
            connection.close()

    def remove_exact(self, image_id):
        require(image_id.startswith('sha256:') and len(image_id) == 71, 'Refusing to remove an invalid image ID')
        path = '/images/' + urllib.parse.quote(image_id, safe='') + '?' + urllib.parse.urlencode({'force': '0', 'noprune': '1'})
        self.request('DELETE', path, expected=(200,))

    def inspect_container(self, name, *, missing_ok=False):
        path = '/containers/' + urllib.parse.quote(name, safe='') + '/json'
        status, value = self.request('GET', path, expected=(200, 404) if missing_ok else (200,))
        return None if status == 404 else value

    def remove_container_exact(self, container_id):
        require(re.fullmatch(r'[0-9a-f]{64}', container_id) is not None,
                'Refusing to remove an invalid container ID')
        path = '/containers/' + container_id + '?' + urllib.parse.urlencode({'force': '1', 'v': '1'})
        self.request('DELETE', path, expected=(204,))


def verify_environment(api, environment=os.environ, machine=None):
    require(environment.get('DOCKER_HOST') == DOCKER_HOST, 'Only the product loopback Docker API is allowed')
    require(environment.get('HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT') == DEV_CONTEXT,
            'Service pull benchmark requires the explicit named development context')
    expected_driver = environment.get('HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER')
    require(expected_driver in ('vfs', 'overlay2'), 'Expected development Docker storage driver must be vfs or overlay2')
    _, info = api.request('GET', '/info')
    _, version = api.request('GET', '/version')
    architecture = native_architecture(machine)
    info_architecture = native_architecture(info.get('Architecture'))
    require(info_architecture == architecture and info.get('OSType') == 'linux',
            'Docker Engine is not the native Linux architecture')
    require(version.get('Arch') == architecture and version.get('Os') == 'linux',
            'Docker daemon version platform is not native Linux')
    require(version.get('Version') == DAEMON_VERSION and version.get('GitCommit') == DAEMON_COMMIT,
            'Docker daemon version or source revision changed')
    require('gvisor' in str(version.get('KernelVersion', '')).lower(), 'Docker daemon is not running inside gVisor')
    require(info.get('Driver') == expected_driver, 'Docker Engine storage driver differs from the selected development mode')
    require(info.get('DockerRootDir') == '/home/runner/.docker-data', 'Unexpected Docker data root')
    return {'architecture': architecture, 'storage_driver': expected_driver}


def repository_digest_aliases(reference):
    repository, digest = reference.rsplit('@', 1)
    require(repository.startswith('docker.io/library/'), 'Unexpected service repository')
    short = repository.removeprefix('docker.io/library/')
    return {short + '@' + digest, 'library/' + short + '@' + digest, repository + '@' + digest}


def verify_image(image, service, architecture):
    expected = service['platforms'][architecture]
    require(image.get('Id') == expected['config_digest'], 'Pulled service config digest changed')
    require(image.get('Os') == 'linux' and image.get('Architecture') == architecture,
            'Pulled service platform is not native Linux')
    repo_digests = image.get('RepoDigests')
    require(isinstance(repo_digests, list) and len(set(repo_digests) & repository_digest_aliases(service['reference'])) == 1,
            'Pulled service repository digest changed')
    return {'config_digest': image['Id'], 'platform': 'linux/' + architecture,
            'repository_digest': service['reference'].rsplit('@', 1)[1]}


def version_check(api, service, expected_id):
    name = 'hakopod-service-pull-' + service['name'].split(':', 1)[0] + '-' + str(os.getpid()) + '-' + str(time.monotonic_ns())
    command = ['docker', 'run', '--rm', '--network', 'none', '--read-only', '--entrypoint', service['binary'],
               '--name', name, '--label', OWNER_LABEL + '=' + name, '--cpus', '1', '--memory', '512m',
               '--pids-limit', '128', service['reference'], '--version']
    try:
        with tempfile.TemporaryFile() as output_file:
            process = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=output_file, stderr=subprocess.STDOUT,
                                     timeout=30, check=False, env={'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
                                                                  'DOCKER_HOST': DOCKER_HOST})
            size = output_file.tell()
            output_file.seek(0)
            output = output_file.read(4097).decode('utf-8', errors='replace').strip()
        require(process.returncode == 0 and 0 < size <= 4096 and len(output) <= 4096,
                service['name'] + ' version command failed or exceeded its output bound')
        return output.splitlines()[0][:256]
    finally:
        container = api.inspect_container(name, missing_ok=True)
        if container is not None:
            labels = container.get('Config', {}).get('Labels') or {}
            require(container.get('Name') == '/' + name and labels.get(OWNER_LABEL) == name and
                    container.get('Image') == expected_id, 'Refusing to remove an unverified version container')
            api.remove_container_exact(container.get('Id', ''))


def recover_owned_image(api, service, architecture):
    """Return an exact new config ID only when both post-pull names identify it."""
    expected_id = service['platforms'][architecture]['config_digest']
    by_reference = api.inspect(service['reference'], missing_ok=True)
    by_config = api.inspect(expected_id, missing_ok=True)
    if by_reference is None or by_config is None:
        return None
    require(by_reference.get('Id') == expected_id and by_config.get('Id') == expected_id,
            'Refusing cleanup because post-pull image identities differ')
    verify_image(by_reference, service, architecture)
    verify_image(by_config, service, architecture)
    return expected_id


def benchmark_service(api, service, architecture):
    require(api.inspect(service['reference'], missing_ok=True) is None,
            service['name'] + ' target image is already present; cold pull required')
    expected_id = service['platforms'][architecture]['config_digest']
    require(api.inspect(expected_id, missing_ok=True) is None,
            service['name'] + ' target config is already present; cold pull required')
    owned_id = None
    try:
        pull = api.pull(service['reference'], service['platforms'][architecture]['layer_prefixes'])
        image = api.inspect(service['reference'])
        identity = verify_image(image, service, architecture)
        owned_id = image['Id']
        version = version_check(api, service, expected_id)
        require(api.inspect(expected_id).get('Id') == expected_id, 'Owned service config identity changed before cleanup')
        result = {'service': service['name'], 'pull': pull, 'image': identity, 'version': version}
    except Exception as primary_error:
        try:
            recovered = recover_owned_image(api, service, architecture) if owned_id is None else owned_id
            if recovered == expected_id:
                api.remove_exact(recovered)
        except Exception as cleanup_error:
            raise primary_error from cleanup_error
        raise
    api.remove_exact(owned_id)
    return result


def main():
    api = DockerAPI()
    environment = verify_environment(api)
    result = {'schema_version': 1, 'scope': 'development-only sequential cold service image pulls',
              **environment, 'limits': {'images': 2, 'seconds_per_image': MAX_IMAGE_SECONDS,
                                        'stream_bytes_per_image': MAX_STREAM_BYTES, 'events_per_image': MAX_EVENTS},
              'services': []}
    for service in SERVICES:
        result['services'].append(benchmark_service(api, service, environment['architecture']))
    print(PREFIX + json.dumps(result, separators=(',', ':'), sort_keys=True), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        failure = {'schema_version': 1, 'status': 'failed', 'error': str(error)[:512]}
        if error.__cause__ is not None:
            failure['cleanup_error'] = str(error.__cause__)[:256]
        print(PREFIX + json.dumps(failure,
                                  separators=(',', ':'), sort_keys=True), flush=True)
        sys.exit(1)
