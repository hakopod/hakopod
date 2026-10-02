# Managed database release acceptance

## Redis 8

Redis was qualified on 2026-10-02 against the named `k3d-hakopod-dev`
development cluster. The final release source was
`34aa7ba12362fa892472f5284d40580cbd62ed97` and the tested controller image was
`ghcr.io/hakopod/managed-redis-operator@sha256:87a426b087355e41247210d176d82812a5c8c462cc2856789513dd00a37ab32a`.

The release runtime is
`ghcr.io/hakopod/hakopod-redis-runtime:8.2.10-opstree@sha256:caa42ea6bb728692da6045aca364f2ae3a33e15242a2e874dfbfa75ecceb5657`.
Its native amd64 and arm64 builds, authenticated and TLS smoke tests, manifest
publication, and anonymous registry inspection passed in GitHub Actions run
`37000780889`. The platform manifests were:

- `linux/amd64`: `sha256:66a38f88f516d50de222e0b14631776851b9ff3adca5a6a4d03f1485e0d4f08a`
- `linux/arm64`: `sha256:f09fa2d4578c2f962c9391b35b4e617571076c17cb95d388101e7ee0d321f8ae`

The image builds Redis 8.2.10 from revision
`86b20b1b9e5f03d86db72c3d59bec432e471fbc9`, whose source archive SHA-256 is
`cc123e28b60ea1cfc6466f9f69dad8880515197a0f4786098b49304540b27b43`.
It uses Opstree runtime files from revision
`8a6d56bd05d42f05ff1f1f6ccfe1a7b9fbc1ef5f`. The image contains the upstream
license texts, the explicit Redis AGPL-3.0-only selection notice, the source
lock, complete source locations, and its build recipe. CI also verified the
Hakopod source, license, and upstream revision labels.

The long behavioral suite ran from source
`0d01cef579bc14759717cca47aa1ded6b240583d` against the same controller and the
earlier runtime index `sha256:a8b5559271d94989b92e3b5fe22c5167bf8ccda83fbac33065b7e1a42dd250cd`.
That runtime has the same Redis binary, entrypoint, configuration, UID, TLS
behavior, and data paths as the final image. The later image changes add
license/source material and correct OCI source metadata. The suite passed:

- standalone binary and Unicode-safe backup and separate-target recovery;
- a six-member TLS cluster with replica-acknowledged writes, primary
  replacement, separate-target shard recovery, and absolute expiry;
- hostname, issuer, and plaintext rejection; and
- automatic certificate renewal with old-CA overlap in standalone and cluster
  modes.

The package completed in 1,846.908 seconds with five pass events, no failures,
and no skips. Its bounded JSON evidence was 20,001 bytes with SHA-256
`a09910bde9ab587477e0e5e9b35f7a46544a7cc670c67578978eaad01bb0461c`;
stderr was empty. The staged source-tree checkpoint and final hashes both were
`40ad18b97bc2f23fd4bc8c0d0b0134809541902ae7332cc20bd27b28d65953a3`.
The checkpoint was recorded during execution after immutable source staging,
so it is evidence that the source did not change through completion rather
than a pre-execution measurement.

A separate final-digest check used exact release source
`34aa7ba12362fa892472f5284d40580cbd62ed97`. Kubernetes reported the final
runtime digest in both the pod specification and runtime image identity. The
standalone Redis test reached ready state, verified TLS and plaintext
rejection, and passed in 31.14 seconds. The package passed in 31.159 seconds.
Its JSON evidence was 2,888 bytes with SHA-256
`868753c05728909ddb18f13ebd22521973b82489ee1964b90b50ac5d8ce57bf4`;
stderr was empty. Cleanup removed the disposable namespace. All six namespaces
created by the longer suite were also absent after cleanup.

The real-cluster jobs were capped to an aggregate two CPUs and 12 GiB of
memory. The longer suite's observed peak was 3,237,474,304 bytes. These results
qualify the named development cluster and the stated immutable artifacts; they
do not claim production deployment.

## PostgreSQL 17 pooling and recovery

PostgreSQL 17 pooling was qualified on 2026-10-02 against the same named
development cluster and templates revision
`40de62d34a9cb5d70cdae2534266372afaf0fe7d`. The pooled application-binding
test passed in the first run from source
`e2d6e3949651eb6bd069d7395bc15b1ea36927c2`. The accompanying recovery test
exhausted the original shared 15-minute parent after provisioning its source
and target, so that run was not counted as recovery evidence.

The isolated recovery retry used source
`b7a34ce550aa75a1799d07f9e0d88543cc0f22ff`, which includes the bounded
30-minute parent for source and target provisioning. It passed authenticated
backup, restore into a separate empty target, retained source independence and
TLS access through the target pooler in 1,013.949 seconds. The retry had one
pass event, no failures or skips, an unchanged source-tree SHA-256 of
`46569283a6d8dd3ec550ec1836b38a6e1a6dc3e8e0e9d290ca8569c622e8efae`,
bounded JSON SHA-256
`4fcc8c107faa4bf8da1e0bbbd696c955f9261d8a1a13e14b445bd0fd97c5dea8`,
and evidence SHA-256
`8ca531955941a19eef55fe1c7c38c539bc66c22e09fa813b677baf74af16b98d`.
Stderr was empty. All three retry fixture namespaces were absent after cleanup.

## MongoDB 8.0 binding

MongoDB 8.0 binding was qualified on 2026-10-02 against the same named
development cluster and templates revision. The first run exhausted its shared
20-minute parent during certificate renewal and was not counted as passing
evidence. The isolated retry used source
`b7a34ce550aa75a1799d07f9e0d88543cc0f22ff`, which gives provisioning,
application rollout and renewal a bounded 30-minute parent.

`TestManagedMongoDBBindingLive` passed in 1,543.877 seconds with one pass event,
no failures and no skips. The source-tree SHA-256 remained
`46569283a6d8dd3ec550ec1836b38a6e1a6dc3e8e0e9d290ca8569c622e8efae`.
The bounded JSON SHA-256 was
`eb1535c89ef5fcdeb7af270a0556fdcc87c530573966ad2b05cebe63c3ca176e`
and the evidence SHA-256 was
`e2c2ceb02160b86523134ad4fb1bb19e812d5f4766f8e8ab6ca53d0034c6c220`;
stderr was empty. Its disposable fixture namespace was absent after cleanup.

These PostgreSQL and MongoDB results qualify the named development cluster and
the stated immutable source and template revisions. They do not claim a
production deployment.
