# Contributing

Read AGENTS.md, docs/architecture.md and docs/milestones.md before changing
orchestration behavior. Go owns validation, authorization and state changes;
the CLI and TanStack dashboard use the same API.

Run `make build`, `make check`, and relevant real-cluster acceptance scripts.
Set `HAKOPOD_TEST_DATABASE_URL` to a disposable PostgreSQL superuser DSN to run
the integration suite; it creates and drops a uniquely named test database.
Never point infrastructure scripts at a production Kubernetes context.

After editing api/generate.py run `python3 api/generate.py` and
`pnpm --dir web generate:api`; commit both generated contract files. Changes
to TOML need validation tests and an explicit schema compatibility decision.
NetworkPolicy and rollout changes require real Kubernetes behavior tests.

## Live failure acceptance

After `scripts/local-up.sh` has prepared the named development cluster and
PostgreSQL, run these commands from the repository root. They use the local
credentials without printing them and run the two cases sequentially to bound
resource use:

```sh
set -a
. .local/env
set +a

HAKOPOD_TEST_DATABASE_URL="$HAKOPOD_DATABASE_URL" \
HAKOPOD_TEST_KUBECONFIG="$PWD/.local/kubeconfig" \
HAKOPOD_FAILURE_TEST=1 GOMAXPROCS=2 GOMEMLIMIT=128MiB \
  go test -p 1 ./internal/api \
    -run '^TestLivePartialGroupAndRegistryFailures$' -count=1 -v -timeout=3m

HAKOPOD_TEST_KUBECONFIG="$PWD/.local/kubeconfig" \
HAKOPOD_FAILURE_TEST=1 GOMAXPROCS=2 GOMEMLIMIT=128MiB \
  go test -p 1 ./internal/cluster \
    -run '^TestLiveImagePullFailure$' -count=1 -v -timeout=2m
```

Both tests refuse contexts other than `k3d-hakopod-dev` and use isolated,
temporary application namespaces. The API case also creates and drops a unique
database; the configured PostgreSQL user needs database-creation privileges.
The tests leave the normal API and its applications untouched and remove their
own resources on completion.

The API case proves whole-group recovery after one service succeeds and another
fails readiness, then verifies that an invalid registry tag produces an
actionable error before Kubernetes mutation. The cluster case directly applies
an unavailable digest to model an artifact disappearing after resolution and
checks the actual kubelet `ErrImagePull` or `ImagePullBackOff` diagnosis.
Without `HAKOPOD_FAILURE_TEST=1`, these cases skip; ordinary Go test success is
not evidence that live failure acceptance ran.

Keep server memory bounded. Avoid whole-cluster caches, unbounded API lists,
retained application logs in PostgreSQL, and unnecessary runtime services.
Mark unsupported capabilities clearly; do not hide failures with mock data.

Use small pull requests describing the user-visible change, validation, and
remaining risks. Preserve dependency notices. Contributions use Apache-2.0.
