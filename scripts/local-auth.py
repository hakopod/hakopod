#!/usr/bin/env python3
"""Create development installation secrets without choosing a user's identity."""
import os
from pathlib import Path
import secrets
import shlex
import shutil

root = Path(__file__).resolve().parent.parent
local = root / ".local"
local.mkdir(mode=0o700, exist_ok=True)
settings = {}
for variable, filename in [
    ("HAKOPOD_SETUP_SECRET_FILE", "setup-secret"),
    ("HAKOPOD_AUTH_ENCRYPTION_KEY_FILE", "auth-encryption-key"),
]:
    target = local / filename
    try:
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        if not target.is_file() or target.stat().st_mode & 0o077:
            raise SystemExit(f"Refusing insecure installation secret file: {filename}")
    else:
        with os.fdopen(fd, "w") as file:
            file.write(secrets.token_hex(32) + "\n")
    settings[variable] = str(target)
settings["HAKOPOD_WEB_ORIGIN"] = "http://127.0.0.1:4173"
settings["HAKOPOD_PUBLIC_HTTPS_PORT"] = "18443"
settings["HAKOPOD_K3S_SUPERVISOR_URL"] = "https://k3d-hakopod-dev-server-0:6443"
settings["HAKOPOD_BACKUP_STATE_DIR"] = str(local / "backups")
pg_dump = shutil.which("pg_dump")
if not pg_dump and Path("/opt/homebrew/opt/libpq/bin/pg_dump").is_file():
    pg_dump = "/opt/homebrew/opt/libpq/bin/pg_dump"
if pg_dump:
    settings["HAKOPOD_PG_DUMP_PATH"] = pg_dump
path = local / "env"
lines = path.read_text().splitlines() if path.exists() else []
lines = [line for line in lines if not any(line.startswith(f"export {key}=") for key in settings)]
lines += [f"export {key}={shlex.quote(value)}" for key, value in settings.items()]
path.write_text("\n".join(lines) + "\n")
path.chmod(0o600)
print("Installation setup and MFA encryption secrets are ready in restricted .local files. No user account was created.")
