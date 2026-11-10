Hakopod 0.1.0-alpha.85 fixes Managed Actions warm pools and spot reruns found by running real GitHub jobs on AWS VMs.

- **Warm pool claims work.** Claiming a stopped standby re-sent its capacity tag, which the documented IAM policy does not allow. Every claim was refused, so the standby was terminated and the job launched cold. Claims now write only the tags that change. When the pool holds one standby too many, Hakopod now terminates one that is still booting and keeps the ready ones.
- **Spot interruptions are caught at the two-minute notice.** The runner VM watches for EC2's interruption notice and stops the runner 30 s before the reclaim, so GitHub fails the job while the VM is still up. Hakopod reads the instance's spot request, so the interruption is recorded before the job's failure arrives; before, that failure could arrive first and the job was then never rerun. Interruptions sent by AWS FIS are now recognised too.
- **Measured on a real AWS account and real GitHub jobs** (us-east-2, t4g.small, default image): a job finished 37 s after dispatch on a cold VM and 35 s on a warm standby or spot instance. After an FIS interruption, the job was rerun on on-demand capacity within 2m32s.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.85/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.85
```

Upgrade an alpha.83 or alpha.84 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.85/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.85
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
