# Security policy

This is an early, single-organization development milestone. It is not a
production-hardened service or a hostile multi-tenant isolation boundary.

Do not post credentials, exploitable vulnerability details, or production
logs in public issues. If this repository is published on GitHub, use its
private vulnerability reporting feature once the maintainer has enabled it.
Until a private reporting channel is published, ask the repository owner for
a private contact without including exploit details. No security mailbox or
ownership of hakopod.com is assumed.

Include the affected revision, minimal reproduction, impact, and whether
real secrets or customer data were exposed. Rotate compromised credentials
and preserve relevant audit records. The project currently makes no response
time or supported-release guarantee.

Dependencies are pinned and updated through reviewed changes. Run the Go
vulnerability scanner and package audit as part of release preparation;
passing tests alone does not establish the absence of vulnerabilities.
