#!/usr/bin/env python3
"""Run Oracle wallet prompts without putting a credential in process arguments.

The managed image contract requires Python 3 and the Oracle wallet tools. Tool
output is deliberately withheld: a native failure may echo a credential.
"""

import json
import fcntl
import os
import pty
import re
import select
import signal
import subprocess
import sys
import termios
import time


def main():
    raw = sys.stdin.buffer.read(16385)
    if len(raw) > 16384:
        raise ValueError("input exceeds its bound")
    request = json.loads(raw)
    if set(request) != {"operation", "wallet", "password", "value", "arguments"}:
        raise ValueError("invalid request")
    operation, wallet, password = (request[k] for k in ("operation", "wallet", "password"))
    value, arguments = request["value"], request["arguments"]
    if not isinstance(wallet, str) or not (wallet.startswith("/opt/oracle/") or wallet == "/tmp/dataguard-auth-wallet") or ".." in wallet or len(wallet) > 512:
        raise ValueError("invalid wallet path")
    if not isinstance(password, str) or re.fullmatch(r"[a-f0-9]{64}", password) is None:
        raise ValueError("invalid wallet credential")
    if not isinstance(arguments, list) or len(arguments) > 2 or any(not isinstance(a, str) or not a or len(a) > 4096 or "\n" in a or "\r" in a for a in arguments):
        raise ValueError("invalid tool arguments")
    if operation in ("credential", "entry"):
        if not isinstance(value, str) or re.fullmatch(r"[a-f0-9]{64}", value) is None:
            raise ValueError("invalid account credential")
    elif value != "":
        raise ValueError("unexpected account credential")
    if operation == "create" and not arguments:
        command = ["orapki", "wallet", "create", "-wallet", wallet, "-auto_login"]
    elif operation == "import" and len(arguments) == 1:
        command = ["orapki", "wallet", "import_pkcs12", "-wallet", wallet, "-pkcs12file", arguments[0]]
    elif operation == "trust" and len(arguments) == 1:
        command = ["orapki", "wallet", "add", "-wallet", wallet, "-trusted_cert", "-cert", arguments[0]]
    elif operation == "credential" and len(arguments) == 2:
        command = ["mkstore", "-wrl", wallet, "-createCredential", *arguments]
    elif operation == "entry" and len(arguments) == 1:
        command = ["mkstore", "-wrl", wallet, "-createEntry", arguments[0]]
    else:
        raise ValueError("unsupported wallet operation")
    master, slave = pty.openpty()
    modes = termios.tcgetattr(slave)
    modes[3] &= ~termios.ECHO
    termios.tcsetattr(slave, termios.TCSANOW, modes)
    def child_terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)
    child = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, close_fds=True, preexec_fn=child_terminal)
    os.close(slave)
    deadline, pending, total, prompts = time.monotonic() + 45, b"", 0, 0
    # Oracle wallet tools read their prompts from a terminal on some images.
    # Match the meaning of a prompt rather than assuming a version-specific
    # order. Unknown prompts time out; their output never reaches the caller.
    prompt = re.compile(rb"(?:enter|re-enter|reenter)[^\r\n:]{0,180}:\s*", re.I)
    try:
        while time.monotonic() < deadline:
            if not select.select([master], [], [], 0.1)[0]:
                if child.poll() is not None:
                    if child.returncode != 0 or prompts == 0:
                        raise ValueError("wallet tool failed")
                    return
                continue
            try:
                chunk = os.read(master, 4096)
            except OSError:
                child.wait(timeout=2)
                if child.returncode != 0 or prompts == 0:
                    raise ValueError("wallet tool failed")
                return
            if not chunk:
                continue
            total += len(chunk)
            if total > 65536:
                raise ValueError("wallet output exceeds its bound")
            pending = (pending + chunk)[-4096:]
            while True:
                match = prompt.search(pending)
                if match is None:
                    break
                wording = match.group().lower()
                pending = pending[match.end():]
                prompts += 1
                if prompts > 8 or b"password" not in wording and b"secret" not in wording:
                    raise ValueError("unexpected wallet prompt")
                if operation in ("credential", "entry"):
                    # Refuse ambiguous bare-password prompts for account edits.
                    # Native qualification must confirm the image's prompt text.
                    if b"wallet" in wording:
                        answer = password
                    elif b"secret" in wording or b"credential" in wording:
                        answer = value
                    else:
                        raise ValueError("ambiguous wallet prompt")
                else:
                    answer = password
                os.write(master, answer.encode("ascii") + b"\n")
        raise ValueError("wallet tool timed out")
    finally:
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
        child.wait(timeout=2)
        os.close(master)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Managed Oracle wallet operation failed.", file=sys.stderr)
        sys.exit(1)
