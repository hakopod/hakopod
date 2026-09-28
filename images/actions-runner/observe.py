#!/usr/bin/python3
"""Forward only the upstream runner's masked job paging log, never diagnostics.

The diagnostic JobRunner identity selects the job-level paging file. Child step
pages are deliberately ignored to avoid duplicate output. Collection isbest
effort: GitHub remains the source for completed logs if a page rotated before
we opened it. No runner token or job environment is serialized.
"""
import json
import os
import re
import signal
import stat
import subprocess
import sys
import threading
import time

PREFIX = 'HAKOPOD_WORKFLOW_V1 '
ROOT = '/home/runner'
STATE = ROOT + '/.hakopod-observation'
JOB_ID = re.compile(r'^\[WORKER [^\]]+ INFO JobRunner\] Job ID ([0-9a-fA-F-]{36})$')
FIELDS = {'repository': 'GITHUB_REPOSITORY', 'workflow': 'GITHUB_WORKFLOW',
          'run_id': 'GITHUB_RUN_ID', 'run_number': 'GITHUB_RUN_NUMBER',
          'attempt': 'GITHUB_RUN_ATTEMPT', 'job_key': 'GITHUB_JOB',
          'branch': 'GITHUB_REF_NAME', 'sha': 'GITHUB_SHA'}


def read_regular(path, limit, *, projected_config=False):
    # Kubernetes projects this trusted read-only Secret volume through symlinks.
    # All job-controlled observation and log paths still reject symlinks.
    flags = os.O_RDONLY | os.O_NONBLOCK
    if not projected_config:
        flags |= os.O_NOFOLLOW
    fd = os.open(path, flags)
    with os.fdopen(fd, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise ValueError('not a regular observation')
        return stream.read(limit + 1)


def hook():
    os.makedirs(STATE, mode=0o700, exist_ok=True)
    data = {key: os.environ.get(env, '')[:256] for key, env in FIELDS.items()}
    for key in ('run_id', 'run_number', 'attempt'):
        data[key] = int(data[key]) if data[key].isdigit() else 0
    temp = STATE + '/job.tmp'
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(data, stream)
    os.replace(temp, STATE + '/job.json')


class Collector:
    def __init__(self, emit):
        self.emit = emit
        self.identity = None
        self.pages_fd = None
        self.files = {}
        self.closed = set()
        self.line = 0
        self.metadata = None
        self.last_metadata = 0
        self.total = 0
        self.truncated = False

    def tick(self):
        try:
            raw = read_regular(STATE + '/job.json', 4096)
            if len(raw) <= 4096:
                self.metadata = json.loads(raw)
        except (OSError, ValueError):
            pass
        if self.metadata and time.monotonic() - self.last_metadata > 3:
            self.emit({'job': self.metadata})
            self.last_metadata = time.monotonic()
        if not self.identity or self.truncated:
            return
        if self.pages_fd is None:
            root = os.open(ROOT, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                diag = os.open('_diag', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=root)
                try:
                    self.pages_fd = os.open('pages', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=diag)
                finally:
                    os.close(diag)
            finally:
                os.close(root)
        pattern = re.compile(r'^[0-9a-f-]{36}_' + self.identity + r'_(\d+)\.log$')
        names = []
        with os.scandir(self.pages_fd) as entries:
            for index, entry in enumerate(entries):
                if index >= 512:
                    self.truncated = True
                    self.emit({'truncated': True})
                    break
                match = pattern.fullmatch(entry.name)
                if match:
                    names.append((int(match.group(1)), entry.name))
        for _, path in sorted(names)[:128]:
            if path in self.files or path in self.closed or len(self.files) >= 4:
                continue
            try:
                fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=self.pages_fd)
                if not stat.S_ISREG(os.fstat(fd).st_mode):
                    os.close(fd)
                    continue
                self.files[path] = (os.fdopen(fd, 'rb', buffering=0), b'')
            except OSError:
                continue
        had_output = False
        for path, (stream, pending) in list(self.files.items()):
            data = stream.read(65536)
            had_output = had_output or bool(data)
            self.total += len(data)
            if self.total > 32 * 1024 * 1024:
                self.truncated = True
                self.emit({'truncated': True})
                break
            pending += data
            while b'\n' in pending or len(pending) > 16384:
                split = pending.find(b'\n', 0, 16384)
                split = split + 1 if split >= 0 else min(16384, len(pending))
                line, pending = pending[:split], pending[split:]
                self.line += 1
                self.emit({'line': self.line, 'text': line.decode('utf-8-sig', errors='replace').rstrip('\r\n')})
            self.files[path] = (stream, pending)
            if not data and os.fstat(stream.fileno()).st_nlink == 0:
                if pending:
                    self.line += 1
                    self.emit({'line': self.line, 'text': pending.decode('utf-8-sig', errors='replace')})
                stream.close()
                self.closed.add(path)
                del self.files[path]
        if self.metadata and had_output:
            self.emit({'job': self.metadata, 'truncated': self.truncated})


def run():
    lock = threading.Lock()
    def emit(data):
        with lock:
            print(PREFIX + json.dumps(data, ensure_ascii=True), flush=True)
    collector = Collector(emit)
    stopped = threading.Event()
    def collect():
        while not stopped.wait(0.05):
            try:
                collector.tick()
            except (OSError, ValueError):
                pass  # Observability must never fail the user's job.
    thread = threading.Thread(target=collect, daemon=True)
    thread.start()
    env = dict(os.environ, ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT='1',
               ACTIONS_RUNNER_HOOK_JOB_STARTED='/usr/local/bin/hakopod-job-started.sh')
    config = read_regular('/run/hakopod-jit/config', 128 * 1024, projected_config=True).decode().strip()
    child = subprocess.Popen(['./run.sh', '--jitconfig', config], env=env,
                             stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, lambda sig, frame: child.send_signal(sig))
    for raw in iter(lambda: child.stdout.readline(65536), b''):
        line = raw.decode('utf-8', errors='replace').rstrip('\r\n')
        match = JOB_ID.fullmatch(line)
        if match:
            collector.identity = match.group(1).lower()
        with lock:
            print(line, flush=True)
    code = child.wait()
    stopped.set()
    thread.join(timeout=2)
    try:
        collector.tick()
    except (OSError, ValueError):
        pass
    if collector.metadata:
        emit({'job': collector.metadata, 'finished': True, 'truncated': collector.truncated})
    return code


if __name__ == '__main__':
    if len(sys.argv) > 1 and sys.argv[1] == 'hook':
        try:
            hook()
        except (OSError, ValueError):
            pass
    else:
        sys.exit(run())
