set -eu
umask 077
export ORACLE_PWD="$(cat /etc/hakopod-oracle/admin)"
export ORACLE_PDB=FREEPDB1
case "$ORACLE_PWD" in ''|*[!a-f0-9]*) exit 1;; esac
mkdir -p /tmp/hakopod
rm -f /tmp/hakopod/ready /tmp/hakopod/bootstrap-ready

# Refuse partially initialized data. Never let an image entrypoint replace an
# existing database merely because its completion marker is missing.
if [ -d /opt/oracle/oradata/FREE ] && [ ! -f /opt/oracle/oradata/.FREE.created ]; then
  echo 'Oracle initialization is incomplete; retained data needs inspection.'
  exit 1
fi
echo 'Starting the managed Oracle instance.'
stage='image initialization'
# Keep image diagnostics to bounded error codes. Bootstrap output can contain
# credentials or SQL and must never be forwarded to the container log.
bash /opt/oracle/runOracle.sh > >(awk '{ while (match($0,/ORA-[0-9][0-9][0-9][0-9][0-9]/)) { code=substr($0,RSTART,RLENGTH); if (!seen[code]++ && count++<32) print "Oracle initialization reported " code; $0=substr($0,RSTART+RLENGTH) } }') 2>&1 & bootstrap=$!
stop() {
  trap - EXIT HUP INT TERM
  printf 'shutdown immediate;\nexit\n' | sqlplus -s / as sysdba >/dev/null 2>&1 || true
  lsnrctl stop >/dev/null 2>&1 || true
  kill -TERM "$bootstrap" 2>/dev/null || true
  wait "$bootstrap" 2>/dev/null || true
}
trap 'status=$?; if [ "$status" -ne 0 ]; then printf "Oracle startup failed during %s (exit %s).\n" "$stage" "$status"; fi; stop' EXIT
trap 'exit 143' HUP INT TERM
for attempt in $(seq 1 360); do
  # A PDB can be open while DBCA is still finishing. Wait for the image's
  # startup hook before replacing listener configuration or creating APP.
  if [ -f /tmp/hakopod/bootstrap-ready ] && [ -f /opt/oracle/oradata/.FREE.created ] && /opt/oracle/checkDBStatus.sh >/dev/null 2>&1; then break; fi
  kill -0 "$bootstrap"
  if [ "$attempt" = 360 ]; then echo 'Oracle startup exceeded its time limit.'; exit 1; fi
  sleep 5
done
unset ORACLE_PWD
stage='server wallet creation'

# Create the Oracle wallet from this database's issued identity. Passwords enter
# OpenSSL through the environment and orapki through stdin, never command args.
export HAKOPOD_WALLET_PASSWORD="$(cat /etc/hakopod-oracle/wallet)"
mkdir -p /tmp/hakopod/wallet
openssl pkcs12 -export -in /etc/hakopod-tls/tls.crt -inkey /etc/hakopod-tls/tls.key \
  -certfile /etc/hakopod-tls/ca.crt -out /tmp/hakopod/identity.p12 \
  -passout env:HAKOPOD_WALLET_PASSWORD >/dev/null 2>&1
printf '%s\n%s\n' "$HAKOPOD_WALLET_PASSWORD" "$HAKOPOD_WALLET_PASSWORD" | \
  orapki wallet create -wallet /tmp/hakopod/wallet -auto_login >/dev/null 2>&1
printf '%s\n%s\n' "$HAKOPOD_WALLET_PASSWORD" "$HAKOPOD_WALLET_PASSWORD" | \
  orapki wallet import_pkcs12 -wallet /tmp/hakopod/wallet -pkcs12file /tmp/hakopod/identity.p12 >/dev/null 2>&1
rm /tmp/hakopod/identity.p12
unset HAKOPOD_WALLET_PASSWORD

# A replaced pod has no temporary wallet yet. Its persisted TCPS configuration
# can prevent the image's initial listener from starting. Stopping an absent
# listener is harmless; the required start below still fails on other errors.
stage='TCPS listener configuration'
lsnrctl stop >/dev/null 2>&1 || true
cat > "$ORACLE_HOME/network/admin/listener.ora" <<'LISTENER'
LISTENER=(DESCRIPTION_LIST=(DESCRIPTION=(ADDRESS=(PROTOCOL=IPC)(KEY=HAKOPOD))(ADDRESS=(PROTOCOL=TCPS)(HOST=0.0.0.0)(PORT=2484))))
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=/tmp/hakopod/wallet)))
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
ADMIN_RESTRICTIONS_LISTENER=ON
INBOUND_CONNECT_TIMEOUT_LISTENER=10
LISTENER
cat > "$ORACLE_HOME/network/admin/sqlnet.ora" <<'SQLNET'
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=/tmp/hakopod/wallet)))
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
SQLNET.INBOUND_CONNECT_TIMEOUT=15
SQLNET.RECV_TIMEOUT=60
SQLNET.SEND_TIMEOUT=60
SQLNET.EXPIRE_TIME=3
DISABLE_OOB=ON
SQLNET
lsnrctl start >/dev/null 2>&1
app_password="$(cat /etc/hakopod-app/password)"
case "$app_password" in ''|*[!a-f0-9]*) exit 1;; esac
app_quota="$(cat /etc/hakopod-config/app-quota-gib)"
case "$app_quota" in 1|2|3|4|5|6|7|8|9|10) ;; *) exit 1;; esac

# Only this owned container can use local OS administration. The application
# receives a PDB schema account; it never receives CDB administrator credentials.
stage='application schema configuration'
sqlplus -s / as sysdba >/dev/null 2>&1 <<SQL
whenever oserror exit failure
whenever sqlerror exit failure
set echo off feedback off verify off
ALTER SYSTEM SET LOCAL_LISTENER='(ADDRESS=(PROTOCOL=IPC)(KEY=HAKOPOD))' SCOPE=BOTH;
ALTER SYSTEM REGISTER;
ALTER SYSTEM SET MAX_DUMP_FILE_SIZE='10M' SCOPE=BOTH;
ALTER SESSION SET CONTAINER=FREEPDB1;
DECLARE n NUMBER;
BEGIN
  SELECT COUNT(*) INTO n FROM dba_users WHERE username='APP';
  IF n=0 THEN
    EXECUTE IMMEDIATE 'CREATE USER APP IDENTIFIED BY "$app_password" DEFAULT TABLESPACE USERS';
    EXECUTE IMMEDIATE 'GRANT CREATE SESSION, CREATE TABLE, CREATE VIEW, CREATE SEQUENCE, CREATE PROCEDURE, CREATE TRIGGER, CREATE TYPE TO APP';
  END IF;
END;
/
ALTER USER APP QUOTA ${app_quota}G ON USERS;
CREATE OR REPLACE TRIGGER SYS.HAKOPOD_CAPTURE_DDL_GUARD BEFORE DDL ON DATABASE
DECLARE lock_result NUMBER;
BEGIN
  IF ORA_DICT_OBJ_OWNER='APP' AND SYS_CONTEXT('USERENV','SESSION_USER')='APP' THEN
    lock_result := DBMS_LOCK.REQUEST(id=>82635001, lockmode=>DBMS_LOCK.S_MODE, timeout=>0, release_on_commit=>TRUE);
    IF lock_result NOT IN (0,4) THEN
      RAISE_APPLICATION_ERROR(-20010,'Schema changes are temporarily paused for a managed backup. Retry after capture completes.');
    END IF;
  END IF;
END;
/
exit
SQL
unset app_password
touch /tmp/hakopod/ready
echo 'Oracle startup and TCPS configuration completed.'
stage='database process supervision'
while kill -0 "$bootstrap" 2>/dev/null; do sleep 5; done
exit 1
