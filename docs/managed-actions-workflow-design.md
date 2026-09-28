# Managed Actions workflow visibility

The pool's Actions tab shows observed busy, idle and starting runners, recent
jobs grouped by workflow run and attempt, GitHub step status, and searchable logs.
A runner filter and job selection live in the URL. Queue counts are not inferred
from runner registrations; GitHub has not assigned queued jobs to this pool.

The image observes the upstream runner's job-level `_diag/pages` output after
`ExecutionContext.Write` applies GitHub's secret masker. The JobRunner diagnostic
identity selects that job's paging files; diagnostic output and child pages are
not presented as workflow logs. A supported job-start hook records only an
allowlist of repository/run/attempt/job metadata. The observer runs as UID 1001,
reads no tokens except the runner's existing JIT configuration at startup, and
never receives a control-plane or Kubernetes credential. Collection failure does
not fail jobs. Already uploaded/deleted pages can be missed; live output is a
bounded best-effort view, and completed logs come from GitHub.

Structured output is bounded to 32 MiB per job; API snapshots read at most 2 MiB
of container output and expose at most 1 MiB of workflow text. Explicit truncation
and unavailable states preserve these limits. PostgreSQL stores metadata only,
with 30-day/1,000-job retention per pool. Every request checks application scope;
log bodies require logs:read. Provider requests use the control-plane credential
and validate repository, run attempt and exact recorded runner identity before
fetching logs. Organization membership alone is not sufficient authorization.
GitHub Actions read permission is needed in addition to runner management.

Completed-log downloads follow only GitHub's HTTPS blob-storage redirect, without
forwarding Authorization or accepting another redirect. Bodies and deadlines are
bounded. The UI renders text, removes terminal control sequences, and cannot
execute log HTML. Provider errors, missing permissions, deletion, stale metadata,
and log retention expiry stay visible. No new service or log database is added.

## Runtime verification

The isolated development workflow [run 36481872038, attempt 2](https://github.com/hakopod/hakopod/actions/runs/36481872038/attempts/2)
passed on the named `k3d-hakopod-dev` cluster. It verified UID 1001, GitHub CLI,
C/C++ build tools, a generated masked value in live output while the job ran,
and completed steps and log retrieval from GitHub. It published and deployed
no application. The observer image also passes native AMD64 and ARM64 Python
3.12 source builds of zxing-cpp 2.3.0. The development candidate was imported
by digest; this check does not establish anonymous registry pull availability
or a production rollout.
