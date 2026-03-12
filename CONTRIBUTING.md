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

Keep server memory bounded. Avoid whole-cluster caches, unbounded API lists,
retained application logs in PostgreSQL, and unnecessary runtime services.
Mark unsupported capabilities clearly; do not hide failures with mock data.

Use small pull requests describing the user-visible change, validation, and
remaining risks. Preserve dependency notices. Contributions use Apache-2.0.
