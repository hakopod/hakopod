# Application logs

## Overview

When a pod crashes, is evicted or is replaced by a rollout, Kubernetes deletes
its log files. Hakopod now copies each application's logs while the pod is
alive and keeps them after the pod is gone. By default it keeps 7 days and up
to 512 MiB per application. You can read them through the API, the MCP tools,
the CLI and the dashboard.

Live logs still work as before. History is a second, opt-in way to read: you
ask for it with `history`, `from`, `to`, `pod` or a cursor. This is a bounded
operational record, not an audit archive. See "Gaps and limits" below.

## How logs are collected

The Hakopod API process collects logs itself. It adds no collector or database.
One API process holds a database lease and runs one pass every 5 seconds.

| Pod runs on | Source | Notes |
| --- | --- | --- |
| The server node | Kubelet files under `/var/log/pods`, read directly | Follows kubelet rotation. Normally no gap. |
| Any other node | Kubernetes API (`pods/log`), polled with a time cursor | Limited by the poll interval. |

- Each container run is stored separately, keyed by its restart count. A crash
  loop gives one stored run per restart.
- When a container restarts, Hakopod first reads the end of the previous run,
  then starts the new one.
- A terminating pod is drained on the next pass, ahead of other pods, so a
  rollout or scale-down normally keeps the final output.
- Per pass, at most 4 pods are read at once, 4 MiB per container and 16 MiB in
  total, in reads of 1 MiB. A very chatty app catches up over several passes.
- If a pod's files are not readable (see "Gaps and limits"), that pod is read
  through the Kubernetes API for the rest of its life.

Pods that disappear are marked ended with a reason: `completed`, `error`,
`oom_killed`, `evicted`, `replaced` or `deleted`.

## What is stored and where

- Lines are masked once, when they are collected, using the same masker as live
  logs (the pod's environment and secret references). If a pod's masker is not
  ready, Hakopod skips that pod and retries on the next pass. No unmasked line
  is written to disk.
- Lines are stored under `/var/lib/hakopod/app-logs/<application id>/`, in
  segment files (one JSON object per line). The directory is owned by
  `hakopod-api` with mode 0700 and the files use mode 0600.
- A segment is compressed (gzip) when it reaches 4 MiB or is 5 minutes old.
- PostgreSQL holds only an index: which segment covers which pod, container,
  restart and time range, plus pod records (start, end, reason, known gap).
  It never holds log text.
- Lines are assembled up to 64 KiB and masked, then cut to 16 KiB and flagged
  `truncated`. A line longer than 64 KiB is stored as "[line withheld: longer
  than 64 KiB]".
- Private-key masking continues across a Hakopod restart.
- Collection cursors hold no log text: only file offsets, an inode, a hash of
  masked text, and flags.
- Deleting an application removes its index rows; its files are removed by the
  next pruning sweep.

## Retention

| Setting | Default | Allowed |
| --- | --- | --- |
| `enabled` | true | true or false |
| `retention_days` | 7 | 1 to 30 |
| `max_mib_per_app` | 512 | 64 to 4096 (compressed size) |

Pruning runs about every 10 minutes. It deletes segments older than the
retention window, then deletes the oldest segments of any application that is
over its size limit. Old pod records and cursors are pruned the same way.

Installation admins change these in Settings, under the logs tab, or with the API:

```sh
GET /api/v1/settings/app-logs
PUT /api/v1/settings/app-logs
{"enabled":true,"retention_days":7,"max_mib_per_app":512,"expected_revision":3}
```

`expected_revision` is 0 the first time, otherwise the revision you just read.
Turning `enabled` off stops collection; pruning still runs.

## Reading logs

Reading needs `logs:read` on the application.

### API

```sh
POST /api/v1/applications/{id}/logs/query
{"service":"api","history":true,"from":"2026-10-26T08:00:00Z","to":"2026-10-26T09:00:00Z","pod_uid":"<uid>","limit":500}
GET  /api/v1/applications/{id}/log-sources?from=...&to=...&limit=200
```

- With `history` absent or false, the request behaves exactly as described in
  [logs and terminals](observability.md).
- `from` and `to` default to the last hour. The range may not exceed the
  retention window. `since_seconds` can replace `from`.
- `limit` is 1 to 2,000 (default 500). `pod_uid` selects one pod. `pod` (a name)
  is also accepted. The usual `query` filter applies.
- If more lines match, the response has `next_cursor`. Send it back as `cursor`
  with the same other fields to continue.
- The `history` object in the response says whether history is `available`
  (with a plain-English `message` if not), `collected_through`, `scan_limited`,
  `retention_days` and `gaps` (pod, seconds, note). A single query reads at most
  64 MiB of stored text and then stops with `scan_limited`.
- `log-sources` lists current and past pods: name, service, revision, node,
  start, end, reason, restarts, known gap, and whether the pod is `current`.
  `limit` is 1 to 500 (default 200).

### MCP

- `logs` accepts `history`, `from`, `to` (RFC 3339), `pod` (name or UID) and
  `cursor`. Setting any of them reads stored history.
- `log_sources` lists current and past pods and takes `from`, `to` and `limit`.

### CLI

```sh
hakopod logs shop --service api --history --since 3d
hakopod logs shop --service api --history --from 2026-10-01T08:00:00Z --to 2h --pod POD_NAME_OR_UID
hakopod logs pods shop --since 7d
```

- `--from`, `--to` and `--since` take RFC 3339 times or relative spans such as
  `2h` or `3d`. With history, `--since` may go up to 30d.
- `--pod` takes a pod name or UID. The CLI follows `next_cursor` and stops at
  `--limit` lines (default 1000).
- `hakopod logs pods` prints POD, SERVICE, REVISION, STARTED, ENDED, REASON and
  GAP. Gaps and "history unavailable" messages go to stderr.
- History is a snapshot, so it cannot be combined with `--follow`.

### Dashboard

The Logs explorer has a Pod picker that lists the service's pods, current and
past. A Past pods panel lists ended pods, and a status line shows how far
collection has reached and any gaps. If history is off or unavailable, the
explorer says so and falls back to live logs.

## Gaps and limits

Hakopod records known gaps instead of hiding them. A gap is an upper bound on
missing output, in seconds, and appears in the API, the CLI and the dashboard.

- **Hard kill on a non-server node.** Those pods are polled every 5 seconds. A
  pod killed between polls can lose up to about 5 seconds of output.
- **Rotation outruns collection.** Kubelet keeps 3 files of 20 MiB per
  container. If output is written faster than Hakopod catches up and a file is
  compressed or removed before it is read, that output is lost and a gap is
  recorded ("Log file rotated before Hakopod read it."). The first line read
  after such a gap is stored as "[line withheld: start of line was not
  collected]", because it may be the end of a line whose start was lost, and a
  partial secret cannot be masked reliably.
- **Several restarts between collections.** If a container restarts more than
  once between two passes, only the latest previous run is drained. Earlier
  runs are not collected ("Container restarted more than once between
  collections; earlier runs were not collected.").
- **Hakopod restarting.** Output written while the API was down may be missing
  ("Hakopod was restarting; output in this period may be missing.").
- **Bursts.** More output within one second than Hakopod reads is skipped
  ("Burst of output within one second exceeded what Hakopod reads; some lines
  were skipped.").
- **Missing permission.** If `hakopod-api` cannot read `/var/log/pods` (the ACL
  is missing), those pods are read through the Kubernetes API instead. They
  then have the API limits above, and the API log warns once to run
  `installer.sh --upgrade`.
- **Same timestamps.** API reads resume from a timestamp. Lines sharing the last
  timestamp are matched by content so they are not repeated; if the match
  fails, Hakopod keeps a possible duplicate rather than lose a line.
- **Query bounds.** 2,000 lines and 64 MiB scanned per request. Use the cursor
  or a narrower range.
- **Not collected:** anything that happened before this feature was installed,
  and stored logs older than the retention window.

## When history is unavailable

History reports one of these reasons:

- "Log history is not set up on this server yet. Run installer.sh --upgrade to
  enable it."
- "Log history is turned off in installation settings."

The dashboard then shows live logs with a note. The CLI and MCP fall back to
live logs automatically (up to 24h, current pods only), with the reason on
stderr or in `history_fallback`.

## Installation and upgrades

The installer sets up three things on the host:

1. The `acl` package and a read-only ACL for `hakopod-api` on `/var/log/pods`
   (`setfacl -R -m u:hakopod-api:rX`, plus a default ACL so new pods inherit
   it). `/var/log` needs traverse permission and is made traversable.
2. `/var/lib/hakopod/app-logs`, owned by `hakopod-api`, mode 0700, added to the
   API unit's `ReadWritePaths`.
3. Kubelet `container-log-max-size=20Mi` (was 10Mi), keeping 3 files.

On existing installs, `installer.sh --upgrade` applies the ACL, the directory
and the unit change. A dashboard-only upgrade does not apply them, so no
history is collected until you run `installer.sh --upgrade`. Until then the
dashboard, CLI and MCP show live logs and say "Log history is not set up on
this server yet." If the directory exists but the ACL is missing, Hakopod
collects through the Kubernetes API instead and the gaps above apply more
often. `setfacl` was verified in Debian 12 and Ubuntu 24.04 containers, not on a
real host. The 20Mi size is written to the K3s config but takes effect
at the next K3s restart, such as a reboot. The upgrade does not force one.

## Resource use

There is no additional component or service. Collection runs inside the
existing API process. Bounds: 4 concurrent pod reads, 16 MiB per pass, 4 MiB
per container per pass and 64 MiB scanned per query. Disk is limited per
application by `max_mib_per_app` and the retention window.

Measured on an Apple Silicon development machine, not a real host: about
15-22 MiB of extra heap with 20 noisy pods, ingest of about 30 MB/s, and about
31 MB of disk per million lines. Use of the collector on a real host is not yet
measured.
