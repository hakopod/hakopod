# Kubernetes patches

Hakopod turns each long-running service into a Deployment, a ClusterIP
Service (when the service has a port) and an Ingress (when it is public). Most
needs are covered by TOML fields. When one is not, such as a toleration for a
dedicated node pool, an extra probe or an annotation a mesh or HAProxy reads, a
service can carry a YAML patch for any of those three objects.

Patches are strategic-merge patches, the same format as `kubectl patch`. Hakopod
builds the object exactly as it would without a patch, applies the patch, and
compares the result with what it built. If the patch changes a protected field
the release fails before anything is applied, and the error names the field.

## Turning it on

Patches are off for every installation until an administrator enables them in
**Settings → Kubernetes patches**, or with the API:

```sh
curl -X PUT "$HAKOPOD_URL/api/v1/settings/kubernetes-patches" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"enabled": true, "expected_revision": 0}'
```

`expected_revision` is `0` the first time and the `revision` you read after that;
a stale revision returns 409. Every change is recorded in the audit log.

With the setting on, a release that **adds or changes** a patch also needs the
`kubernetes:patch` permission. Installation administrators and project admins
have it; the built-in developer and viewer roles do not. A custom role or API key
can be granted it. A CLI session asks for it with
`hakopod login --project P --environment E --allow-kubernetes-patch`; like
`pods:exec`, a CLI or machine key must name the grant, and an `admin` wildcard
on the key is not enough. Deployments started from a Git source run with that
source's deploy grant, which does not include `kubernetes:patch`, so a Git push
cannot add or change a patch.

Patches show in the plan diff as the service field `kubernetes` and are kept in
deployment history like every other field. Do not put secrets in them; use a
secret reference in the specification.

Turning the setting off blocks new and changed patches only. A service keeps
the patches it already has, and redeploying, restarting or rolling it back to a
revision with the same patches still works. Removing a patch never needs the
setting or the permission.

## Example

```toml
[services.api]
image = "ghcr.io/acme/api:1.4"
port = 8080
public = true

[services.api.kubernetes]
deployment = '''
spec:
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
    spec:
      tolerations:
        - key: dedicated
          operator: Equal
          value: api
          effect: NoSchedule
      containers:
        - name: app
          livenessProbe:
            httpGet:
              path: /livez
              port: 8080
            periodSeconds: 10
'''
ingress = '''
metadata:
  annotations:
    haproxy.org/load-balance: leastconn
    haproxy.org/timeout-server: 120s
'''
```

The service container is always named `app`; use that name when patching it.

Each patch is a YAML mapping of at most 16 KiB. It cannot set `apiVersion`,
`kind`, `metadata.name` or `metadata.namespace`. A misspelt field is refused
instead of silently ignored. The `service` patch needs a service with a port and
the `ingress` patch needs a public service with a port. Jobs, scheduled jobs,
sandbox sessions, Actions pools and serverless services do not accept patches.

## What a patch may change

**Deployment.** New labels and annotations on the Deployment and its pod
template. In the pod: `tolerations` (added), `nodeSelector` (keys added),
`affinity` (only when Hakopod set none, which it does for `node_name` and some
shared volumes), `topologySpreadConstraints`, `priorityClassName`,
`terminationGracePeriodSeconds`, `dnsPolicy`, `dnsConfig`, `hostAliases`,
`enableServiceLinks`, `readinessGates` and new `emptyDir` or `downwardAPI`
volumes. In each existing container: `env`, `resources` (cpu, memory and
ephemeral-storage), `livenessProbe`, `readinessProbe`, `startupProbe`,
`lifecycle`, `ports` without `hostPort` or `hostIP`, `volumeMounts` for volumes the patch adds, and
`terminationMessagePath`/`terminationMessagePolicy`.

**Service.** New labels and annotations, extra ports, `sessionAffinity`,
`sessionAffinityConfig`, `publishNotReadyAddresses`, `internalTrafficPolicy` and
`trafficDistribution`.

**Ingress.** Annotations under `haproxy.org/` only, added or overriding the ones
Hakopod sets (such as `haproxy.org/timeout-server`).

## Protected fields

Everything not listed above stays exactly as Hakopod generated it, including
fields Kubernetes adds in future releases. In particular:

- **Every object:** `apiVersion`, `kind`, `status`, and all of `metadata` other
  than labels and annotations (name, namespace, owner references, finalizers).
  Labels and annotations Hakopod sets cannot be changed or removed, and new keys
  cannot contain `hakopod` or use a `kubernetes.io` or `k8s.io` prefix other
  than the recommended `app.kubernetes.io/` labels. This keeps ownership,
  network membership and AppArmor profiles out of reach.
- **Deployment spec:** `replicas`, `selector`, `strategy`, `minReadySeconds`,
  `revisionHistoryLimit`, `progressDeadlineSeconds` and `paused`. Use
  `replicas`, `autoscaling` and `update_strategy` in TOML.
- **Pod:** `serviceAccountName`, `automountServiceAccountToken`, `hostNetwork`,
  `hostPID`, `hostIPC`, `hostUsers`, `shareProcessNamespace`, `securityContext`,
  `nodeName`, `runtimeClassName`, `schedulerName`, `imagePullSecrets`,
  `resourceClaims`, `os`, `overhead`, `priority`, `preemptionPolicy`,
  `restartPolicy` and `ephemeralContainers`. A `priorityClassName` starting with
  `system-` is refused.
- **Scheduling:** Hakopod's own tolerations, node selector keys and affinity stay.
  An added toleration must name a taint key (an empty key tolerates every taint)
  and cannot tolerate a taint whose key contains `hakopod`.
- **Volumes:** Hakopod's volumes stay as generated. A patch may add only
  `emptyDir` and `downwardAPI` volumes: `secret`, `configMap` and `projected`
  volumes could read Hakopod's own credentials or a service account token in the
  namespace, and `hostPath` and every other source are refused.
- **Containers:** containers and init containers (including Hakopod's sidecars)
  cannot be added, removed or reordered. In each one `image` (the resolved
  digest), `imagePullPolicy`, `command`, `args`, `workingDir`, `securityContext`
  (privileged, runAsNonRoot, runAsUser/runAsGroup, allowPrivilegeEscalation,
  capabilities, seccompProfile, readOnlyRootFilesystem), `envFrom` and
  `volumeDevices` stay. Environment variables Hakopod reads from a secret or
  binding cannot change, and new ones cannot read a Secret or ConfigMap
  (`fieldRef` and `resourceFieldRef` are fine). Resource names other than cpu,
  memory and ephemeral-storage, such as GPUs, stay. Probes and lifecycle hooks
  cannot name a `host`, because the kubelet would connect from the node.
- **Service:** `type` stays `ClusterIP`; `selector`, `clusterIP`, `externalIPs`,
  `loadBalancerIP`, `externalName` and the other external-exposure fields stay.
  Hakopod's ports cannot be changed or removed.
- **Ingress:** the whole spec (`ingressClassName`, hosts, paths, backends and
  TLS). Annotations outside `haproxy.org/`, such as `cert-manager.io/*`, are
  refused, and so are the HAProxy annotations that inject configuration, read a
  Secret or change what Hakopod Edge sees: any `*-snippet`, `cr-*`, `auth-type`,
  `auth-secret`, `auth-realm`, `server-ca`, `server-crt`, `client-ca`,
  `client-crt-optional`, `ssl-certificate`, `ssl-passthrough`, `src-ip-header`,
  `route-acl`, `path-rewrite`, `set-host` and `standalone-backend`.

Annotations a patch adds are recorded on the object and removed by the next
release once the patch no longer sets them. They are applied again on every
release, so a hand edit in the cluster does not survive.

## Seeing the result

`hakopod manifests shop --service api` prints the Deployment, Service and
Ingress of the current revision as YAML with the patches applied, without
applying anything. Omit `--service` for every long-running service. The same
output is available from `GET /api/v1/applications/{id}/manifests?service=api`
with `deployments:read`. Secrets are separate objects and never appear. Bindings
resolved while deploying, namely environment snapshots, workload identity,
container daemon, runtime and node profiles, registry credentials and reserved
capacity, are not shown; a deployment applies the same patch and the same
checks on top of them.

A patch that touches a protected field is refused when the release is accepted,
again before the deployment applies anything, and by `hakopod manifests`.
