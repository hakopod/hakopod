# ClickHouse tenant access

Use this opt-in profile for a trusted service that creates read-only tenant
logins on a dedicated standalone instance. The default is `application`.

Add this section to the database TOML:

```toml
[clickhouse]
access_profile = "tenant_admin"
```

For an existing instance, keep its capacity unchanged and approve a review:

```sh
hakopod database resize-plan DATABASE_ID --file database.toml
hakopod database resize DATABASE_ID --file database.toml --review-id REVIEW_ID --revision REVISION
```

The dashboard provides the same reviewed action under database capacity.
Creation exposes the profile in **Security**, with a summary in **Review**.
The API field is `spec.clickhouse.access_profile`.

The application login can create/delete SQL users and manage quotas and row
policies. It can delegate SELECT on `app.*`. It cannot delegate server
administration, external sources or arbitrary grants. XML-managed platform
accounts remain protected. Configure tenant quotas and row policies before
issuing credentials.

Disabling this profile requires migration and tenant credential revocation.
Removing the profile alone would leave previously created tenant users active,
so Hakopod rejects an in-place downgrade.

Backups contain data, not tenant users, quotas or row policies. Store tenant
access records separately. After restore, recreate and verify tenant access
before enabling reads. Do not back up `system.users` as a substitute.

Development verification: Go tests, real PostgreSQL review authorization and
managed Kubernetes lifecycle checks, and independent desktop/mobile UI review
using explicit development fixtures. Publication and production deployment
remain pending release qualification. The Terraform provider
does not yet have a managed database resource; use the reviewed API flow from a
protected Terraform controller rather than claiming native provider support.
