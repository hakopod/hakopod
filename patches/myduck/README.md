# Hakopod managed MyDuck runtime

This patch is reviewed against upstream commit `6e3427591fd8895df9585969e7256f958fb639bb` only. Apply it with `scripts/apply-managed-myduck-patches.py`; use `scripts/build-managed-myduck.sh source-only NEW_EMPTY_SCRATCH` for the first bounded VM validation.

The StatefulSet starts `/usr/local/bin/myduckserver --managed-config=/etc/hakopod-config/managed.json`. Health uses `--managed-check` with the same path. Cold restore qualification uses `--managed-empty` and succeeds only when both authenticated TLS protocols expose the same `app` database with no user tables.

The strict JSON object has schema version 1 and the fields `database`, `data_dir`, `password_file`, `tls_cert_file`, `tls_key_file`, `server_name`, `mysql_port`, `postgres_port`, `memory_limit_bytes`, `threads`, `temp_directory`, `temp_limit_bytes`, `max_connections`, and `statement_timeout_seconds`. Unknown fields and unsupported paths, ports, or bounds are rejected.

The password file is exactly 64 hexadecimal bytes. MySQL accepts only `root`; PostgreSQL accepts only `postgres`; both use that secret and TLS 1.2 or newer. Startup removes `/var/lib/myduck/mysql.bin` before recreating MySQL authentication, so a restored volume cannot replace the current Secret or restore extra accounts. Cold archives contain `app.db` and an optional `app.db.wal`, never `mysql.bin`.

DuckDB external access and extension autoload/install are disabled and its configuration is locked. Managed SQL admission also rejects account, role, plugin, replication, subscription, online backup/restore, extension, filesystem, and network operations while preserving client `COPY FROM STDIN` and `COPY TO STDOUT`.

The image runs as UID/GID 1000 and expects writable volumes at `/var/lib/myduck` and `/tmp`. Its root filesystem may be read-only with all Linux capabilities dropped. The image also contains the controlled `hakopod-myduck-storage` helper, Python 3, `/bin/sh`, and `tar`.
