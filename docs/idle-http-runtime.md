# HTTP idle runtime hooks

An embedding product may enable `WorkloadPolicy.IdleHTTP` and use the exported
`auth.Service` idle methods for its authorized project/environment scopes.
Application TOML cannot turn this policy on. These methods are not HTTP API routes.
The product must send all public HTTP traffic through its activity gateway and
serialize sleep/wake transitions against active requests, including WebSockets.

Eligible services have one replica, public HTTP ingress, and no jobs, HPA,
persistent volumes, manual suspension or public TCP mappings. Runtime changes
check ownership and current revision, take the reconciler's application lock and
a short database row lock, then update Kubernetes optimistically. They leave the
saved replica count alone and never wake an arbitrary zero-replica Deployment.
Pod startup is awaited outside the database transaction.

Automatic sleep has a persisted Deployment annotation and a distinct `sleeping`
observation. The shared dashboard displays Sleeping and Wakes on request; alarms
do not report failure solely because a sleeping service has zero pods. Explicit
stop/resume and new desired revisions clear the automatic marker. Failed or
recovered releases are ineligible until a successful desired revision exists.

Verification: unit and PostgreSQL tests cover scope/revision checks, manual zero,
new desired state and concurrent configuration acceptance. `TestLiveIdleHTTP`
passed on the named `k3d-hakopod-dev` cluster with gVisor, observing zero replicas,
restored ready pods/endpoints, unchanged saved replicas and a manual stop that
remained at zero. Enable with `HAKOPOD_IDLE_HTTP_TEST=1` and an explicit
`HAKOPOD_TEST_KUBECONFIG`; it refuses other contexts.
