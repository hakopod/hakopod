# AWS workload identity

Hakopod can give a service short-lived AWS credentials through `AssumeRoleWithWebIdentity`. The operator approves a role for an exact project, environment, application and service. Application TOML references that binding; it cannot select an arbitrary IAM role.

This works with a self-managed K3s cluster on EC2 after its OpenID Connect issuer is registered with AWS IAM. It does not attach the EC2 instance profile to containers. Keep the node role limited to node duties and move the application's SES, S3 or other AWS permissions into a separate workload role.

## Operator configuration

Create a root-owned file such as `/etc/hakopod/aws-identities.toml`, readable by the API service user (for example `root:hakopod-api`, mode `0640`). Use mode `0600` if you point at it through the operator configuration file rather than the environment variable: that path refuses a file carrying any group or other permission bit, while the environment variable refuses only a group or other writable one.

```toml
schema_version = 1

[[bindings]]
name = "mail-sender"
project = "mail"
environment = "production"
application = "mail"
service = "smtp"
role_arn = "arn:aws:iam::123456789012:role/hakopod/mail-sender"
region = "ap-south-1"
```

In the operator TOML:

```toml
[aws]
identities_file = "/etc/hakopod/aws-identities.toml"
```

Alternatively set `HAKOPOD_AWS_IDENTITIES_FILE`. Restart Hakopod after changing the file. Startup rejects unknown fields, duplicate binding names, wildcard scopes, invalid role/region pairs and files writable by group or others. The file is limited to 64 KiB and 128 bindings.

The service opts in through its normal reviewed revision:

```toml
[services.smtp]
# Keep the service's image, ports and other existing settings.
aws_identity = "mail-sender"
```

Use an AWS SDK with web identity support. Hakopod injects the approved role, regional STS settings and `AWS_WEB_IDENTITY_TOKEN_FILE`. It disables the EC2 metadata provider and shared credential/config files. Do not bake AWS keys or credential profiles into the image. The application remains responsible for its SDK configuration and cannot be prevented from using credentials deliberately included in its own image.

Services need an egress-enabled network for public regional STS and the AWS APIs they use. Private VPC endpoints are not yet supported by the default egress policy; do not remove the metadata/private-network isolation rules to work around that.

## Prepare K3s and AWS IAM

Perform these steps on a separate test installation before production. Changing an existing cluster's service-account issuer or API audiences affects its other controllers and credentials; use a planned Kubernetes issuer migration.

1. Choose a stable public HTTPS issuer URL under your control, such as `https://identity.example.com/hakopod`. Configure the K3s API server with that service-account issuer and its public JWKS URI. Its Kubernetes API audience must exclude `sts.amazonaws.com`. A new installation can use these K3s `kube-apiserver-arg` values after verifying its controller configuration:

   ```yaml
   kube-apiserver-arg:
     - "service-account-issuer=https://identity.example.com/hakopod"
     - "service-account-jwks-uri=https://identity.example.com/hakopod/openid/v1/jwks"
     - "api-audiences=https://kubernetes.default.svc.cluster.local"
   ```

2. Publish the issuer's discovery document at `https://identity.example.com/hakopod/.well-known/openid-configuration` and its public signing keys at the JWKS URI. Export the API server's `/openid/v1/jwks` through an operator-authenticated connection. The discovery document must use the exact configured issuer, its HTTPS `jwks_uri`, `response_types_supported: ["id_token"]`, `subject_types_supported: ["public"]`, and `id_token_signing_alg_values_supported: ["RS256"]`. Serve these public documents through an HTTPS host with a trusted certificate; the Kubernetes API itself need not be exposed to the internet. Never publish the service-account signing private key.
3. Register this exact URL as an IAM OpenID Connect provider, with client ID `sts.amazonaws.com`. The HTTPS endpoint must be reachable by AWS STS. Keep the discovery document and JWKS available during issuance and rotation. Publish both old and new public keys during a signing-key rotation until all old tokens have expired.
4. Create a separate workload role. Give it only the application permissions required, such as sending from specific SES identities or accessing a particular S3 bucket/prefix. Prefer conditions on resource, account and region where the AWS action supports them. Copying an entire node instance policy defeats workload isolation.
5. Give the role a federated trust policy with exact `aud` and `sub` conditions. The subject is `system:serviceaccount:<application-namespace>:<generated-service-account>`. Get both names from the prepared service identity status or its generated Deployment. Do not use `StringLike` or a namespace-wide wildcard.

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [{
       "Effect": "Allow",
       "Principal": {"Federated": "arn:aws:iam::123456789012:oidc-provider/identity.example.com/hakopod"},
       "Action": "sts:AssumeRoleWithWebIdentity",
       "Condition": {"StringEquals": {
         "identity.example.com/hakopod:aud": "sts.amazonaws.com",
         "identity.example.com/hakopod:sub": "system:serviceaccount:hp-APPLICATION_ID:hp-aws-GENERATED_HASH"
       }}
     }]
   }
   ```

   `AWSIdentityServiceAccount` hashes the binding name, exact scope, role ARN and region. Its Kubernetes namespace is scoped to the application ID. Changing the binding or recreating an application requires reviewing the exact IAM subject again.

6. Stage a deployment and run the acceptance checks below. The dashboard distinguishes a configured/prepared Kubernetes binding from verified AWS role assumption. Creating a ServiceAccount is not evidence of successful AWS access.

Hakopod needs permission to manage ServiceAccounts in owned application namespaces, create `serviceaccounts/token`, and create `authentication.k8s.io/tokenreviews`. Before projecting an AWS token it verifies that this token does not authenticate against the Kubernetes API's default audiences. If the API accepts it, deployment fails with an audience-isolation error. No Kubernetes RoleBinding or ClusterRoleBinding is created for the workload.

## Tokens, permissions and revocation

The only projected token uses audience `sts.amazonaws.com`, a requested one-hour lifetime, and the pod-bound ServiceAccount subject. It is mounted read-only at `/var/run/secrets/hakopod/aws/token`, mode `0440`, readable through the configured filesystem group. Kubelet rotates it; there is no `subPath` mount. The default Kubernetes token stays disabled. Existing network policies continue blocking node metadata and private infrastructure addresses.

The SDK exchanges the token for temporary AWS credentials and refreshes them. IAM trust and the role's permission policy remain the AWS authorization boundary. Do not grant unrelated roles broad trust of this issuer. Bindings alone cannot restrict an IAM role whose own policy trusts all issuer subjects.

Removing `aws_identity` from a deployed revision removes the projected token and managed AWS environment from replacement pods. Unused owned ServiceAccounts are cleaned up after successful rollout; accounts still used by draining pods are retained. Removing an operator binding blocks future deployment through that binding, but it does not stop already-running pods. For urgent revocation, first deny the role in IAM and revoke active sessions, then stop or redeploy affected workloads. Previously issued STS sessions can remain valid until expiry unless explicitly denied. Keep the old workload role's trust and bindings available when a reviewed rollback still needs them.

## Acceptance

Kubernetes-only acceptance uses a unique namespace in the named development cluster:

```sh
HAKOPOD_AWS_IDENTITY_TEST=1 \
HAKOPOD_TEST_KUBECONFIG="$PWD/.local/kubeconfig" \
go test ./internal/cluster -run '^TestLiveAWSIdentityTokenIsolation$' -count=1 -timeout 5m -v
```

It verifies real token projection, non-root access, read-only permissions, the exact token audience/subject, Kubernetes API rejection, metadata network isolation and removal on a subsequent revision. It does not contact AWS.

For an AWS-configured test workload that contains Python 3, run the probe over stdin, setting the exact context and namespace yourself:

```sh
kubectl --context YOUR_TEST_CONTEXT -n YOUR_APPLICATION_NAMESPACE \
  exec -i deployment/smtp -- python3 - \
  --deny-role-arn arn:aws:iam::123456789012:role/hakopod/unrelated-test-role \
  < scripts/aws-workload-identity-probe.py
```

The negative target must be an existing test role whose trust policy excludes this subject. The probe calls regional `AssumeRoleWithWebIdentity`, signs `GetCallerIdentity` with the temporary credentials, checks the returned account/role, and requires `AccessDenied` for the unrelated role. It performs no cloud mutation, bypasses proxies and redirects, bounds requests to 15 seconds and responses to 64 KiB, and never prints credentials, tokens or AWS response bodies. AWS CloudTrail may record these calls. It is not automatically run against an operator's cluster.

Then verify the application's specific SES/S3 actions using controlled test resources, including expected denied actions. Repeat the identity check after token refresh and after removing the identity. Keep SMTP on its existing deployment until public-TCP/STARTTLS, certificate rotation, AWS permissions and rollback have all been proven with external clients. This feature does not itself certify a production cutover.

References: [Kubernetes ServiceAccount token projection](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/), [AWS IAM OIDC providers](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html), and [STS AssumeRoleWithWebIdentity](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRoleWithWebIdentity.html).
