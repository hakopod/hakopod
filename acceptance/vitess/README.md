# Vitess HTTP vertical acceptance

This package proves the release candidate through the real `/api/v1` server and
managed-database workers. It covers scoped authorization, creation, credentials,
public trust, sharded application data, backup, separate-target recovery,
inspection, application connection revision and API deletion. It does not replace
the direct native lifecycle, recovery, reseed or revocation qualification cases.

Run it only on the named development VM after the native lane is idle. The runner
refuses any existing `hdb-*` namespace, verifies the protected cluster receipt and
dedicated worker identities, requires 17700m free requested CPU and a 12 GiB
scratch reserve, and never prints fixture credentials. Use a disposable PostgreSQL
server through `HAKOPOD_TEST_DATABASE_URL`.

For the current isolated fixture:

```sh
export HAKOPOD_TEST_DATABASE_URL='postgresql://127.0.0.1/hakopod_acceptance'
python3 scripts/run-development-vitess-http-acceptance.py \
  --root /srv/hakopod-backup-scratch/vitess-http-final-a48baa9-v1 \
  --source /srv/hakopod-backup-scratch/vitess-http-final-a48baa9-v1/source \
  --fixture /srv/hakopod-backup-scratch/vitess-native-final-a48baa9-v34/vitess-native-fixture.json \
  --kubeconfig /srv/hakopod-backup-scratch/vitess-native-final-a48baa9-v34/development-kubeconfig \
  --kubectl /srv/hakopod-backup-scratch/vitess-native-final-a48baa9-v34/bin/kubectl \
  --cache-root /srv/hakopod-backup-scratch/vitess-http-final-a48baa9-v1/cache \
  --cluster-receipt /srv/hakopod-vitess-v28/receipts/prepared-workers-v3.json \
  --cluster-receipt-sha256 4d4861b73b0ea917c4688cf66eb74a36fc679971a22ece2f9c7fca129efac420 \
  --pool vitess-acceptance \
  --storage-class local-path \
  --attempt 1
```

Do not put the PostgreSQL URL or fixture contents in committed files or evidence.
The runner records the qualification runtime inventory and the HTTP harness
inventory separately before and after execution. A skipped test, changed source,
failed command, timeout or oversized log cannot pass.
