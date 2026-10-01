#!/usr/bin/env python3
"""Owned, bounded launcher for the public-endpoint Kubernetes fault proxy."""
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import stat
import subprocess
import time
import urllib.error
import urllib.request


STATUS_FIELDS = {'schema_version', 'mode', 'route_mutation_seen', 'successful_route_mutations',
                 'pre_mutation_proxy_upgrade_relays', 'pre_mutation_database_upgrade_relays'}
NAMESPACE = re.compile(r'^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$')
RESOURCE = re.compile(r'^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?)*$')


def file_hash(path, maximum=64 * 1024 * 1024):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > maximum:
        raise ValueError('fault proxy executable is missing, symbolic or oversized')
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


class FaultProxy:
    def __init__(self, binary, binary_sha256, kubectl, kubeconfig, context, proxy_namespace, work):
        self.binary = Path(binary)
        self.binary_sha256 = binary_sha256
        self.kubectl = str(kubectl)
        self.kubeconfig = Path(kubeconfig)
        self.context = context
        self.proxy_namespace = proxy_namespace
        self.work = Path(work)
        self.kubectl_port, self.port = free_port(), free_port()
        while self.port == self.kubectl_port:
            self.port = free_port()
        self.mode = None
        self.kubectl_process = None
        self.helper_process = None
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.proxy_kubeconfig = self.work / ('fault-proxy-' + secrets.token_hex(4) + '.kubeconfig.json')

    def validate_inputs(self):
        info = self.binary.lstat()
        kube_info = self.kubeconfig.lstat()
        work_info = self.work.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o111 == 0 or
                not re.fullmatch(r'[a-f0-9]{64}', self.binary_sha256) or
                file_hash(self.binary) != self.binary_sha256):
            raise ValueError('fault proxy binary does not match the preflight plan')
        if (not stat.S_ISREG(kube_info.st_mode) or kube_info.st_uid != os.getuid() or kube_info.st_mode & 0o077 or
                not stat.S_ISDIR(work_info.st_mode) or work_info.st_uid != os.getuid() or
                not self.context or not NAMESPACE.fullmatch(self.proxy_namespace)):
            raise ValueError('fault proxy launcher inputs are invalid')

    def status(self):
        with self.opener.open(f'http://127.0.0.1:{self.port}/__hakopod_fault_proxy/status', timeout=2) as response:
            raw = response.read(4097)
        value = json.loads(raw)
        if (len(raw) > 4096 or not isinstance(value, dict) or set(value) != STATUS_FIELDS or
                value['schema_version'] != 1 or value['mode'] != self.mode or
                not isinstance(value['route_mutation_seen'], bool) or
                any(type(value[name]) is not int or value[name] < 0 for name in (
                    'successful_route_mutations', 'pre_mutation_proxy_upgrade_relays',
                    'pre_mutation_database_upgrade_relays'))):
            raise ValueError('fault proxy returned invalid status')
        return value

    def start(self, *, mode, database_namespace=None, route_namespace=None, route_resource=None):
        if self.mode is not None:
            raise ValueError('fault proxy cannot be started twice')
        if mode not in ('missing_tcp_crd', 'master_socket_unavailable', 'route_reload_after_mutation'):
            raise ValueError('fault proxy mode is invalid')
        route_mode = mode == 'route_reload_after_mutation'
        if not isinstance(database_namespace, str) or not database_namespace:
            raise ValueError('fault proxy database namespace is required')
        if not NAMESPACE.fullmatch(database_namespace):
            raise ValueError('fault proxy database namespace is invalid')
        if database_namespace == self.proxy_namespace:
            raise ValueError('fault proxy namespaces must be distinct')
        route_values = (route_namespace, route_resource)
        if (route_mode and not all(isinstance(value, str) and value for value in route_values)) or (
                not route_mode and any(value is not None for value in route_values)):
            raise ValueError('route fault identity is incomplete or unexpected')
        if route_mode and (route_namespace != database_namespace or not NAMESPACE.fullmatch(route_namespace) or
                           len(route_resource) > 253 or
                           not RESOURCE.fullmatch(route_resource)):
            raise ValueError('route fault identity is invalid')
        self.validate_inputs()
        self.mode = mode
        try:
            self.kubectl_process = subprocess.Popen([
                self.kubectl, '--kubeconfig', str(self.kubeconfig), '--context', self.context, 'proxy',
                '--address', '127.0.0.1', '--port', str(self.kubectl_port), '--accept-hosts', r'^127\.0\.0\.1$',
                '--reject-paths=^/api/.*/pods/.*/attach,^/api/.*/pods/.*/portforward'],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
            upstream = f'http://127.0.0.1:{self.kubectl_port}'
            self.wait_ready(upstream + '/version', self.kubectl_process, 'kubectl proxy')
            args = [str(self.binary), '--listen', f'127.0.0.1:{self.port}', '--upstream', upstream,
                    '--mode', mode, '--proxy-namespace', self.proxy_namespace,
                    '--database-namespace', database_namespace]
            if route_mode:
                args.extend(['--route-namespace', route_namespace, '--route-resource', route_resource])
            self.helper_process = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                                   start_new_session=True)
            self.wait_ready(f'http://127.0.0.1:{self.port}/__hakopod_fault_proxy/status',
                            self.helper_process, 'fault proxy helper')
            self.status()
            config = {'apiVersion': 'v1', 'kind': 'Config', 'current-context': self.context,
                      'clusters': [{'name': 'fault', 'cluster': {
                          'server': f'http://127.0.0.1:{self.port}'}}],
                      'users': [{'name': 'fault', 'user': {}}],
                      'contexts': [{'name': self.context, 'context': {'cluster': 'fault', 'user': 'fault'}}]}
            descriptor = os.open(self.proxy_kubeconfig, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(descriptor, 'w') as output:
                json.dump(config, output, separators=(',', ':'))
            return self.proxy_kubeconfig
        except Exception:
            self.stop()
            raise

    def wait_ready(self, url, process, name):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise ValueError(f'owned {name} exited during startup')
            try:
                with self.opener.open(url, timeout=1) as response:
                    if response.status == 200:
                        return
            except (OSError, urllib.error.URLError):
                time.sleep(.1)
        raise ValueError(f'owned {name} did not become ready')

    @staticmethod
    def stop_process(process):
        if process is None:
            return
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)

    def stop(self):
        self.stop_process(self.helper_process)
        self.helper_process = None
        self.stop_process(self.kubectl_process)
        self.kubectl_process = None
