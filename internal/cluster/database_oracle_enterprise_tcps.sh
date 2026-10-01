#!/usr/bin/env bash
set -euo pipefail
umask 077

# The licensed image must provide the Oracle Kubernetes extension, these native
# tools and Python 3. The pinned operator invokes this same script on renewal.
for tool in sqlplus lsnrctl orapki openssl python3 flock timeout; do
  command -v "$tool" >/dev/null || exit 1
done
case "${ORACLE_SID:-}" in HPDB[0-9]) ;; *) exit 1;; esac
case "${ORACLE_HOME:-}" in /opt/oracle/product/*) ;; *) exit 1;; esac
config="/opt/oracle/oradata/dbconfig/${ORACLE_SID}"
[ -d "$config" ] || exit 1
exec 9>"$config/.hakopod-tcps.lock"
flock -w 30 9

stage="$(mktemp -d "$config/.hakopod-tcps.XXXXXXXX")"
cleanup() {
  result=$?
  rm -rf -- "$stage"
  if [ "$result" -ne 0 ]; then
    # A partial renewal must not leave a plaintext or unverified listener live.
    timeout 15 lsnrctl stop >/dev/null 2>&1 || true
    echo 'Managed Oracle TCPS configuration failed.' >&2
  fi
}
trap cleanup EXIT
export HAKOPOD_WALLET_PASSWORD="$(cat /etc/hakopod-oracle-access/wallet)"
case "$HAKOPOD_WALLET_PASSWORD" in ''|*[!a-f0-9]*) exit 1;; esac
[ "${#HAKOPOD_WALLET_PASSWORD}" = 64 ] || exit 1
wallet() {
  # Environment variables are read only by this short-lived trusted helper;
  # they never become argv, SQL output, or a Kubernetes exec command value.
  python3 - "$1" "$2" "${3:-}" <<'PY' | python3 /etc/hakopod-oracle/wallet.py
import json, os, sys
print(json.dumps({"operation": sys.argv[1], "wallet": sys.argv[2], "password": os.environ["HAKOPOD_WALLET_PASSWORD"], "value": "", "arguments": [sys.argv[3]] if sys.argv[3] else []}))
PY
}
mkdir "$stage/server" "$stage/client"
timeout 30 openssl pkcs12 -export -in /etc/hakopod-oracle-tls/tls.crt -inkey /etc/hakopod-oracle-tls/tls.key \
  -certfile /etc/hakopod-oracle-tls/ca.crt -out "$stage/identity.p12" \
  -passout env:HAKOPOD_WALLET_PASSWORD >/dev/null 2>&1
wallet create "$stage/server"
wallet import "$stage/server" "$stage/identity.p12"
wallet create "$stage/client"
wallet trust "$stage/client" /etc/hakopod-oracle-tls/ca.crt
rm "$stage/identity.p12"
unset HAKOPOD_WALLET_PASSWORD

cat > "$stage/listener.ora" <<EOF
LISTENER=(DESCRIPTION_LIST=(DESCRIPTION=(ADDRESS=(PROTOCOL=IPC)(KEY=HAKOPOD))(ADDRESS=(PROTOCOL=TCPS)(HOST=0.0.0.0)(PORT=2484))))
SID_LIST_LISTENER=(SID_LIST=(SID_DESC=(GLOBAL_DBNAME=${ORACLE_SID}_DGMGRL)(ORACLE_HOME=${ORACLE_HOME})(SID_NAME=${ORACLE_SID})))
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=${config}/.tls-wallet)))
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
ADMIN_RESTRICTIONS_LISTENER=ON
INBOUND_CONNECT_TIMEOUT_LISTENER=10
EOF
cat > "$stage/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=${config}/.tls-wallet)))
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_SERVER_DN_MATCH=YES
SSL_VERSION=1.2
SQLNET.INBOUND_CONNECT_TIMEOUT=15
SQLNET.RECV_TIMEOUT=60
SQLNET.SEND_TIMEOUT=60
SQLNET.EXPIRE_TIME=3
DISABLE_OOB=ON
EOF
cat > "$stage/client/sqlnet.ora" <<EOF
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
EOF
cat > "$stage/client/tnsnames.ora" <<EOF
APPDB=(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=${HOSTNAME})(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=APPDB))(SECURITY=(SSL_SERVER_DN_MATCH=YES)))
EOF
# Native maintenance and broker processes share the generated CA-only wallet.
# The public client wallet never contains the database's private key.
timeout 15 lsnrctl stop >/dev/null 2>&1 || true
rm -rf -- "$config/.tls-wallet.previous"
if [ -e "$config/.tls-wallet" ]; then mv "$config/.tls-wallet" "$config/.tls-wallet.previous"; fi
mv "$stage/server" "$config/.tls-wallet"
client="/opt/oracle/oradata/clientWallet/${ORACLE_SID}"
mkdir -p "$(dirname "$client")"
rm -rf -- "${client}.previous"
if [ -e "$client" ]; then mv "$client" "${client}.previous"; fi
mv "$stage/client" "$client"
mv "$stage/listener.ora" "$config/listener.ora"
mv "$stage/sqlnet.ora" "$config/sqlnet.ora"
ln -sfn "$config/listener.ora" "$ORACLE_HOME/network/admin/listener.ora"
ln -sfn "$config/sqlnet.ora" "$ORACLE_HOME/network/admin/sqlnet.ora"
mkdir -p /opt/oracle/dg-wallet
ln -sfn "$config/.tls-wallet" /opt/oracle/dg-wallet/sidb-standby-dg-client-wallet
timeout 30 sqlplus -s / as sysdba >/dev/null 2>&1 <<'SQL'
whenever sqlerror exit failure
whenever oserror exit failure
set echo off verify off feedback off
ALTER SYSTEM SET LOCAL_LISTENER='(ADDRESS=(PROTOCOL=IPC)(KEY=HAKOPOD))' SCOPE=BOTH;
ALTER SYSTEM SET MAX_DUMP_FILE_SIZE='10M' SCOPE=BOTH;
DECLARE mode VARCHAR2(20);
BEGIN
  SELECT open_mode INTO mode FROM v$database;
  IF mode='READ WRITE' THEN DBMS_XDB_CONFIG.SETHTTPPORT(0); DBMS_XDB_CONFIG.SETHTTPSPORT(0); END IF;
END;
/
exit
SQL
timeout 30 lsnrctl start >/dev/null 2>&1
timeout 30 sqlplus -s / as sysdba >/dev/null 2>&1 <<'SQL'
whenever sqlerror exit failure
whenever oserror exit failure
ALTER SYSTEM REGISTER;
exit
SQL
