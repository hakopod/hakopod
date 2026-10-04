#!/usr/bin/env python3
"""Bounded local TCP relay through an identity-bound Kubernetes exec stream."""
import os
import re
import select
import signal
import socket
import subprocess
import threading
import time


BUFFER_LIMIT = 64 << 10
UID = re.compile(r"^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$")
NAME = re.compile(r"^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$")
IMAGE = re.compile(r"^[^\s@]+@sha256:[0-9a-f]{64}$")

class TargetChanged(Exception):
    pass

class TargetUnavailable(Exception):
    """An owned workload is between stable revisions; reject this connection."""
    pass


def _require(value, message):
    if not value:
        raise RuntimeError(message)


def _transfer(client, process, stop, hard_timeout, idle_timeout):
    client.setblocking(False)
    os.set_blocking(process.stdin.fileno(), False)
    os.set_blocking(process.stdout.fileno(), False)
    incoming, outgoing = bytearray(), bytearray()
    client_eof = server_eof = input_closed = False
    hard_deadline = time.monotonic() + hard_timeout
    idle_deadline = time.monotonic() + idle_timeout
    while not stop.is_set() and time.monotonic() < min(hard_deadline, idle_deadline):
        reads = ([] if client_eof or len(incoming) >= BUFFER_LIMIT else [client])
        reads += ([] if server_eof or len(outgoing) >= BUFFER_LIMIT else [process.stdout])
        writes = ([process.stdin] if incoming and not input_closed else []) + ([client] if outgoing else [])
        remaining = max(0, min(hard_deadline, idle_deadline) - time.monotonic())
        ready, writable, _ = select.select(reads, writes, [], min(1, remaining))
        if ready or writable:
            idle_deadline = time.monotonic() + idle_timeout
        if client in ready:
            data = client.recv(min(32768, BUFFER_LIMIT - len(incoming)))
            incoming.extend(data)
            client_eof = not data
        if process.stdout in ready:
            data = os.read(process.stdout.fileno(), min(32768, BUFFER_LIMIT - len(outgoing)))
            outgoing.extend(data)
            server_eof = not data
        if process.stdin in writable:
            del incoming[:os.write(process.stdin.fileno(), incoming)]
        if client in writable:
            del outgoing[:client.send(outgoing)]
        if client_eof and not incoming and not input_closed:
            process.stdin.close()
            input_closed = True
        if server_eof and not outgoing:
            return


class OwnedPodForward:
    """Relay localhost connections to a callback-selected immutable Pod target."""

    REQUIRED = frozenset(("kubeconfig", "context", "namespace", "namespace_uid", "pod", "pod_uid",
                          "owner_kind", "owner_name", "owner_uid", "workload_kind", "workload_name",
                          "workload_uid", "container", "image", "port"))

    def __init__(self, listen_port, target_callback, max_connections, lifetime=900,
                 idle_timeout=90, stream_timeout=120, kubectl="kubectl"):
        _require(type(listen_port) is int and 0 <= listen_port <= 65535, "invalid relay listen port")
        _require(callable(target_callback), "relay target callback is required")
        _require(type(max_connections) is int and 1 <= max_connections <= 136, "invalid relay connection bound")
        _require(1 <= lifetime <= 7200 and 1 <= idle_timeout <= 300 and 1 <= stream_timeout <= 900,
                 "invalid relay timeout bound")
        self.listen_port, self.target_callback, self.max_connections = listen_port, target_callback, max_connections
        self.lifetime, self.idle_timeout, self.stream_timeout, self.kubectl = lifetime, idle_timeout, stream_timeout, kubectl
        self.stop = threading.Event()
        self.listener = None
        self.workers = set()
        self.processes = set()
        self.error = None
        self.lock = threading.Lock()
        self.semaphore = threading.BoundedSemaphore(max_connections)

    def _target(self):
        value = self.target_callback()
        _require(isinstance(value, dict) and set(value) == self.REQUIRED, "relay target fields differ")
        for key in self.REQUIRED - {"port"}:
            _require(isinstance(value[key], str) and value[key], "relay target identity is incomplete")
        _require(type(value["port"]) is int and 1 <= value["port"] <= 65535, "relay target port is invalid")
        _require(value["context"] == "k3d-hakopod-dev" and os.path.isabs(value["kubeconfig"]), "relay cluster binding is invalid")
        _require(all(UID.fullmatch(value[key]) for key in ("namespace_uid", "pod_uid", "owner_uid", "workload_uid")), "relay UID binding is invalid")
        _require(all(NAME.fullmatch(value[key]) for key in ("namespace", "pod", "owner_name", "workload_name", "container")), "relay resource name is invalid")
        _require(value["owner_kind"] in ("ReplicaSet", "StatefulSet") and value["workload_kind"] in ("Deployment", "StatefulSet"), "relay ownership kind is invalid")
        _require(IMAGE.fullmatch(value["image"]), "relay image is not digest pinned")
        return value

    def _close_process(self, process):
        if process is None:
            return
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=3)
        for stream in (process.stdin, process.stdout):
            if stream is not None and not stream.closed:
                stream.close()

    def _record_error(self, error):
        with self.lock:
            if self.error is None:
                self.error = RuntimeError("owned Pod relay failed: " + str(error))
        self.stop.set()

    def check_healthy(self):
        with self.lock:
            error = self.error
        if error is not None:
            raise error
        return True

    def _forward(self, client):
        process = None
        try:
            identity = self._target()
            command = [self.kubectl, "--request-timeout=125s", "--kubeconfig", identity["kubeconfig"],
                       "--context", identity["context"], "-n", identity["namespace"], "exec", "-i",
                       identity["pod"], "-c", identity["container"], "--", "/bin/bash", "-ec",
                       "exec 3<>/dev/tcp/127.0.0.1/" + str(identity["port"]) + "; cat <&3 & reader=$!; trap 'kill \"$reader\" 2>/dev/null || true; wait \"$reader\" 2>/dev/null || true' EXIT; cat >&3"]
            command[1] = "--request-timeout=" + str(self.stream_timeout + 5) + "s"
            process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                       stderr=subprocess.DEVNULL, start_new_session=True, bufsize=0)
            with self.lock:
                self.processes.add(process)
            if self._target() != identity: raise TargetChanged()
            _transfer(client, process, self.stop, self.stream_timeout, self.idle_timeout)
            if self._target() != identity: raise TargetChanged()
        except (TargetChanged, TargetUnavailable):
            pass
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:
            self._record_error(error)
        finally:
            try:
                client.close()
                try:
                    self._close_process(process)
                except Exception as error:
                    self._record_error(error)
            finally:
                with self.lock:
                    self.processes.discard(process)
                    self.workers.discard(threading.current_thread())
                self.semaphore.release()

    def start(self):
        _require(self.listener is None, "relay is already started")
        listener = socket.socket()
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", self.listen_port))
        listener.listen(min(self.max_connections, 128))
        listener.settimeout(1)
        self.listener = listener
        self.listen_port = listener.getsockname()[1]
        deadline = time.monotonic() + self.lifetime
        def serve():
            try:
                while not self.stop.is_set() and time.monotonic() < deadline:
                    try:
                        client, _ = listener.accept()
                    except socket.timeout:
                        continue
                    except OSError:
                        break
                    if not self.semaphore.acquire(False):
                        client.close()
                        continue
                    worker = threading.Thread(target=self._forward, args=(client,), daemon=True)
                    with self.lock:
                        self.workers.add(worker)
                    worker.start()
            finally:
                self.stop.set()
        self.server = threading.Thread(target=serve, daemon=True)
        self.server.start()
        return self

    def close(self):
        self.stop.set()
        if self.listener is not None:
            self.listener.close()
        if hasattr(self, "server"):
            self.server.join(timeout=3)
        with self.lock:
            workers = list(self.workers)
            processes = list(self.processes)
        for process in processes:
            if process.poll() is None:
                try: os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError: pass
        deadline = time.monotonic() + 6
        for worker in workers:
            worker.join(timeout=max(0, deadline-time.monotonic()))
        self.listener = None

    def __enter__(self):
        return self.start()

    def __exit__(self, *_):
        self.close()
        if not _[0]:
            self.check_healthy()
