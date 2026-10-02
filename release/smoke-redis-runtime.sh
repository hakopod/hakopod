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

plain=$(docker run -d -e SETUP_MODE=standalone -e REDIS_PASSWORD=runtime-smoke "$image")
for _ in $(seq 1 30); do
  if docker exec -e REDIS_PASSWORD=runtime-smoke "$plain" /usr/bin/healthcheck.sh | grep -q PONG; then break; fi
  sleep 1
done
test "$(docker exec -e REDISCLI_AUTH=runtime-smoke "$plain" redis-cli get runtime-smoke)" = ''
docker exec -e REDISCLI_AUTH=runtime-smoke "$plain" redis-cli set runtime-smoke ok | grep -q OK
test "$(docker exec -e REDISCLI_AUTH=runtime-smoke "$plain" redis-cli get runtime-smoke)" = ok
docker rm -f "$plain" >/dev/null
plain=

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=redis-runtime \
  -addext 'subjectAltName=DNS:redis-runtime,DNS:localhost,IP:127.0.0.1' \
  -keyout "$work/tls.key" -out "$work/tls.crt" >/dev/null 2>&1
chmod 0644 "$work/tls.key" "$work/tls.crt"
tls=$(docker run -d --hostname redis-runtime \
  -e SETUP_MODE=standalone -e REDIS_PASSWORD=runtime-smoke -e TLS_MODE=true \
  -e REDIS_TLS_CERT=/tls/tls.crt -e REDIS_TLS_CERT_KEY=/tls/tls.key -e REDIS_TLS_CA_KEY=/tls/tls.crt \
  -v "$work:/tls:ro" "$image")
for _ in $(seq 1 30); do
  if docker exec -e REDIS_PASSWORD=runtime-smoke -e TLS_MODE=true \
    -e REDIS_TLS_CERT=/tls/tls.crt -e REDIS_TLS_CERT_KEY=/tls/tls.key -e REDIS_TLS_CA_KEY=/tls/tls.crt \
    "$tls" /usr/bin/healthcheck.sh | grep -q PONG; then break; fi
  sleep 1
done
docker exec "$tls" grep -Fx 'port 0' /etc/redis/redis.conf
docker exec "$tls" grep -Fx 'tls-replication yes' /etc/redis/redis.conf
docker exec "$tls" redis-cli --tls --cert /tls/tls.crt --key /tls/tls.key --cacert /tls/tls.crt \
  -h redis-runtime -a runtime-smoke --no-auth-warning ping | grep -q PONG
