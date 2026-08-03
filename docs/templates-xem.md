# xem.email deployment review

Xem is an open-source email marketing workspace with a Go API and a Next.js/Bun frontend. It is not a drop-in SMTP mail server. The catalog includes a guided entry rather than a deploy button because the reviewed upstream frontend embeds `NEXT_PUBLIC_API_URL` during its image build. Changing the container's runtime environment cannot reliably redirect every browser API request to a new self-hosted API.

The official deployment chart is [mailxem/devops](https://github.com/mailxem/devops). It names the Docker Hub images below. Their manifests were resolved on 12 September 2026 and both contain Linux AMD64 and ARM64 images:

| Component | Reviewed immutable reference |
| --- | --- |
| API | `docker.io/theboringhumane/xemgo:latest@sha256:2e2645fb55429e87d099312cb1cfcb11c800ea67d2c836152ecb523f037619b4` |
| Frontend | `docker.io/theboringhumane/xemapp:latest@sha256:11bceebde080246765d6d94c9753e0c7272efd07c91654716a7eb15726271938` |

These official image names were taken from the upstream chart. They are not guessed registry aliases. The backend manifest identifies source commit `38be77ef8b17e0a124b1dc2ebd5ce07a0d830f05` and the frontend identifies `dbb692623cdcbf35dae7d936426f9659874a1cd1`. Both identify GPL-3.0 licensing. The ARM64 compressed image sizes were approximately 26 MB and 86 MB respectively; neither index alone proves a successful deployment.

At that frontend commit, `hooks/use-api.ts` reads `process.env.NEXT_PUBLIC_API_URL` in browser code. `lib/services/api.ts` uses runtime `INTERNAL_API_URL` only on the server; `auth.ts` also uses the public API setting. `next.config.js` does not supply a general same-origin proxy that repairs the browser calls. The official Dockerfile builds with the public API origin and the upstream publishing workflow supplies it from repository variables. Reusing a prebuilt hosted-origin frontend could break self-hosting or send application requests to the wrong server.

A complete installation therefore needs an operator-built frontend, pinned to the resulting digest, with `NEXT_PUBLIC_API_URL` set to the operator's API origin. Verify browser network calls, authentication, uploads and callbacks against that origin before using it. The frontend's `INTERNAL_API_URL`, `NEXTAUTH_URL` and `NEXTAUTH_SECRET` must also match the deployment. No repository push, upstream CI run or registry publication was performed as part of this catalog change.

The API uses private PostgreSQL and Redis. It supports local persistent storage through `STORAGE_PROVIDER=local` and `STORAGE_BASE_PATH`, listens on port 9001, and runs as UID 65532. Set the PostgreSQL/Redis host, database/user and password values explicitly. Do not copy the upstream development database logging setting that logs every SQL statement. The API requires a non-default JWT secret and `PRIVATE_KEY`: at the reviewed commit this is a base64-encoded RSA private key parsed by the upstream crypto package. It also initializes the administrator from `SUPERADMIN_EMAIL`, `SUPERADMIN_PASSWORD`, `SUPERADMIN_NAME` and `SUPERADMIN_TEAM_NAME`; do not ship shared or sample credentials.

Use separate scoped secret references for database/Redis passwords, JWT signing material, frontend session material, encryption keys and administrator credentials. Persist the API's local storage and database data, and back them up with encryption keys. Keep managed SMTP, managed sending and optional hosted AI assistants disabled unless the operator has explicitly configured the corresponding providers. A marketing workspace is not permission to send email: no messages were sent during this review.

The upstream Kubernetes chart and [deployment guide](https://github.com/mailxem/devops/blob/main/docs/kubernetes.md) are the current starting point. Once a frontend build supports the selected self-hosted origin and a complete startup/login test passes, its immutable frontend digest can be used in an ordinary reviewed Hakopod application specification. Until then, `deployable=false` is deliberate and does not present a broken plan as a working installation.
