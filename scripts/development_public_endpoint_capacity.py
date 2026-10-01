#!/usr/bin/env python3
"""Shared, read-only host-capacity checks for database endpoint acceptance."""

import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import signal
import subprocess
import time


ROOT_RESERVE_CPU_MILLIS = 1000
ROOT_RESERVE_MEMORY_BYTES = 2 * 1024 ** 3
RUNNER_CPU_MILLIS = 1000
RUNNER_MEMORY_BYTES = 3 * 1024 ** 3
CONTROL_POSTGRES_CPU_MILLIS = 500
CONTROL_POSTGRES_MEMORY_BYTES = 512 * 1024 ** 2

TOP_FIELDS = {
    'schema_version', 'cluster', 'reviewed_by', 'root_reserve',
    'docker_workloads', 'host_workloads',
}
RESERVE_FIELDS = {'cpu_millis', 'memory_bytes', 'cgroups'}
ROOT_CGROUP_FIELDS = {'cgroup_path', 'executables'}
DOCKER_FIELDS = {'name', 'container_id', 'class', 'cpu_millis', 'memory_bytes'}
HOST_FIELDS = {'name', 'class', 'cgroup_path', 'executables', 'cpu_millis', 'memory_bytes'}
EVIDENCE_FIELDS = {
    'schema_version', 'cpu_millis', 'memory_bytes', 'online_cpus', 'reviewed_by',
    'reserved', 'docker_workloads', 'host_workloads', 'ancestor_limits',
}
DOCKER_EVIDENCE_FIELDS = {
    'name', 'container_id', 'class', 'cgroup_path', 'cpu_millis', 'memory_bytes',
    'reserved_cpu_millis', 'reserved_memory_bytes', 'cpuset', 'image_id',
}
HOST_EVIDENCE_FIELDS = {
    'name', 'class', 'cgroup_path', 'executables', 'cpu_millis', 'memory_bytes',
    'reserved_cpu_millis', 'reserved_memory_bytes', 'cpuset',
}
ANCESTOR_EVIDENCE_FIELDS = {
    'cgroup_path', 'cpu_millis', 'memory_bytes', 'claimed_cpu_millis', 'claimed_memory_bytes',
}
WORKLOAD_CLASS = re.compile(r'^[a-z][a-z0-9-]{0,63}$')
CONTAINER_ID = re.compile(r'^[a-f0-9]{64}$')


def _stop(process):
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait()


def _pump(process, *, stdin=None, stdout=b'', stderr=b'', maximum, stderr_maximum,
          timeout, allowed, stop_after_line=False):
    if stdin is not None and not isinstance(stdin, bytes):
        raise ValueError('bounded command input must be bytes')
    out, err = bytearray(stdout), bytearray(stderr)
    pending = memoryview(stdin) if stdin is not None else None
    if len(out) > maximum or len(err) > stderr_maximum:
        _stop(process)
        raise ValueError('bounded command output exceeded its limit')
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            if process.stdin is not None and not process.stdin.closed:
                os.set_blocking(process.stdin.fileno(), False)
                selector.register(process.stdin, selectors.EVENT_WRITE, 'stdin')
            for stream, name in ((process.stdout, 'stdout'), (process.stderr, 'stderr')):
                if stream is not None and not stream.closed:
                    os.set_blocking(stream.fileno(), False)
                    selector.register(stream, selectors.EVENT_READ, name)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise subprocess.TimeoutExpired(process.args, timeout)
                events = selector.select(min(remaining, 0.25))
                if not events and process.poll() is not None:
                    events = [(key, None) for key in list(selector.get_map().values())]
                for key, _ in events:
                    if key.data == 'stdin':
                        try:
                            written = os.write(key.fileobj.fileno(), pending[:65536])
                        except BrokenPipeError:
                            written = len(pending)
                        pending = pending[written:]
                        if not pending:
                            selector.unregister(key.fileobj)
                            key.fileobj.close()
                        continue
                    try:
                        block = os.read(key.fileobj.fileno(), 65536)
                    except BlockingIOError:
                        continue
                    if not block:
                        selector.unregister(key.fileobj)
                        continue
                    buffer = out if key.data == 'stdout' else err
                    buffer.extend(block)
                    limit = maximum if key.data == 'stdout' else stderr_maximum
                    if len(buffer) > limit:
                        raise ValueError('bounded command output exceeded its limit')
                if stop_after_line and b'\n' in out:
                    line, remainder = bytes(out).split(b'\n', 1)
                    return line + b'\n', remainder, bytes(err), False
        returncode = process.wait(timeout=max(0.1, deadline - time.monotonic()))
        if returncode not in allowed:
            raise ValueError('bounded command failed')
        return bytes(out), b'', bytes(err), True
    except BaseException:
        _stop(process)
        raise


def bounded_command(args, *, stdin=None, maximum, stderr_maximum=64 * 1024,
                    timeout=30, env=None, allowed=(0,)):
    process = subprocess.Popen(
        args, stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, start_new_session=True)
    try:
        stdout, remainder, _, complete = _pump(
            process, stdin=stdin, maximum=maximum, stderr_maximum=stderr_maximum,
            timeout=timeout, allowed=allowed)
        if remainder or not complete:
            raise AssertionError('bounded command did not complete')
        return stdout
    finally:
        for stream in (process.stdin, process.stdout, process.stderr):
            if stream is not None and not stream.closed:
                stream.close()


def bounded_read_line(process, *, maximum, stderr_maximum=64 * 1024, timeout=20):
    line, remainder, stderr, complete = _pump(
        process, maximum=maximum, stderr_maximum=stderr_maximum,
        timeout=timeout, allowed=(0,), stop_after_line=True)
    if complete:
        raise ValueError('bounded session ended before its initial line')
    return line, remainder, stderr


def bounded_finish(process, *, stdout=b'', stderr=b'', maximum,
                   stderr_maximum=64 * 1024, timeout=260, allowed=(0,)):
    output, remainder, _, complete = _pump(
        process, stdout=stdout, stderr=stderr, maximum=maximum,
        stderr_maximum=stderr_maximum, timeout=timeout, allowed=allowed)
    if remainder or not complete:
        raise AssertionError('bounded session did not complete')
    return output


def _strict(value, fields, label):
    if not isinstance(value, dict) or set(value) != set(fields):
        raise ValueError(f'{label} has missing or unknown fields')
    return value


def _positive(value, label):
    if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
        raise ValueError(f'{label} must be a positive integer')
    return value


def parse_cpuset(value):
    if not isinstance(value, str) or not value:
        return set()
    cpus = set()
    try:
        for part in value.split(','):
            if not part or part.count('-') > 1:
                raise ValueError
            bounds = [int(item) for item in part.split('-', 1)]
            if any(item < 0 for item in bounds) or len(bounds) == 2 and bounds[1] < bounds[0]:
                raise ValueError
            cpus.update(range(bounds[0], bounds[-1] + 1))
    except ValueError:
        raise ValueError('CPU set is invalid') from None
    return cpus


def _cgroup_path(value):
    if not isinstance(value, str) or not value.startswith('/') or '\x00' in value:
        raise ValueError('cgroup path is invalid')
    path = PurePosixPath(value)
    if '..' in path.parts or str(path) != value or len(value) > 1024:
        raise ValueError('cgroup path is invalid')
    return value


def _descendant(path, ancestor):
    return path == ancestor or path.startswith(ancestor.rstrip('/') + '/')


def _ancestors(path):
    current = PurePosixPath(path)
    values = []
    while True:
        values.append(str(current))
        if str(current) == '/':
            break
        current = current.parent
    return list(reversed(values))


def unified_cgroup(proc_root, pid):
    try:
        entries = (proc_root / str(pid) / 'cgroup').read_text().splitlines()
    except OSError:
        raise ValueError('running workload cgroup disappeared during inspection') from None
    unified = [line[3:] for line in entries if line.startswith('0::')]
    if len(unified) != 1:
        raise ValueError('acceptance requires one unified cgroup v2 hierarchy')
    return _cgroup_path(unified[0])


def docker_scope(path):
    """Return the container scope while retaining the PID path for limit checks."""
    parsed = PurePosixPath(path)
    if parsed.name == 'init' and parsed.parent.name.endswith('.scope'):
        return str(parsed.parent)
    return path


def _read(path):
    try:
        return path.read_text().strip()
    except OSError:
        raise ValueError('cgroup capacity changed during inspection') from None


def _control_snapshot(cgroup_root, path):
    base = cgroup_root / path.lstrip('/')
    result = {}
    for name in ('cpu.max', 'memory.max', 'cpuset.cpus.effective'):
        try:
            result[name] = (base / name).read_text().strip()
        except FileNotFoundError:
            if path != '/' or name == 'cpuset.cpus.effective':
                raise ValueError('cgroup capacity changed during inspection') from None
            result[name] = None
        except OSError:
            raise ValueError('cgroup capacity changed during inspection') from None
    return result


def _direct_capacity(cgroup_root, path, root_cpu_millis=None, root_memory_bytes=None,
                     snapshots=None):
    snapshot = _control_snapshot(cgroup_root, path)
    if snapshots is not None and snapshots.setdefault(path, snapshot) != snapshot:
        raise ValueError('cgroup capacity changed during inspection')
    cpuset = parse_cpuset(snapshot['cpuset.cpus.effective'])
    if not cpuset:
        raise ValueError('cgroup has no effective CPUs')
    if snapshot['cpu.max'] is None:
        cpu = _positive(root_cpu_millis, 'root CPU capacity')
    else:
        cpu_text = snapshot['cpu.max'].split()
        if len(cpu_text) != 2:
            raise ValueError('cgroup CPU quota is invalid')
        cpu = None
        if cpu_text[0] != 'max':
            try:
                quota, period = map(int, cpu_text)
            except ValueError:
                raise ValueError('cgroup CPU quota is invalid') from None
            if quota <= 0 or period <= 0:
                raise ValueError('cgroup CPU quota is invalid')
            cpu = (quota * 1000) // period
    if snapshot['memory.max'] is None:
        memory = _positive(root_memory_bytes, 'root memory capacity')
    else:
        memory_text = snapshot['memory.max']
        memory = None
        if memory_text != 'max':
            try:
                memory = int(memory_text)
            except ValueError:
                raise ValueError('cgroup memory limit is invalid') from None
            if memory <= 0:
                raise ValueError('cgroup memory limit is invalid')
    return {'cpu_millis': cpu, 'memory_bytes': memory, 'cpuset': cpuset}


def effective_limits(cgroup_root, path, online_cpus, host_memory_bytes, snapshots=None):
    cpu_limits = [len(online_cpus) * 1000]
    memory_limits = [host_memory_bytes]
    cpuset = set(online_cpus)
    quota_limited = False
    for ancestor in _ancestors(path):
        direct = _direct_capacity(cgroup_root, ancestor, len(online_cpus) * 1000,
                                  host_memory_bytes, snapshots)
        cpuset &= direct['cpuset']
        if direct['cpu_millis'] is not None:
            quota_limited = True
            cpu_limits.append(direct['cpu_millis'])
        if direct['memory_bytes'] is not None:
            memory_limits.append(direct['memory_bytes'])
    if not cpuset or not cpuset <= online_cpus:
        raise ValueError('cgroup CPU set is outside the online host CPUs')
    cpu_limits.append(len(cpuset) * 1000)
    return {
        'cpu_millis': min(cpu_limits),
        'memory_bytes': min(memory_limits),
        'cpuset': cpuset,
        'quota_limited': quota_limited,
    }


def validate_host_view(proc_root, cgroup_root):
    self_root = proc_root / str(os.getpid())
    for namespace in ('cgroup', 'mnt'):
        try:
            init_stat = os.stat(proc_root / '1' / 'ns' / namespace)
            self_stat = os.stat(self_root / 'ns' / namespace)
        except OSError:
            raise ValueError('host namespace identity is unavailable') from None
        if (init_stat.st_dev, init_stat.st_ino) != (self_stat.st_dev, self_stat.st_ino):
            raise ValueError('capacity inspection must run in the host cgroup and mount namespaces')
    try:
        lines = (self_root / 'mountinfo').read_text().splitlines()
    except OSError:
        raise ValueError('host cgroup mount inventory is unavailable') from None
    expected = str(cgroup_root.resolve())
    matches = []
    for line in lines:
        fields = line.split()
        if '-' not in fields:
            continue
        separator = fields.index('-')
        if separator + 1 >= len(fields) or fields[separator + 1] != 'cgroup2':
            continue
        root = fields[3].replace('\\040', ' ')
        mountpoint = fields[4].replace('\\040', ' ')
        if mountpoint == expected:
            matches.append(root)
    if matches != ['/']:
        raise ValueError('capacity inspection requires the host cgroup v2 mount root')


def docker_declared_limits(host):
    limits = []
    nano = host.get('NanoCpus')
    quota, period = host.get('CpuQuota'), host.get('CpuPeriod')
    if isinstance(nano, int) and not isinstance(nano, bool) and nano > 0:
        limits.append(nano // 1_000_000)
    if all(isinstance(item, int) and not isinstance(item, bool) and item > 0 for item in (quota, period)):
        limits.append((quota * 1000) // period)
    cpuset = parse_cpuset(host.get('CpusetCpus', ''))
    if cpuset:
        limits.append(len(cpuset) * 1000)
    memory = host.get('Memory')
    if not limits or not isinstance(memory, int) or isinstance(memory, bool) or memory <= 0:
        raise ValueError('Docker workloads require explicit CPU and memory limits')
    return {'cpu_millis': min(limits), 'memory_bytes': memory, 'cpuset': cpuset}


def _executables(proc_root, path):
    names = set()
    for process in proc_root.iterdir():
        if not process.name.isdigit():
            continue
        try:
            if unified_cgroup(proc_root, int(process.name)) != path:
                continue
            try:
                names.add((process / 'exe').resolve(strict=True).name)
            except OSError:
                names.add('[kernel]')
        except ValueError:
            continue
    return names


def host_process_inventory(proc_root, docker_roots):
    inventory = {}
    for process in proc_root.iterdir():
        if not process.name.isdigit():
            continue
        try:
            path = unified_cgroup(proc_root, int(process.name))
        except ValueError:
            continue
        if any(_descendant(path, root) for root in docker_roots):
            continue
        try:
            executable = (process / 'exe').resolve(strict=True).name
        except OSError:
            executable = '[kernel]'
        inventory.setdefault(path, set()).add(executable)
    return inventory


def validate_budget(value, context):
    _strict(value, TOP_FIELDS, 'host budget')
    if value['schema_version'] != 1 or value['cluster'] != context or value['reviewed_by'] != ['root']:
        raise ValueError('host budget requires schema version 1 and root review for the named cluster')
    reserve = _strict(value['root_reserve'], RESERVE_FIELDS, 'root reserve')
    if (_positive(reserve['cpu_millis'], 'root reserve CPU') != ROOT_RESERVE_CPU_MILLIS or
            _positive(reserve['memory_bytes'], 'root reserve memory') != ROOT_RESERVE_MEMORY_BYTES):
        raise ValueError('host budget must keep the fixed root operating-system reserve')
    if not isinstance(reserve['cgroups'], list) or len(reserve['cgroups']) != 2:
        raise ValueError('root reserve must inventory the root and init cgroups')
    root_groups = {}
    for item in reserve['cgroups']:
        _strict(item, ROOT_CGROUP_FIELDS, 'root reserve cgroup')
        path = _cgroup_path(item['cgroup_path'])
        if (not isinstance(item['executables'], list) or not item['executables'] or
                any(not isinstance(name, str) or not name or len(name) > 256 for name in item['executables'])):
            raise ValueError('root reserve executable inventory is invalid')
        if path in root_groups or len(set(item['executables'])) != len(item['executables']):
            raise ValueError('root reserve cgroup inventory is duplicated')
        root_groups[path] = set(item['executables'])
    if root_groups != {'/': {'[kernel]'}, '/init.scope': {'systemd'}}:
        raise ValueError('root reserve can cover only kernel threads and systemd in the exact root cgroups')

    docker = {}
    container_ids = set()
    if not isinstance(value['docker_workloads'], list) or not value['docker_workloads']:
        raise ValueError('host budget requires an explicit Docker workload inventory')
    for item in value['docker_workloads']:
        _strict(item, DOCKER_FIELDS, 'Docker workload')
        if (not isinstance(item['name'], str) or not item['name'] or len(item['name']) > 255 or
                not isinstance(item['container_id'], str) or not CONTAINER_ID.fullmatch(item['container_id']) or
                not isinstance(item['class'], str) or not WORKLOAD_CLASS.fullmatch(item['class'])):
            raise ValueError('Docker workload identity is invalid')
        _positive(item['cpu_millis'], 'Docker workload CPU reservation')
        _positive(item['memory_bytes'], 'Docker workload memory reservation')
        if item['name'] in docker or item['container_id'] in container_ids:
            raise ValueError('Docker workload inventory is duplicated')
        docker[item['name']] = item
        container_ids.add(item['container_id'])

    host = {}
    host_names = set()
    if not isinstance(value['host_workloads'], list) or not value['host_workloads']:
        raise ValueError('host budget requires an explicit non-Docker host workload inventory')
    for item in value['host_workloads']:
        _strict(item, HOST_FIELDS, 'host workload')
        path = _cgroup_path(item['cgroup_path'])
        if path in ('/', '/init.scope') or any(_descendant(path, other) or _descendant(other, path) for other in host):
            raise ValueError('host workload cgroups must be distinct non-overlapping leaves')
        if (not isinstance(item['name'], str) or not item['name'] or len(item['name']) > 255 or
                item['name'] in docker or item['name'] in host_names or
                not isinstance(item['class'], str) or not WORKLOAD_CLASS.fullmatch(item['class']) or
                not isinstance(item['executables'], list) or not item['executables'] or
                any(not isinstance(name, str) or not name or len(name) > 256 for name in item['executables']) or
                len(set(item['executables'])) != len(item['executables'])):
            raise ValueError('host workload identity is invalid')
        _positive(item['cpu_millis'], 'host workload CPU reservation')
        _positive(item['memory_bytes'], 'host workload memory reservation')
        host[path] = item
        host_names.add(item['name'])
    if sum(item['class'] == 'acceptance-runner' for item in host.values()) != 1:
        raise ValueError('host budget requires exactly one acceptance-runner workload')
    if sum(item['class'] == 'control-postgres' for item in list(host.values()) + list(docker.values())) != 1:
        raise ValueError('host budget requires exactly one control-postgres workload')
    runner = next(item for item in host.values() if item['class'] == 'acceptance-runner')
    if (runner['cpu_millis'] != RUNNER_CPU_MILLIS or runner['memory_bytes'] != RUNNER_MEMORY_BYTES):
        raise ValueError('acceptance-runner reservation must match its required systemd limits')
    control = next(item for item in list(host.values()) + list(docker.values())
                   if item['class'] == 'control-postgres')
    if (control['cpu_millis'] < CONTROL_POSTGRES_CPU_MILLIS or
            control['memory_bytes'] < CONTROL_POSTGRES_MEMORY_BYTES):
        raise ValueError('control PostgreSQL reservation is too small')
    return root_groups, docker, host


def inspect(docker, command, budget, nodes, *, context, proc_root=Path('/proc'),
            cgroup_root=Path('/sys/fs/cgroup'), online_path=Path('/sys/devices/system/cpu/online')):
    root_groups, expected_docker, expected_host = validate_budget(budget, context)
    validate_host_view(proc_root, cgroup_root)
    online = parse_cpuset(online_path.read_text().strip())
    if not online:
        raise ValueError('online host CPU inventory is empty')
    info = json.loads(command([docker, 'info', '--format', '[{{json .NCPU}},{{json .MemTotal}}]'],
                              maximum=256, timeout=20))
    if (not isinstance(info, list) or len(info) != 2 or
            not isinstance(info[0], int) or isinstance(info[0], bool) or info[0] <= 0 or
            not isinstance(info[1], int) or isinstance(info[1], bool) or info[1] <= 0):
        raise ValueError('Docker host capacity inventory is invalid')
    if len(online) > info[0]:
        raise ValueError('online CPU inventory exceeds Docker host capacity')
    ids = command([docker, 'ps', '--no-trunc', '--quiet'], maximum=1024 * 1024, timeout=20).decode().splitlines()
    if any(not CONTAINER_ID.fullmatch(item) for item in ids) or len(ids) != len(set(ids)):
        raise ValueError('running Docker container inventory is invalid')
    values = json.loads(command([docker, 'inspect', *ids], maximum=4 * 1024 * 1024, timeout=20)) if ids else []
    if not isinstance(values, list) or len(values) != len(ids):
        raise ValueError('Docker workload inventory changed during inspection')

    cgroup_snapshots = {}
    actual_docker = {}
    docker_roots = set()
    claims = []
    node_result = {}
    node_configured_cpusets = {}
    docker_evidence = []
    for item in values:
        name = item.get('Name', '').lstrip('/')
        container_id = item.get('Id')
        if (not isinstance(container_id, str) or container_id not in ids or name in actual_docker or
                item.get('State', {}).get('Running') is not True):
            raise ValueError('Docker workload inventory changed during inspection')
        expected = expected_docker.get(name)
        if expected is None or expected['container_id'] != container_id:
            raise ValueError('unknown or replaced running Docker workload')
        pid = item.get('State', {}).get('Pid')
        if not isinstance(pid, int) or isinstance(pid, bool) or pid <= 0:
            raise ValueError('running Docker workload has no host PID')
        path = unified_cgroup(proc_root, pid)
        scope = docker_scope(path)
        if scope in docker_roots:
            raise ValueError('Docker workloads share an unexpected leaf cgroup')
        docker_roots.add(scope)
        actual = effective_limits(cgroup_root, path, online, info[1], cgroup_snapshots)
        declared = docker_declared_limits(item.get('HostConfig') or {})
        configured_cpuset = declared['cpuset']
        if configured_cpuset:
            if not configured_cpuset <= online:
                raise ValueError('configured Docker CPU set is outside online host CPUs')
            for ancestor in _ancestors(path):
                if not configured_cpuset <= _direct_capacity(
                        cgroup_root, ancestor, len(online) * 1000, info[1],
                        cgroup_snapshots)['cpuset']:
                    raise ValueError('configured Docker CPU set is outside an effective ancestor CPU set')
        if (actual['cpu_millis'] > declared['cpu_millis'] or actual['memory_bytes'] > declared['memory_bytes'] or
                configured_cpuset and not actual['cpuset'] <= configured_cpuset):
            raise ValueError('actual Docker cgroup limits exceed the Docker declaration')
        if expected['cpu_millis'] < actual['cpu_millis'] or expected['memory_bytes'] < actual['memory_bytes']:
            raise ValueError('Docker workload reservation is below its actual effective limits')
        evidence = {
            'name': name, 'container_id': container_id, 'class': expected['class'],
            'cgroup_path': path, 'cpu_millis': actual['cpu_millis'],
            'memory_bytes': actual['memory_bytes'], 'reserved_cpu_millis': expected['cpu_millis'],
            'reserved_memory_bytes': expected['memory_bytes'], 'cpuset': sorted(actual['cpuset']),
            'image_id': item.get('Image', ''),
        }
        docker_evidence.append(evidence)
        claims.append((path, expected['cpu_millis'], expected['memory_bytes'], name))
        actual_docker[name] = item
        if name in nodes:
            labels = item.get('Config', {}).get('Labels') or {}
            image_id = item.get('Image', '')
            if (expected['class'] != 'development-node' or labels.get('app') != 'k3d' or
                    labels.get('k3d.cluster') != budget['cluster'].removeprefix('k3d-') or
                    labels.get('k3d.role') != nodes[name] or
                    not isinstance(image_id, str) or not re.fullmatch(r'sha256:[a-f0-9]{64}', image_id)):
                raise ValueError('Docker node ownership, role or immutable image identity is invalid')
            node_result[name] = {
                'role': nodes[name], 'cpus': actual['cpu_millis'] / 1000,
                'cpu_millis': actual['cpu_millis'], 'memory_bytes': actual['memory_bytes'],
                'cpuset': sorted(actual['cpuset']), 'image_id': image_id,
            }
            node_configured_cpusets[name] = configured_cpuset
    if set(actual_docker) != set(expected_docker) or set(node_result) != set(nodes):
        raise ValueError('Docker budget does not exactly match all running workloads')
    configured = [item for item in node_configured_cpusets.values() if item]
    if configured and (len(configured) != len(nodes) or
                       any(left & right for index, left in enumerate(configured)
                           for right in configured[index + 1:])):
        raise ValueError('development node Docker CPU sets must be complete and non-overlapping')

    actual_host = host_process_inventory(proc_root, docker_roots)
    expected_paths = set(root_groups) | set(expected_host)
    if set(actual_host) != expected_paths:
        raise ValueError('unknown or missing non-Docker host workload class')
    host_evidence = []
    for path, executables in actual_host.items():
        if path in root_groups:
            if executables != root_groups[path]:
                raise ValueError('root reserve process inventory changed')
            continue
        expected = expected_host[path]
        if executables != set(expected['executables']):
            raise ValueError('non-Docker host workload executable inventory changed')
        actual = effective_limits(cgroup_root, path, online, info[1], cgroup_snapshots)
        if (not actual['quota_limited'] or actual['memory_bytes'] >= info[1] or
                expected['cpu_millis'] < actual['cpu_millis'] or
                expected['memory_bytes'] < actual['memory_bytes']):
            raise ValueError('non-Docker host workload is unbounded or exceeds its reservation')
        evidence = {
            'name': expected['name'], 'class': expected['class'], 'cgroup_path': path,
            'executables': sorted(executables), 'cpu_millis': actual['cpu_millis'],
            'memory_bytes': actual['memory_bytes'], 'reserved_cpu_millis': expected['cpu_millis'],
            'reserved_memory_bytes': expected['memory_bytes'], 'cpuset': sorted(actual['cpuset']),
        }
        host_evidence.append(evidence)
        claims.append((path, expected['cpu_millis'], expected['memory_bytes'], expected['name']))
    runner_path = unified_cgroup(proc_root, os.getpid())
    if expected_host.get(runner_path, {}).get('class') != 'acceptance-runner':
        raise ValueError('planner is outside the reviewed acceptance-runner cgroup')
    final_ids = command([docker, 'ps', '--no-trunc', '--quiet'],
                        maximum=1024 * 1024, timeout=20).decode().splitlines()
    final_values = json.loads(command([docker, 'inspect', *final_ids],
                                      maximum=4 * 1024 * 1024, timeout=20)) if final_ids else []
    final_host = host_process_inventory(proc_root, docker_roots)
    def fingerprint(item):
        return (item.get('Id'), item.get('Name'), item.get('Image'), item.get('State', {}).get('Pid'),
                item.get('State', {}).get('Running'), item.get('HostConfig'),
                (item.get('Config', {}).get('Labels') or {}))
    if (len(final_ids) != len(ids) or set(final_ids) != set(ids) or
            not isinstance(final_values, list) or
            sorted((fingerprint(item) for item in final_values), key=repr) !=
            sorted((fingerprint(item) for item in values), key=repr) or final_host != actual_host):
        raise ValueError('host workload inventory changed during inspection')

    ancestors = sorted({ancestor for path, _, _, _ in claims for ancestor in _ancestors(path)},
                       key=lambda item: (item.count('/'), item))
    ancestor_evidence = []
    for ancestor in ancestors:
        direct = _direct_capacity(cgroup_root, ancestor, len(online) * 1000,
                                  info[1], cgroup_snapshots)
        cpu_claim = sum(cpu for path, cpu, _, _ in claims if _descendant(path, ancestor))
        memory_claim = sum(memory for path, _, memory, _ in claims if _descendant(path, ancestor))
        if ancestor == '/':
            cpu_claim += ROOT_RESERVE_CPU_MILLIS
            memory_claim += ROOT_RESERVE_MEMORY_BYTES
        cpu_capacity = min(len(direct['cpuset']) * 1000,
                           direct['cpu_millis'] if direct['cpu_millis'] is not None else len(online) * 1000)
        memory_capacity = direct['memory_bytes'] if direct['memory_bytes'] is not None else info[1]
        if cpu_claim > cpu_capacity or memory_claim > memory_capacity:
            raise ValueError('reviewed workload reservations exceed a shared ancestor cgroup budget')
        ancestor_evidence.append({
            'cgroup_path': ancestor, 'cpu_millis': cpu_capacity, 'memory_bytes': memory_capacity,
            'claimed_cpu_millis': cpu_claim, 'claimed_memory_bytes': memory_claim,
        })
    for path, snapshot in cgroup_snapshots.items():
        if _control_snapshot(cgroup_root, path) != snapshot:
            raise ValueError('cgroup capacity changed during inspection')
    root_limit = next(item for item in ancestor_evidence if item['cgroup_path'] == '/')
    reserved = {
        'root_cpu_millis': ROOT_RESERVE_CPU_MILLIS,
        'root_memory_bytes': ROOT_RESERVE_MEMORY_BYTES,
        'runner_cpu_millis': RUNNER_CPU_MILLIS,
        'runner_memory_bytes': RUNNER_MEMORY_BYTES,
        'control_postgres_cpu_millis': CONTROL_POSTGRES_CPU_MILLIS,
        'control_postgres_memory_bytes': CONTROL_POSTGRES_MEMORY_BYTES,
    }
    evidence = {
        'schema_version': 1, 'cpu_millis': root_limit['cpu_millis'],
        'memory_bytes': root_limit['memory_bytes'], 'online_cpus': sorted(online),
        'reviewed_by': ['root'], 'reserved': reserved,
        'docker_workloads': sorted(docker_evidence, key=lambda item: item['name']),
        'host_workloads': sorted(host_evidence, key=lambda item: item['name']),
        'ancestor_limits': ancestor_evidence,
    }
    return node_result, evidence


def validate_plan_evidence(value, nodes):
    if not isinstance(value, dict) or set(value) != EVIDENCE_FIELDS or value.get('schema_version') != 1:
        return False
    expected_reserved = {
        'root_cpu_millis': ROOT_RESERVE_CPU_MILLIS,
        'root_memory_bytes': ROOT_RESERVE_MEMORY_BYTES,
        'runner_cpu_millis': RUNNER_CPU_MILLIS,
        'runner_memory_bytes': RUNNER_MEMORY_BYTES,
        'control_postgres_cpu_millis': CONTROL_POSTGRES_CPU_MILLIS,
        'control_postgres_memory_bytes': CONTROL_POSTGRES_MEMORY_BYTES,
    }
    if (value.get('reviewed_by') != ['root'] or value.get('reserved') != expected_reserved or
            not isinstance(value.get('online_cpus'), list) or not value['online_cpus'] or
            any(not isinstance(cpu, int) or isinstance(cpu, bool) or cpu < 0 for cpu in value['online_cpus']) or
            len(value['online_cpus']) != len(set(value['online_cpus'])) or
            not isinstance(value.get('cpu_millis'), int) or value['cpu_millis'] <= 0 or
            not isinstance(value.get('memory_bytes'), int) or value['memory_bytes'] <= 0):
        return False
    docker = value.get('docker_workloads')
    host = value.get('host_workloads')
    ancestors = value.get('ancestor_limits')
    if (not isinstance(docker, list) or not docker or not isinstance(host, list) or not host or
            not isinstance(ancestors, list) or not ancestors):
        return False
    claims = []
    docker_by_name = {}
    identifiers = set()
    for item in docker:
        if not isinstance(item, dict) or set(item) != DOCKER_EVIDENCE_FIELDS:
            return False
        if (not isinstance(item['name'], str) or item['name'] in docker_by_name or
                not isinstance(item['container_id'], str) or not CONTAINER_ID.fullmatch(item['container_id']) or
                item['container_id'] in identifiers or not isinstance(item['class'], str) or
                not WORKLOAD_CLASS.fullmatch(item['class']) or not isinstance(item['image_id'], str) or
                not re.fullmatch(r'sha256:[a-f0-9]{64}', item['image_id'])):
            return False
        identifiers.add(item['container_id'])
        try:
            path = _cgroup_path(item['cgroup_path'])
        except ValueError:
            return False
        for key in ('cpu_millis', 'memory_bytes', 'reserved_cpu_millis', 'reserved_memory_bytes'):
            if not isinstance(item[key], int) or isinstance(item[key], bool) or item[key] <= 0:
                return False
        if (item['cpu_millis'] > item['reserved_cpu_millis'] or
                item['memory_bytes'] > item['reserved_memory_bytes'] or
                not isinstance(item['cpuset'], list) or not item['cpuset'] or
                any(not isinstance(cpu, int) or isinstance(cpu, bool) or cpu not in value['online_cpus']
                    for cpu in item['cpuset']) or len(item['cpuset']) != len(set(item['cpuset']))):
            return False
        docker_by_name[item['name']] = item
        claims.append((path, item['reserved_cpu_millis'], item['reserved_memory_bytes']))
    if set(nodes) - set(docker_by_name):
        return False
    for name, node in nodes.items():
        item = docker_by_name[name]
        if (item['class'] != 'development-node' or item['cpu_millis'] != node.get('cpu_millis') or
                item['memory_bytes'] != node.get('memory_bytes') or item['cpuset'] != node.get('cpuset') or
                item['image_id'] != node.get('image_id')):
            return False
    host_names = set()
    for item in host:
        if not isinstance(item, dict) or set(item) != HOST_EVIDENCE_FIELDS:
            return False
        if (not isinstance(item['name'], str) or item['name'] in host_names or
                not isinstance(item['class'], str) or not WORKLOAD_CLASS.fullmatch(item['class']) or
                not isinstance(item['executables'], list) or not item['executables'] or
                any(not isinstance(name, str) or not name for name in item['executables']) or
                len(item['executables']) != len(set(item['executables']))):
            return False
        host_names.add(item['name'])
        try:
            path = _cgroup_path(item['cgroup_path'])
        except ValueError:
            return False
        for key in ('cpu_millis', 'memory_bytes', 'reserved_cpu_millis', 'reserved_memory_bytes'):
            if not isinstance(item[key], int) or isinstance(item[key], bool) or item[key] <= 0:
                return False
        if (item['cpu_millis'] > item['reserved_cpu_millis'] or
                item['memory_bytes'] > item['reserved_memory_bytes'] or
                not isinstance(item['cpuset'], list) or not item['cpuset'] or
                any(not isinstance(cpu, int) or isinstance(cpu, bool) or cpu not in value['online_cpus']
                    for cpu in item['cpuset']) or len(item['cpuset']) != len(set(item['cpuset']))):
            return False
        claims.append((path, item['reserved_cpu_millis'], item['reserved_memory_bytes']))
    if (sum(item['class'] == 'acceptance-runner' for item in host) != 1 or
            sum(item['class'] == 'control-postgres' for item in docker + host) != 1):
        return False
    expected_ancestors = {ancestor for path, _, _ in claims for ancestor in _ancestors(path)}
    if len(ancestors) != len(expected_ancestors):
        return False
    ancestor_by_path = {}
    for item in ancestors:
        if not isinstance(item, dict) or set(item) != ANCESTOR_EVIDENCE_FIELDS:
            return False
        try:
            path = _cgroup_path(item['cgroup_path'])
        except ValueError:
            return False
        if path in ancestor_by_path:
            return False
        for key in ('cpu_millis', 'memory_bytes', 'claimed_cpu_millis', 'claimed_memory_bytes'):
            if not isinstance(item[key], int) or isinstance(item[key], bool) or item[key] <= 0:
                return False
        cpu_claim = sum(cpu for claim_path, cpu, _ in claims if _descendant(claim_path, path))
        memory_claim = sum(memory for claim_path, _, memory in claims if _descendant(claim_path, path))
        if path == '/':
            cpu_claim += ROOT_RESERVE_CPU_MILLIS
            memory_claim += ROOT_RESERVE_MEMORY_BYTES
        if (item['claimed_cpu_millis'] != cpu_claim or item['claimed_memory_bytes'] != memory_claim or
                cpu_claim > item['cpu_millis'] or memory_claim > item['memory_bytes']):
            return False
        ancestor_by_path[path] = item
    root = ancestor_by_path.get('/')
    return (set(ancestor_by_path) == expected_ancestors and root is not None and
            root['cpu_millis'] == value['cpu_millis'] and root['memory_bytes'] == value['memory_bytes'])
