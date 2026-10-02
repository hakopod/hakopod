#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: smoke-redis-runtime.sh IMAGE}
work=$(mktemp -d)
plain=
tls=
cleanup() {
  test -z "$plain" || docker rm -f "$plain" >/dev/null 2>&1 || true
  test -z "$tls" || docker rm -f "$tls" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

test "$(docker image inspect "$image" --format '{{.Config.User}}')" = 1000
test "$(docker image inspect "$image" --format '{{json .Config.Entrypoint}}')" = '["/usr/bin/entrypoint.sh"]'
docker run --rm --entrypoint redis-server "$image" --version | grep -F 'v=8.2.10'
docker run --rm --entrypoint redis-server "$image" --version | grep -F 'malloc=jemalloc'
docker run --rm --entrypoint sh "$image" -ec \
  'test -x /usr/bin/healthcheck.sh && test -x /usr/bin/setupMasterSlave.sh'
docker run --rm -e SETUP_MODE=cluster -e REDIS_MAJOR_VERSION=v8 \
  -e REDIS_PASSWORD=runtime-smoke -e TLS_MODE=true \
  -e REDIS_TLS_CERT=/tls/server.crt -e REDIS_TLS_CERT_KEY=/tls/server.key -e REDIS_TLS_CA_KEY=/tls/ca.crt \
  --entrypoint bash "$image" -ec '
    mkdir /tmp/bin
    printf "#!/bin/sh\ngrep -q \"^requirepass \" /etc/redis/redis.conf\ngrep -Fx \"cluster-enabled yes\" /etc/redis/redis.conf\ngrep -Fx \"tls-cluster yes\" /etc/redis/redis.conf\ngrep -Fx \"port 0\" /etc/redis/redis.conf\n" >/tmp/bin/redis-server
    chmod +x /tmp/bin/redis-server
    PATH=/tmp/bin:$PATH /usr/bin/entrypoint.sh'

docker run --rm --entrypoint bash "$image" -ec '
  if ! redis-server /etc/redis/redis.conf --daemonize yes --dir /tmp --pidfile /tmp/redis.pid \
    --logfile /tmp/redis.log --bind 127.0.0.1 --port 6379 --protected-mode no --requirepass runtime-smoke; then
    cat /tmp/redis.log
    exit 1
  fi
  export REDISCLI_AUTH=runtime-smoke
  redis-cli -h 127.0.0.1 ping | grep -q PONG
  redis-cli -h 127.0.0.1 set runtime-smoke ok | grep -q OK
  test "$(redis-cli -h 127.0.0.1 get runtime-smoke)" = ok
  redis-cli -h 127.0.0.1 shutdown nosave'

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=redis-runtime-ca \
  -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj /CN=redis-runtime \
  -addext 'subjectAltName=DNS:redis-runtime,DNS:localhost,IP:127.0.0.1' \
  -keyout "$work/server.key" -out "$work/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:redis-runtime,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth,clientAuth\n' >"$work/server.ext"
openssl x509 -req -days 1 -in "$work/server.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/server.ext" -out "$work/server.crt" >/dev/null 2>&1
chmod 0755 "$work"
chmod 0644 "$work/ca.crt" "$work/server.key" "$work/server.crt"
docker run --rm --hostname redis-runtime -v "$work:/tls:ro" --entrypoint bash "$image" -ec '
  if ! redis-server /etc/redis/redis.conf --daemonize yes --dir /tmp --pidfile /tmp/redis.pid \
    --logfile /tmp/redis.log --bind 127.0.0.1 --port 0 --tls-port 6379 \
    --tls-cert-file /tls/server.crt --tls-key-file /tls/server.key --tls-ca-cert-file /tls/ca.crt \
    --tls-auth-clients optional --protected-mode no --requirepass runtime-smoke; then
    cat /tmp/redis.log
    exit 1
  fi
  export REDISCLI_AUTH=runtime-smoke
  for attempt in $(seq 1 30); do
    if redis-cli --tls --cacert /tls/ca.crt -h localhost ping | grep -q PONG; then break; fi
    if test "$attempt" -ge 30; then
      cat /tmp/redis.log
      exit 1
    fi
    sleep 1
  done
  redis-cli --tls --cacert /tls/ca.crt -h localhost shutdown nosave'
