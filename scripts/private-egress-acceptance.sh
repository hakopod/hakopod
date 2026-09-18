#!/usr/bin/env bash
# Disposable endpoint on the named development cluster's network; no database.
set -euo pipefail
: "${HAKOPOD_TEST_KUBECONFIG:?Set the explicit development kubeconfig}"
context=$(kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" config current-context)
if [[ "$context" != k3d-hakopod-dev ]]; then
  echo 'Refusing a context other than k3d-hakopod-dev' >&2
  exit 1
fi
fixture=hakopod-dev-private-egress-fixture
if docker container inspect "$fixture" >/dev/null 2>&1; then
  echo 'The private egress fixture already exists; inspect and remove your previous fixture first' >&2
  exit 1
fi
image='python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a'
# Clean up only the immutable container ID created by this invocation.
fixture_id=$(docker run -d --name "$fixture" --label hakopod.test=private-egress --network k3d-hakopod-dev "$image" python -u -c '
import socket, threading, time

def serve(port):
    listener = socket.socket()
    listener.bind(("0.0.0.0", port))
    listener.listen()
    while True:
        connection, _ = listener.accept()
        connection.close()

for port in (5432, 5433):
    threading.Thread(target=serve, args=(port,), daemon=True).start()
time.sleep(600)
')
trap 'docker rm -f "$fixture_id" >/dev/null' EXIT
# Verify both listeners, so the denied-port assertion tests policy, not a
# closed port. Bounded retry covers initial container startup.
docker exec "$fixture_id" python -c '
import socket, time
for attempt in range(20):
    try:
        for port in (5432, 5433):
            socket.create_connection(("127.0.0.1", port), 1).close()
        break
    except OSError:
        if attempt == 19:
            raise
        time.sleep(0.25)
'
export HAKOPOD_TEST_PRIVATE_EGRESS_IP
HAKOPOD_TEST_PRIVATE_EGRESS_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$fixture_id")
HAKOPOD_CLUSTER_TEST=1 go test ./internal/cluster -run '^TestLivePrivateEgress$' -count=1 -v
