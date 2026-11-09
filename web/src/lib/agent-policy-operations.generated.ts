// Generated from api/openapi.json by api/generate_policy_operations.py. Do not edit.
export const policyOperations = [
  {
    "id": "acceptManagedPlatform",
    "method": "POST",
    "path": "/managed-platforms/operations",
    "category": "deploy",
    "summary": "Request a managed platform change from a saved review."
  },
  {
    "id": "acceptManagedPlatformRecovery",
    "method": "POST",
    "path": "/managed-platform-recovery/operations",
    "category": "deploy",
    "summary": "Request managed platform recovery from a saved review."
  },
  {
    "id": "acknowledgeAlarm",
    "method": "POST",
    "path": "/alarms/{id}/acknowledge",
    "category": "write",
    "summary": "Acknowledge the specified alarm."
  },
  {
    "id": "activateLicense",
    "method": "PUT",
    "path": "/license",
    "category": "admin",
    "summary": "Activate the installation license."
  },
  {
    "id": "applicationProvenance",
    "method": "GET",
    "path": "/applications/{id}/provenance",
    "category": "read",
    "summary": "Read the provenance records for an application."
  },
  {
    "id": "applyLegacyExternalDatabaseBindingReview",
    "method": "POST",
    "path": "/external-databases/{id}/connect",
    "category": "write",
    "summary": "Request a reviewed application binding to a legacy external database."
  },
  {
    "id": "applySecurityFix",
    "method": "POST",
    "path": "/applications/{id}/security/apply",
    "category": "write",
    "summary": "Apply the fix for a security advisory finding."
  },
  {
    "id": "attachServiceTLS",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/tls",
    "category": "write",
    "summary": "Request attachment of a TLS certificate to the service."
  },
  {
    "id": "attestDatabaseInspection",
    "method": "POST",
    "path": "/databases/{id}/inspect",
    "category": "write",
    "summary": "Record the caller's inspection of restored database data."
  },
  {
    "id": "beginDomainVerification",
    "method": "POST",
    "path": "/applications/{id}/domains",
    "category": "write",
    "summary": "Start verification of an application domain."
  },
  {
    "id": "callSandboxSession",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/sessions/{session}/call",
    "category": "binary",
    "summary": "Send bounded input to the fixed helper of one owned session."
  },
  {
    "id": "cancelActionsJob",
    "method": "POST",
    "path": "/applications/{id}/actions/{service}/jobs/{slot}/cancel",
    "category": "write",
    "summary": "Request cancellation of a Managed Actions job."
  },
  {
    "id": "cancelBackup",
    "method": "POST",
    "path": "/backups/{id}/cancel",
    "category": "backup",
    "summary": "Request cancellation of a backup job."
  },
  {
    "id": "cancelDeployment",
    "method": "POST",
    "path": "/deployments/{id}/cancel",
    "category": "deploy",
    "summary": "Request cancellation of a deployment."
  },
  {
    "id": "cancelJobInvocation",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/invocations/{invocation}/cancel",
    "category": "write",
    "summary": "Request cancellation of one owned job invocation."
  },
  {
    "id": "cancelManagedPlatformRecoveryOperation",
    "method": "POST",
    "path": "/managed-platform-recovery-operations/{id}/cancel",
    "category": "deploy",
    "summary": "Request cancellation of a recovery operation for a managed platform."
  },
  {
    "id": "cancelSourceBuild",
    "method": "POST",
    "path": "/builds/{id}/runs/{run}/cancel",
    "category": "write",
    "summary": "Request cancellation of a source build run."
  },
  {
    "id": "checkApplicationSecurity",
    "method": "POST",
    "path": "/applications/{id}/security/check",
    "category": "write",
    "summary": "Check an application against security advisories now."
  },
  {
    "id": "checkSecretRequirements",
    "method": "POST",
    "path": "/deployment-secret-requirements",
    "category": "write",
    "summary": "Check the secrets required by the proposed application specification."
  },
  {
    "id": "closeHostTerminal",
    "method": "DELETE",
    "path": "/nodes/{node}/terminal/{session}",
    "category": "terminal",
    "summary": "Close the specified host terminal session."
  },
  {
    "id": "closeMCPSession",
    "method": "DELETE",
    "path": "/mcp",
    "category": "transport",
    "summary": "Close the MCP session."
  },
  {
    "id": "completeGitHubAppInstallation",
    "method": "POST",
    "path": "/git/github/install/complete",
    "category": "protocol",
    "summary": "Complete the GitHub App installation callback."
  },
  {
    "id": "completeGitHubAppManifest",
    "method": "POST",
    "path": "/git/github/complete",
    "category": "protocol",
    "summary": "Complete GitHub App registration from its manifest callback."
  },
  {
    "id": "completeGitSourceOAuth",
    "method": "POST",
    "path": "/git/oauth/complete",
    "category": "protocol",
    "summary": "Complete the Git provider authorization callback."
  },
  {
    "id": "completeNamedGitSourceOAuth",
    "method": "POST",
    "path": "/git/connections/{id}/oauth/complete",
    "category": "protocol",
    "summary": "Complete the authorization callback for the specified Git connection."
  },
  {
    "id": "configureGitHub",
    "method": "PUT",
    "path": "/integrations/github",
    "category": "credentials",
    "summary": "Configure the GitHub integration."
  },
  {
    "id": "configureGitLab",
    "method": "PUT",
    "path": "/integrations/gitlab",
    "category": "credentials",
    "summary": "Configure the GitLab integration."
  },
  {
    "id": "confirmInstallationAccess",
    "method": "POST",
    "path": "/installation/access/confirm",
    "category": "admin",
    "summary": "Confirm a pending management access policy from an allowed network."
  },
  {
    "id": "connectSlack",
    "method": "POST",
    "path": "/integrations/slack/connect",
    "category": "protocol",
    "summary": "Start the Slack connection flow."
  },
  {
    "id": "convertCompose",
    "method": "POST",
    "path": "/compose/convert",
    "category": "write",
    "summary": "Convert a Docker Compose configuration to a Hakopod specification."
  },
  {
    "id": "cordonNode",
    "method": "POST",
    "path": "/nodes/{name}/cordon",
    "category": "admin",
    "summary": "Prevent new workloads from scheduling on the node."
  },
  {
    "id": "createApplicationDNSRecords",
    "method": "POST",
    "path": "/applications/{id}/domains/dns-records",
    "category": "write",
    "summary": "Create DNS records for the application domain."
  },
  {
    "id": "createApplicationTLSIssuer",
    "method": "POST",
    "path": "/applications/{id}/tls/issuers",
    "category": "write",
    "summary": "Create a TLS issuer for the application."
  },
  {
    "id": "createBackup",
    "method": "POST",
    "path": "/backups",
    "category": "backup",
    "summary": "Request a database backup."
  },
  {
    "id": "createBackupDestination",
    "method": "POST",
    "path": "/backup-destinations",
    "category": "credentials",
    "summary": "Create a backup destination."
  },
  {
    "id": "createBackupSchedule",
    "method": "POST",
    "path": "/backup-schedules",
    "category": "backup",
    "summary": "Create a backup schedule."
  },
  {
    "id": "createCustomHostname",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/custom-hostnames",
    "category": "write",
    "summary": "Attach a custom hostname to a service"
  },
  {
    "id": "createCustomRole",
    "method": "POST",
    "path": "/roles",
    "category": "admin",
    "summary": "Create a custom role."
  },
  {
    "id": "createDeployment",
    "method": "POST",
    "path": "/deployments",
    "category": "deploy",
    "summary": "Request an application deployment."
  },
  {
    "id": "createDeploymentNotification",
    "method": "POST",
    "path": "/applications/{id}/notifications",
    "category": "deploy",
    "summary": "Create a notification target for application deployments."
  },
  {
    "id": "createEnvironment",
    "method": "POST",
    "path": "/projects/{project}/environments",
    "category": "admin",
    "summary": "Create an environment in the project."
  },
  {
    "id": "createGitConnection",
    "method": "POST",
    "path": "/git/connections",
    "category": "credentials",
    "summary": "Create a Git connection."
  },
  {
    "id": "createInstallationVPNPeer",
    "method": "POST",
    "path": "/installation/access/vpn-peers",
    "category": "admin",
    "summary": "Create a VPN profile for a device from its WireGuard public key."
  },
  {
    "id": "createJobInvocation",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/invocations",
    "category": "write",
    "summary": "Queue one execution of the reviewed job template."
  },
  {
    "id": "createKey",
    "method": "POST",
    "path": "/keys",
    "category": "credentials",
    "summary": "Create an automation key with the requested scope and permissions."
  },
  {
    "id": "createManagedDatabase",
    "method": "POST",
    "path": "/databases",
    "category": "write",
    "summary": "Request creation of a managed database."
  },
  {
    "id": "createNodeEnrollment",
    "method": "POST",
    "path": "/nodes/enrollments",
    "category": "credentials",
    "summary": "Create an enrollment request for a node."
  },
  {
    "id": "createPreview",
    "method": "POST",
    "path": "/applications/{id}/previews",
    "category": "write",
    "summary": "Request a preview environment for the application."
  },
  {
    "id": "createProject",
    "method": "POST",
    "path": "/projects",
    "category": "admin",
    "summary": "Create a project."
  },
  {
    "id": "createProjectInvite",
    "method": "POST",
    "path": "/projects/{project}/invites",
    "category": "admin",
    "summary": "Create an invitation to the project."
  },
  {
    "id": "createRegistry",
    "method": "POST",
    "path": "/registries",
    "category": "write",
    "summary": "Store access settings for a container registry."
  },
  {
    "id": "createSandboxSession",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/sessions",
    "category": "write",
    "summary": "Create an isolated session from an immutable worker template."
  },
  {
    "id": "createSourceBuild",
    "method": "POST",
    "path": "/builds",
    "category": "write",
    "summary": "Create a source build configuration."
  },
  {
    "id": "createTLSIssuer",
    "method": "POST",
    "path": "/tls/issuers",
    "category": "admin",
    "summary": "Create a TLS issuer for the installation."
  },
  {
    "id": "createTeam",
    "method": "POST",
    "path": "/teams",
    "category": "admin",
    "summary": "Create a team."
  },
  {
    "id": "createTeamInvite",
    "method": "POST",
    "path": "/teams/{id}/invites",
    "category": "admin",
    "summary": "Create an invitation to the team."
  },
  {
    "id": "createTerminal",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/terminal",
    "category": "terminal",
    "summary": "Open a terminal session in an application container."
  },
  {
    "id": "createVirtualNetwork",
    "method": "POST",
    "path": "/virtual-networks",
    "category": "write",
    "summary": "Create a virtual network."
  },
  {
    "id": "createWorkloadSecret",
    "method": "POST",
    "path": "/secrets/{name}",
    "category": "write",
    "summary": "Create a workload secret."
  },
  {
    "id": "deleteBackupArtifact",
    "method": "DELETE",
    "path": "/backup-artifacts/{id}",
    "category": "backup",
    "summary": "Delete the backup artifact."
  },
  {
    "id": "deleteBackupDestination",
    "method": "DELETE",
    "path": "/backup-destinations/{id}",
    "category": "backup",
    "summary": "Delete the backup destination."
  },
  {
    "id": "deleteBackupSchedule",
    "method": "DELETE",
    "path": "/backup-schedules/{id}",
    "category": "backup",
    "summary": "Delete the backup schedule."
  },
  {
    "id": "deleteCustomHostname",
    "method": "DELETE",
    "path": "/applications/{id}/services/{service}/custom-hostnames/{hostname}",
    "category": "write",
    "summary": "Remove a custom hostname"
  },
  {
    "id": "deleteCustomRole",
    "method": "DELETE",
    "path": "/roles/{role}",
    "category": "admin",
    "summary": "Delete the custom role."
  },
  {
    "id": "deleteDNSProvider",
    "method": "DELETE",
    "path": "/dns-providers/{name}",
    "category": "admin",
    "summary": "Delete a DNS provider credential that owns no records"
  },
  {
    "id": "deleteDeploymentNotification",
    "method": "DELETE",
    "path": "/applications/{id}/notifications/{target}",
    "category": "deploy",
    "summary": "Delete the notification target for application deployments."
  },
  {
    "id": "deleteEmptyApplication",
    "method": "DELETE",
    "path": "/applications/{id}",
    "category": "write",
    "summary": "Delete an empty application record."
  },
  {
    "id": "deleteEmptyEnvironment",
    "method": "DELETE",
    "path": "/projects/{project}/environments/{environment}",
    "category": "admin",
    "summary": "Delete an empty environment after confirming its ID."
  },
  {
    "id": "deleteEmptyProject",
    "method": "DELETE",
    "path": "/projects/{id}",
    "category": "admin",
    "summary": "Delete an empty project record."
  },
  {
    "id": "deleteExternalDatabase",
    "method": "DELETE",
    "path": "/external-databases/{id}",
    "category": "write",
    "summary": "Request deletion of the legacy external database registration."
  },
  {
    "id": "deleteGitConnection",
    "method": "DELETE",
    "path": "/git/connections/{id}",
    "category": "write",
    "summary": "Delete the Git connection."
  },
  {
    "id": "deleteManagedDatabase",
    "method": "DELETE",
    "path": "/databases/{id}",
    "category": "write",
    "summary": "Request deletion of the managed database."
  },
  {
    "id": "deletePreview",
    "method": "DELETE",
    "path": "/previews/{preview}",
    "category": "deploy",
    "summary": "Request deletion of the preview."
  },
  {
    "id": "deletePrivateEgressBinding",
    "method": "DELETE",
    "path": "/admin/private-egress-bindings/{name}",
    "category": "admin",
    "summary": "Revoke a private egress binding (administrator only)"
  },
  {
    "id": "deleteRegistry",
    "method": "DELETE",
    "path": "/registries/{name}",
    "category": "write",
    "summary": "Delete access settings for the container registry."
  },
  {
    "id": "deleteRetainedApplicationData",
    "method": "DELETE",
    "path": "/storage/retained/{id}",
    "category": "write",
    "summary": "Request deletion of retained volumes for the application."
  },
  {
    "id": "deleteSandboxSession",
    "method": "DELETE",
    "path": "/applications/{id}/services/{service}/sessions/{session}",
    "category": "write",
    "summary": "Close one owned session and delete its runtime."
  },
  {
    "id": "deleteSecretProvider",
    "method": "DELETE",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "summary": "Delete an unreferenced provider (administrator only)"
  },
  {
    "id": "deleteTeam",
    "method": "DELETE",
    "path": "/teams/{id}",
    "category": "admin",
    "summary": "Delete the team."
  },
  {
    "id": "deleteTerminal",
    "method": "DELETE",
    "path": "/applications/{id}/services/{service}/terminal/{session}",
    "category": "terminal",
    "summary": "Close the specified application terminal session."
  },
  {
    "id": "deleteVirtualNetwork",
    "method": "DELETE",
    "path": "/virtual-networks/{name}",
    "category": "write",
    "summary": "Delete the virtual network."
  },
  {
    "id": "deleteWorkloadSecret",
    "method": "DELETE",
    "path": "/secrets/{name}",
    "category": "write",
    "summary": "Delete the workload secret."
  },
  {
    "id": "deployApplicationDraft",
    "method": "POST",
    "path": "/applications/{id}/draft/deploy",
    "category": "deploy",
    "summary": "Deploy the application's staged changes together."
  },
  {
    "id": "deploySource",
    "method": "POST",
    "path": "/applications/{id}/source/deploy",
    "category": "deploy",
    "summary": "Request a deployment from the application source configuration."
  },
  {
    "id": "deploySourceBuild",
    "method": "POST",
    "path": "/builds/{id}/runs/{run}/deploy",
    "category": "deploy",
    "summary": "Request deployment of a verified source build."
  },
  {
    "id": "deploySourceImport",
    "method": "POST",
    "path": "/sources/deploy",
    "category": "deploy",
    "summary": "Request deployment from a reviewed source import."
  },
  {
    "id": "deployTemplate",
    "method": "POST",
    "path": "/templates/{id}/deploy",
    "category": "deploy",
    "summary": "Request an application deployment from a template."
  },
  {
    "id": "deploymentNotifications",
    "method": "GET",
    "path": "/applications/{id}/notifications",
    "category": "deploy",
    "summary": "List notification targets for application deployments."
  },
  {
    "id": "detectBuildFramework",
    "method": "POST",
    "path": "/builds/detect",
    "category": "write",
    "summary": "Detect the build framework from the source repository."
  },
  {
    "id": "discardApplicationDraft",
    "method": "DELETE",
    "path": "/applications/{id}/draft",
    "category": "write",
    "summary": "Discard the application's staged changes."
  },
  {
    "id": "discardDomainVerification",
    "method": "DELETE",
    "path": "/applications/{id}/domains/{hostname}",
    "category": "write",
    "summary": "Discard the domain verification."
  },
  {
    "id": "disconnectSlack",
    "method": "DELETE",
    "path": "/integrations/slack",
    "category": "admin",
    "summary": "Remove the Slack connection."
  },
  {
    "id": "discoverPrivateEgress",
    "method": "POST",
    "path": "/admin/private-egress-bindings/discover",
    "category": "admin",
    "summary": "Propose RDS subnet CIDRs and ports from the installation AWS identity (administrator only)"
  },
  {
    "id": "drainNode",
    "method": "POST",
    "path": "/nodes/{name}/drain",
    "category": "admin",
    "summary": "Evict eligible workloads from the node."
  },
  {
    "id": "executePodCommand",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/exec",
    "category": "execution",
    "summary": "Run a command in the specified owned pod and container."
  },
  {
    "id": "exportUserAuditHistory",
    "method": "GET",
    "path": "/audit/export",
    "category": "admin",
    "summary": "Export a page of audit history as CSV."
  },
  {
    "id": "finishServiceMove",
    "method": "POST",
    "path": "/applications/{id}/service-moves/{move}/finish",
    "category": "write",
    "summary": "Request removal of the service from its source after the destination is ready."
  },
  {
    "id": "getActionsCapacity",
    "method": "GET",
    "path": "/settings/actions-capacity",
    "category": "admin",
    "summary": "Get the Actions capacity"
  },
  {
    "id": "getActionsJobLogs",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/jobs/{slot}/logs",
    "category": "read",
    "summary": "Read logs for the specified Managed Actions job."
  },
  {
    "id": "getActionsProviderHold",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/hold",
    "category": "read",
    "summary": "Read the provider hold for the Managed Actions service."
  },
  {
    "id": "getAgentApproval",
    "method": "GET",
    "path": "/agent-approvals/{id}",
    "category": "read",
    "summary": "Read one machine-key request held for approval."
  },
  {
    "id": "getAlarmSettings",
    "method": "GET",
    "path": "/alarm-settings",
    "category": "read",
    "summary": "Read alarm settings for the selected scope."
  },
  {
    "id": "getAppLogSettings",
    "method": "GET",
    "path": "/settings/app-logs",
    "category": "admin",
    "summary": "Get application log history settings"
  },
  {
    "id": "getAppearance",
    "method": "GET",
    "path": "/settings/appearance",
    "category": "admin",
    "summary": "Read the dashboard appearance settings."
  },
  {
    "id": "getApplication",
    "method": "GET",
    "path": "/applications/{id}",
    "category": "read",
    "summary": "Read the application."
  },
  {
    "id": "getApplicationDraft",
    "method": "GET",
    "path": "/applications/{id}/draft",
    "category": "read",
    "summary": "Read the staged changes for the application, carried forward onto its current revision."
  },
  {
    "id": "getApplicationManifests",
    "method": "GET",
    "path": "/applications/{id}/manifests",
    "category": "read",
    "summary": "Render an application's generated Kubernetes manifests"
  },
  {
    "id": "getApplicationSecurity",
    "method": "GET",
    "path": "/applications/{id}/security",
    "category": "read",
    "summary": "Get security advisory findings for an application."
  },
  {
    "id": "getBackup",
    "method": "GET",
    "path": "/backups/{id}",
    "category": "read",
    "summary": "Read the state of the backup job."
  },
  {
    "id": "getBackupArtifact",
    "method": "GET",
    "path": "/backup-artifacts/{id}",
    "category": "read",
    "summary": "Read the backup artifact."
  },
  {
    "id": "getCloudCapabilities",
    "method": "GET",
    "path": "/cloud/capabilities",
    "category": "read",
    "summary": "Read capabilities exposed by the current Cloud connection."
  },
  {
    "id": "getCustomHostname",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/custom-hostnames/{hostname}",
    "category": "read",
    "summary": "Show one custom hostname"
  },
  {
    "id": "getCustomHostnameSettings",
    "method": "GET",
    "path": "/settings/custom-hostnames",
    "category": "admin",
    "summary": "Get the custom hostname installation cap"
  },
  {
    "id": "getDatabaseApplicationProvisioningOperation",
    "method": "GET",
    "path": "/database-application-provisioning-operations/{id}",
    "category": "read",
    "summary": "Read the progress of an application database provisioning operation."
  },
  {
    "id": "getDatabaseFailureHistory",
    "method": "GET",
    "path": "/databases/{id}/failures",
    "category": "read",
    "summary": "Read retained failure evidence for the managed database."
  },
  {
    "id": "getDatabaseImport",
    "method": "GET",
    "path": "/backup-imports/{id}",
    "category": "read",
    "summary": "Read the state of the database import."
  },
  {
    "id": "getDatabaseMetricHistory",
    "method": "GET",
    "path": "/databases/{id}/metrics",
    "category": "read",
    "summary": "Read recorded metrics for the managed database."
  },
  {
    "id": "getDatabaseMigrationLockRecoveryOperation",
    "method": "GET",
    "path": "/database-migration-lock-recovery-operations/{id}",
    "category": "read",
    "summary": "Read migration lock recovery progress and retained evidence."
  },
  {
    "id": "getDatabaseOperation",
    "method": "GET",
    "path": "/database-operations/{id}",
    "category": "read",
    "summary": "Read the state of the managed database operation."
  },
  {
    "id": "getDatabasePrivateAccessGuide",
    "method": "POST",
    "path": "/databases/{id}/private-access",
    "category": "read",
    "summary": "Generate private database connection steps for the selected client location. Requires database read access in the project. Commands use observed endpoints and contain no credentials. This operation does not open a tunnel or test the connection."
  },
  {
    "id": "getDatabasePublicEndpointCapabilities",
    "method": "GET",
    "path": "/databases/{id}/public-endpoint-capabilities",
    "category": "read",
    "summary": "Read supported options for publishing a database endpoint."
  },
  {
    "id": "getDatabasePublicEndpointOperation",
    "method": "GET",
    "path": "/database-public-endpoint-operations/{id}",
    "category": "read",
    "summary": "Read the state of the operation for a public database endpoint."
  },
  {
    "id": "getDatabasePublicTrust",
    "method": "GET",
    "path": "/databases/{id}/trust",
    "category": "read",
    "summary": "Read public certificate trust information for the database."
  },
  {
    "id": "getDatabaseQueryCapabilities",
    "method": "GET",
    "path": "/databases/{id}/query-capabilities",
    "category": "read",
    "summary": "Read enabled SQL modes and guarantees for the database."
  },
  {
    "id": "getDeployment",
    "method": "GET",
    "path": "/deployments/{id}",
    "category": "deploy",
    "summary": "Read the deployment."
  },
  {
    "id": "getExternalDatabase",
    "method": "GET",
    "path": "/external-databases/{id}",
    "category": "read",
    "summary": "Read the external database."
  },
  {
    "id": "getExternalDatabaseOperation",
    "method": "GET",
    "path": "/external-database-operations/{id}",
    "category": "read",
    "summary": "Read the state of the operation for a legacy external database."
  },
  {
    "id": "getExternalDatabaseTrust",
    "method": "GET",
    "path": "/external-databases/{id}/trust",
    "category": "read",
    "summary": "Read certificate trust information for a legacy external database."
  },
  {
    "id": "getGitConnection",
    "method": "GET",
    "path": "/git/connections/{id}",
    "category": "read",
    "summary": "Read the Git connection."
  },
  {
    "id": "getGitProviderSetup",
    "method": "GET",
    "path": "/git/setup",
    "category": "read",
    "summary": "Read setup status for the Git provider."
  },
  {
    "id": "getHostAccess",
    "method": "GET",
    "path": "/host-access",
    "category": "admin",
    "summary": "Read host access grants."
  },
  {
    "id": "getIdempotentDeployment",
    "method": "GET",
    "path": "/idempotency/{key}",
    "category": "deploy",
    "summary": "Find the deployment associated with an idempotency key."
  },
  {
    "id": "getIdentity",
    "method": "GET",
    "path": "/me",
    "category": "admin",
    "summary": "Read the current identity, credential type, permissions and scope."
  },
  {
    "id": "getInstallationAccess",
    "method": "GET",
    "path": "/installation/access",
    "category": "admin",
    "summary": "Read the management access policy and the address seen for this request."
  },
  {
    "id": "getInstallationLoginProvider",
    "method": "GET",
    "path": "/installation/login-providers/{provider}",
    "category": "admin",
    "summary": "Read the installation's login provider settings."
  },
  {
    "id": "getInstallationLogs",
    "method": "GET",
    "path": "/installation/logs",
    "category": "admin",
    "summary": "Read the installation logs."
  },
  {
    "id": "getInstallationSMTP",
    "method": "GET",
    "path": "/installation/smtp",
    "category": "admin",
    "summary": "Read the installation's SMTP settings."
  },
  {
    "id": "getInstallationSetup",
    "method": "GET",
    "path": "/installation/setup",
    "category": "admin",
    "summary": "Read setup settings for the installation."
  },
  {
    "id": "getInstallationStatus",
    "method": "GET",
    "path": "/installation/status",
    "category": "admin",
    "summary": "Read the installation status."
  },
  {
    "id": "getJobInvocation",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/invocations/{invocation}",
    "category": "read",
    "summary": "Read one job receipt for its creating identity and owner."
  },
  {
    "id": "getJobInvocationLogs",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/invocations/{invocation}/logs",
    "category": "read",
    "summary": "Read the encrypted terminal output for one owned job invocation."
  },
  {
    "id": "getKubernetesPatchSettings",
    "method": "GET",
    "path": "/settings/kubernetes-patches",
    "category": "admin",
    "summary": "Get the raw Kubernetes patch setting"
  },
  {
    "id": "getLicenseStatus",
    "method": "GET",
    "path": "/license",
    "category": "admin",
    "summary": "Read the license status."
  },
  {
    "id": "getManagedActions",
    "method": "GET",
    "path": "/applications/{id}/actions",
    "category": "read",
    "summary": "Read Managed Actions state for the application."
  },
  {
    "id": "getManagedActionsCapabilities",
    "method": "GET",
    "path": "/actions/capabilities",
    "category": "read",
    "summary": "Read available Managed Actions capabilities."
  },
  {
    "id": "getManagedDatabase",
    "method": "GET",
    "path": "/databases/{id}",
    "category": "read",
    "summary": "Read the managed database."
  },
  {
    "id": "getManagedPlatform",
    "method": "GET",
    "path": "/managed-platforms/{id}",
    "category": "read",
    "summary": "Read the managed platform."
  },
  {
    "id": "getManagedPlatformCatalog",
    "method": "GET",
    "path": "/managed-platforms/catalog",
    "category": "read",
    "summary": "Read the catalog of managed platform types."
  },
  {
    "id": "getManagedPlatformOperation",
    "method": "GET",
    "path": "/managed-platform-operations/{id}",
    "category": "read",
    "summary": "Read the state of the managed platform operation."
  },
  {
    "id": "getManagedPlatformPublicTrust",
    "method": "GET",
    "path": "/managed-platforms/{id}/trust",
    "category": "read",
    "summary": "Read public certificate trust information for the managed platform."
  },
  {
    "id": "getManagedPlatformRecoveryOperation",
    "method": "GET",
    "path": "/managed-platform-recovery-operations/{id}",
    "category": "read",
    "summary": "Read the state of the recovery operation for a managed platform."
  },
  {
    "id": "getOrganizationSecurity",
    "method": "GET",
    "path": "/organization/security",
    "category": "admin",
    "summary": "Read security settings for the organization."
  },
  {
    "id": "getPreview",
    "method": "GET",
    "path": "/previews/{preview}",
    "category": "read",
    "summary": "Read the preview."
  },
  {
    "id": "getPrivateEgressBinding",
    "method": "GET",
    "path": "/admin/private-egress-bindings/{name}",
    "category": "admin",
    "summary": "Read one database private egress binding (administrator only)"
  },
  {
    "id": "getProxy",
    "method": "GET",
    "path": "/settings/haproxy",
    "category": "admin",
    "summary": "Read the proxy configuration."
  },
  {
    "id": "getSandboxSession",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/sessions/{session}",
    "category": "read",
    "summary": "Read one owned session and its current generation."
  },
  {
    "id": "getSecretProvider",
    "method": "GET",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "summary": "Read provider configuration without credentials (administrator only)"
  },
  {
    "id": "getSecurityOverview",
    "method": "GET",
    "path": "/security/advisories",
    "category": "read",
    "summary": "List security advisory findings for visible applications."
  },
  {
    "id": "getSecuritySettings",
    "method": "GET",
    "path": "/security/settings",
    "category": "admin",
    "summary": "Get installation security advisory settings."
  },
  {
    "id": "getServiceDelivery",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/delivery",
    "category": "read",
    "summary": "Read delivery status for the service."
  },
  {
    "id": "getServiceRuntime",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/runtime",
    "category": "read",
    "summary": "Read observed runtime state for the service."
  },
  {
    "id": "getServiceTLS",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/tls",
    "category": "read",
    "summary": "Read the service's TLS certificate configuration."
  },
  {
    "id": "getShowcase",
    "method": "GET",
    "path": "/showcase",
    "category": "read",
    "summary": "Read the state of the demonstration application."
  },
  {
    "id": "getSlackIntegration",
    "method": "GET",
    "path": "/integrations/slack",
    "category": "admin",
    "summary": "Read the Slack connection status."
  },
  {
    "id": "getSource",
    "method": "GET",
    "path": "/applications/{id}/source",
    "category": "read",
    "summary": "Read the application's source configuration."
  },
  {
    "id": "getSourceBuild",
    "method": "GET",
    "path": "/builds/{id}",
    "category": "read",
    "summary": "Read the source build configuration."
  },
  {
    "id": "getVirtualNetwork",
    "method": "GET",
    "path": "/virtual-networks/{name}",
    "category": "read",
    "summary": "Read the virtual network."
  },
  {
    "id": "gitHubAppWebhook",
    "method": "POST",
    "path": "/webhooks/github-app/{app}",
    "category": "transport",
    "summary": "Receive an authenticated GitHub App webhook."
  },
  {
    "id": "githubStatus",
    "method": "GET",
    "path": "/integrations/github",
    "category": "admin",
    "summary": "Read the GitHub integration status."
  },
  {
    "id": "githubWebhook",
    "method": "POST",
    "path": "/webhooks/github",
    "category": "transport",
    "summary": "Receive an authenticated GitHub webhook."
  },
  {
    "id": "gitlabStatus",
    "method": "GET",
    "path": "/integrations/gitlab",
    "category": "admin",
    "summary": "Read the GitLab integration status."
  },
  {
    "id": "gitlabWebhook",
    "method": "POST",
    "path": "/webhooks/gitlab",
    "category": "transport",
    "summary": "Receive an authenticated GitLab webhook."
  },
  {
    "id": "grantHostAccess",
    "method": "PUT",
    "path": "/host-access/{user}",
    "category": "admin",
    "summary": "Grant access to the specified hosts."
  },
  {
    "id": "heartbeatSandboxSession",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/sessions/{session}/heartbeat",
    "category": "write",
    "summary": "Renew the idle deadline of the expected live generation."
  },
  {
    "id": "inspectServiceBinding",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/bindings/{variable}",
    "category": "read",
    "summary": "Inspect saved, resolved, loaded and authenticated binding evidence. Requires deployments:read in the application scope. This operation executes no commands. A saved test expires after five minutes. Container, revision, credential or trust changes invalidate earlier verification. The response contains no connection values or fingerprints."
  },
  {
    "id": "installBuildWorkflow",
    "method": "POST",
    "path": "/builds/{id}/install",
    "category": "write",
    "summary": "Install the generated build workflow in the source repository."
  },
  {
    "id": "listActionsJobs",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/jobs",
    "category": "read",
    "summary": "List Managed Actions jobs."
  },
  {
    "id": "listAlarms",
    "method": "GET",
    "path": "/alarms",
    "category": "read",
    "summary": "List alarms."
  },
  {
    "id": "listApplicationDNSProviders",
    "method": "GET",
    "path": "/applications/{id}/domains/dns-providers",
    "category": "read",
    "summary": "List DNS providers available to the application."
  },
  {
    "id": "listApplicationDomains",
    "method": "GET",
    "path": "/applications/{id}/domains",
    "category": "read",
    "summary": "List application domains."
  },
  {
    "id": "listApplicationLogSources",
    "method": "GET",
    "path": "/applications/{id}/log-sources",
    "category": "read",
    "summary": "List current and past pods that wrote logs for an application"
  },
  {
    "id": "listApplicationPreviews",
    "method": "GET",
    "path": "/applications/{id}/previews",
    "category": "read",
    "summary": "List application previews."
  },
  {
    "id": "listApplicationTLSIssuers",
    "method": "GET",
    "path": "/applications/{id}/tls/issuers",
    "category": "read",
    "summary": "List TLS issuers available to the application."
  },
  {
    "id": "listApplications",
    "method": "GET",
    "path": "/applications",
    "category": "read",
    "summary": "List applications."
  },
  {
    "id": "listAudit",
    "method": "GET",
    "path": "/audit",
    "category": "admin",
    "summary": "List audit events."
  },
  {
    "id": "listBackendCertificates",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/certificates",
    "category": "read",
    "summary": "List backend certificates for the service."
  },
  {
    "id": "listBackupArtifacts",
    "method": "GET",
    "path": "/backup-artifacts",
    "category": "read",
    "summary": "List backup artifacts."
  },
  {
    "id": "listBackupDestinations",
    "method": "GET",
    "path": "/backup-destinations",
    "category": "read",
    "summary": "List backup destinations."
  },
  {
    "id": "listBackupSchedules",
    "method": "GET",
    "path": "/backup-schedules",
    "category": "read",
    "summary": "List backup schedules."
  },
  {
    "id": "listBackupTargets",
    "method": "GET",
    "path": "/backup-targets",
    "category": "read",
    "summary": "List resources available as backup targets."
  },
  {
    "id": "listBackups",
    "method": "GET",
    "path": "/backups",
    "category": "read",
    "summary": "List backup jobs."
  },
  {
    "id": "listCustomHostnames",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/custom-hostnames",
    "category": "read",
    "summary": "List a service's custom hostnames"
  },
  {
    "id": "listCustomRoles",
    "method": "GET",
    "path": "/roles",
    "category": "admin",
    "summary": "List custom roles."
  },
  {
    "id": "listDNSProviders",
    "method": "GET",
    "path": "/dns-providers",
    "category": "admin",
    "summary": "List DNS providers: full rows for administrators, names and kinds for a delegated project owner"
  },
  {
    "id": "listDatabaseConnections",
    "method": "GET",
    "path": "/databases/{id}/connections",
    "category": "read",
    "summary": "List application bindings to the managed database."
  },
  {
    "id": "listDatabaseExplorerConnections",
    "method": "GET",
    "path": "/database-explorer/connections",
    "category": "read",
    "summary": "List supported databases and their current explorer eligibility in one project and environment."
  },
  {
    "id": "listDatabaseOperations",
    "method": "GET",
    "path": "/databases/{id}/operations",
    "category": "read",
    "summary": "List database operations."
  },
  {
    "id": "listDatabasePlacementNodes",
    "method": "GET",
    "path": "/database-placement/nodes",
    "category": "read",
    "summary": "List candidate nodes for managed database placement."
  },
  {
    "id": "listDatabasePublicEndpoints",
    "method": "GET",
    "path": "/databases/{id}/public-endpoints",
    "category": "read",
    "summary": "List public endpoints for the managed database."
  },
  {
    "id": "listExternalDatabaseConnections",
    "method": "GET",
    "path": "/external-databases/{id}/connections",
    "category": "read",
    "summary": "List application bindings to the legacy external database."
  },
  {
    "id": "listExternalDatabases",
    "method": "GET",
    "path": "/external-databases",
    "category": "read",
    "summary": "List external databases."
  },
  {
    "id": "listGitConnections",
    "method": "GET",
    "path": "/git/connections",
    "category": "read",
    "summary": "List Git connections."
  },
  {
    "id": "listGitRepositories",
    "method": "GET",
    "path": "/git/connections/{id}/repositories",
    "category": "read",
    "summary": "List Git repositories."
  },
  {
    "id": "listInstallationVPNPeers",
    "method": "GET",
    "path": "/installation/access/vpn-peers",
    "category": "admin",
    "summary": "List live VPN device profiles."
  },
  {
    "id": "listJobInvocations",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/invocations",
    "category": "read",
    "summary": "Find job receipts for the exact owner and correlation identifier."
  },
  {
    "id": "listKeys",
    "method": "GET",
    "path": "/keys",
    "category": "admin",
    "summary": "List automation key metadata."
  },
  {
    "id": "listManagedDatabases",
    "method": "GET",
    "path": "/databases",
    "category": "read",
    "summary": "List managed databases."
  },
  {
    "id": "listManagedPlatformOperations",
    "method": "GET",
    "path": "/managed-platforms/{id}/operations",
    "category": "read",
    "summary": "List managed platform operations."
  },
  {
    "id": "listManagedPlatformRecoveryOperations",
    "method": "GET",
    "path": "/managed-platforms/{id}/recovery-operations",
    "category": "read",
    "summary": "List recovery operations for the managed platform."
  },
  {
    "id": "listManagedPlatforms",
    "method": "GET",
    "path": "/managed-platforms",
    "category": "read",
    "summary": "List managed platforms."
  },
  {
    "id": "listNodeEnrollments",
    "method": "GET",
    "path": "/nodes/enrollments",
    "category": "admin",
    "summary": "List node enrollment requests."
  },
  {
    "id": "listNodes",
    "method": "GET",
    "path": "/nodes",
    "category": "admin",
    "summary": "List nodes."
  },
  {
    "id": "listPlacementNodes",
    "method": "GET",
    "path": "/placement/nodes",
    "category": "read",
    "summary": "List candidate nodes for workload placement."
  },
  {
    "id": "listPrivateEgressBindings",
    "method": "GET",
    "path": "/admin/private-egress-bindings",
    "category": "admin",
    "summary": "List database and file private egress bindings (administrator only)"
  },
  {
    "id": "listProjectMembers",
    "method": "GET",
    "path": "/projects/{project}/members",
    "category": "admin",
    "summary": "List project members."
  },
  {
    "id": "listProjects",
    "method": "GET",
    "path": "/projects",
    "category": "admin",
    "summary": "List projects."
  },
  {
    "id": "listRegistries",
    "method": "GET",
    "path": "/registries",
    "category": "read",
    "summary": "List configured container registries."
  },
  {
    "id": "listRequests",
    "method": "GET",
    "path": "/requests",
    "category": "read",
    "summary": "List observed application requests."
  },
  {
    "id": "listRetainedApplicationData",
    "method": "GET",
    "path": "/storage/retained",
    "category": "read",
    "summary": "List retained volumes for the application."
  },
  {
    "id": "listSandboxSessions",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/sessions",
    "category": "read",
    "summary": "Find sessions for one exact owner and runtime key."
  },
  {
    "id": "listScopedPrivateEgressBindings",
    "method": "GET",
    "path": "/private-egress-bindings",
    "category": "read",
    "summary": "List approved private egress destinations for a project and environment"
  },
  {
    "id": "listSecretProviders",
    "method": "GET",
    "path": "/secret-providers",
    "category": "admin",
    "summary": "List provider configuration for administrators, or names and kinds granted to a deployment scope"
  },
  {
    "id": "listServiceMoves",
    "method": "GET",
    "path": "/applications/{id}/service-moves",
    "category": "read",
    "summary": "List service move operations for the application."
  },
  {
    "id": "listSlackChannels",
    "method": "GET",
    "path": "/integrations/slack/channels",
    "category": "admin",
    "summary": "List Slack channels."
  },
  {
    "id": "listSlackDeliveries",
    "method": "GET",
    "path": "/integrations/slack/deliveries",
    "category": "admin",
    "summary": "List Slack notification deliveries."
  },
  {
    "id": "listSourceBuildRuns",
    "method": "GET",
    "path": "/builds/{id}/runs",
    "category": "read",
    "summary": "List source build runs."
  },
  {
    "id": "listSourceBuilds",
    "method": "GET",
    "path": "/builds",
    "category": "read",
    "summary": "List source build configurations."
  },
  {
    "id": "listTLSIssuers",
    "method": "GET",
    "path": "/tls/issuers",
    "category": "admin",
    "summary": "List TLS issuers."
  },
  {
    "id": "listTeamMembers",
    "method": "GET",
    "path": "/teams/{id}/members",
    "category": "admin",
    "summary": "List team members."
  },
  {
    "id": "listTeams",
    "method": "GET",
    "path": "/teams",
    "category": "admin",
    "summary": "List teams."
  },
  {
    "id": "listTemplates",
    "method": "GET",
    "path": "/templates",
    "category": "read",
    "summary": "List templates."
  },
  {
    "id": "listUserAuditHistory",
    "method": "GET",
    "path": "/audit/history",
    "category": "admin",
    "summary": "List audit history for the specified identity."
  },
  {
    "id": "listUsers",
    "method": "GET",
    "path": "/users",
    "category": "admin",
    "summary": "List users."
  },
  {
    "id": "listVirtualNetworkCandidates",
    "method": "GET",
    "path": "/virtual-networks/{name}/candidates",
    "category": "read",
    "summary": "List candidate application services for a virtual network."
  },
  {
    "id": "listVirtualNetworks",
    "method": "GET",
    "path": "/virtual-networks",
    "category": "read",
    "summary": "List virtual networks."
  },
  {
    "id": "listVolumeResizes",
    "method": "GET",
    "path": "/applications/{id}/volume-resizes",
    "category": "read",
    "summary": "List volume resize operations for the application."
  },
  {
    "id": "listWorkloadSecrets",
    "method": "GET",
    "path": "/secrets",
    "category": "read",
    "summary": "List secret metadata for workloads."
  },
  {
    "id": "mcpMessage",
    "method": "POST",
    "path": "/mcp",
    "category": "transport",
    "summary": "Process an MCP protocol message for the current session."
  },
  {
    "id": "namedGitWebhook",
    "method": "POST",
    "path": "/webhooks/git/{connection}",
    "category": "transport",
    "summary": "Receive an authenticated webhook for the specified Git connection."
  },
  {
    "id": "observeSourceBuild",
    "method": "GET",
    "path": "/builds/{id}/runs/{run}",
    "category": "read",
    "summary": "Read the state of a source build run."
  },
  {
    "id": "openHostTerminal",
    "method": "POST",
    "path": "/nodes/{node}/terminal",
    "category": "terminal",
    "summary": "Open a terminal session on the specified authorized host."
  },
  {
    "id": "planApplicationDraft",
    "method": "POST",
    "path": "/applications/{id}/draft/plan",
    "category": "deploy",
    "summary": "Prepare a deployment plan from the application's staged changes."
  },
  {
    "id": "planBackupRestore",
    "method": "POST",
    "path": "/backup-artifacts/{id}/restore-plan",
    "category": "backup",
    "summary": "Prepare a plan to restore the selected backup."
  },
  {
    "id": "planDatabaseCapacity",
    "method": "POST",
    "path": "/database-capacity-plan",
    "category": "read",
    "summary": "Calculate an advisory database size and recovery allocation from bounded current evidence."
  },
  {
    "id": "planDeployment",
    "method": "POST",
    "path": "/plan",
    "category": "deploy",
    "summary": "Prepare a deployment plan for the application."
  },
  {
    "id": "planServiceMove",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/move-plan",
    "category": "write",
    "summary": "Prepare a plan to move an application service."
  },
  {
    "id": "planSource",
    "method": "POST",
    "path": "/applications/{id}/source/plan",
    "category": "write",
    "summary": "Prepare a deployment plan from the application source configuration."
  },
  {
    "id": "planSourceBuildDeployment",
    "method": "POST",
    "path": "/builds/{id}/runs/{run}/plan",
    "category": "deploy",
    "summary": "Prepare a deployment plan for a source build."
  },
  {
    "id": "planSourceImport",
    "method": "POST",
    "path": "/sources/plan",
    "category": "write",
    "summary": "Prepare a deployment plan from a source repository."
  },
  {
    "id": "planTemplate",
    "method": "POST",
    "path": "/templates/{id}/plan",
    "category": "write",
    "summary": "Prepare a deployment plan from a template."
  },
  {
    "id": "planVirtualNetwork",
    "method": "POST",
    "path": "/virtual-networks/plan",
    "category": "write",
    "summary": "Prepare a plan for virtual network membership."
  },
  {
    "id": "planVolumeResize",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes/plan",
    "category": "write",
    "summary": "Prepare a plan to resize an application volume."
  },
  {
    "id": "pollHostTerminal",
    "method": "GET",
    "path": "/nodes/{node}/terminal/{session}/poll",
    "category": "terminal",
    "summary": "Read retained host terminal output from the supplied cursor."
  },
  {
    "id": "pollTerminal",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/terminal/{session}/poll",
    "category": "terminal",
    "summary": "Read retained application terminal output from the supplied cursor."
  },
  {
    "id": "previewBuildWorkflow",
    "method": "POST",
    "path": "/builds/{id}/preview",
    "category": "write",
    "summary": "Preview the generated build workflow."
  },
  {
    "id": "provisionDatabaseApplication",
    "method": "POST",
    "path": "/databases/{id}/application-provision",
    "category": "write",
    "summary": "Create the reviewed PostgreSQL login and database, verify its grants and queue the application binding."
  },
  {
    "id": "publishDatabasePublicEndpoint",
    "method": "POST",
    "path": "/databases/{id}/public-endpoints",
    "category": "write",
    "summary": "Request publication of a reviewed database endpoint."
  },
  {
    "id": "putActionsCapacity",
    "method": "PUT",
    "path": "/settings/actions-capacity",
    "category": "admin",
    "summary": "Replace the Actions capacity"
  },
  {
    "id": "putAlarmSettings",
    "method": "PUT",
    "path": "/alarm-settings",
    "category": "write",
    "summary": "Save alarm settings for the selected scope."
  },
  {
    "id": "putAppLogSettings",
    "method": "PUT",
    "path": "/settings/app-logs",
    "category": "admin",
    "summary": "Update application log history settings"
  },
  {
    "id": "putCustomHostnameSettings",
    "method": "PUT",
    "path": "/settings/custom-hostnames",
    "category": "admin",
    "summary": "Update the custom hostname installation cap"
  },
  {
    "id": "putDNSProvider",
    "method": "PUT",
    "path": "/dns-providers/{name}",
    "category": "admin",
    "summary": "Create or update a DNS provider credential"
  },
  {
    "id": "putInstallationAccess",
    "method": "PUT",
    "path": "/installation/access",
    "category": "admin",
    "summary": "Save the management access policy; it reverts unless confirmed within five minutes."
  },
  {
    "id": "putInstallationLoginProvider",
    "method": "PUT",
    "path": "/installation/login-providers/{provider}",
    "category": "admin",
    "summary": "Save the installation's login provider settings."
  },
  {
    "id": "putInstallationSMTP",
    "method": "PUT",
    "path": "/installation/smtp",
    "category": "admin",
    "summary": "Save the installation's SMTP settings."
  },
  {
    "id": "putKubernetesPatchSettings",
    "method": "PUT",
    "path": "/settings/kubernetes-patches",
    "category": "admin",
    "summary": "Turn raw Kubernetes patches on or off"
  },
  {
    "id": "putPrivateEgressBinding",
    "method": "PUT",
    "path": "/admin/private-egress-bindings/{name}",
    "category": "admin",
    "summary": "Create or update a private egress binding (administrator only)"
  },
  {
    "id": "putSecretProvider",
    "method": "PUT",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "summary": "Create or update an external secret provider (administrator only)"
  },
  {
    "id": "putSecurityPolicy",
    "method": "PUT",
    "path": "/applications/{id}/security/policy",
    "category": "write",
    "summary": "Set the automatic patching policy for an application."
  },
  {
    "id": "putSecuritySettings",
    "method": "PUT",
    "path": "/security/settings",
    "category": "admin",
    "summary": "Update installation security advisory settings."
  },
  {
    "id": "putTemplateSecret",
    "method": "PUT",
    "path": "/templates/{id}/secrets/{name}",
    "category": "write",
    "summary": "Store the specified secret for a template deployment."
  },
  {
    "id": "putWorkloadSecret",
    "method": "PUT",
    "path": "/secrets/{name}",
    "category": "write",
    "summary": "Store the specified workload secret."
  },
  {
    "id": "queryInstallationLogs",
    "method": "POST",
    "path": "/installation/logs/query",
    "category": "admin",
    "summary": "Query installation logs with the supplied filters and limits."
  },
  {
    "id": "queryLogs",
    "method": "POST",
    "path": "/applications/{id}/logs/query",
    "category": "write",
    "summary": "Query application logs with the supplied filters and limits."
  },
  {
    "id": "queryManagedDatabase",
    "method": "POST",
    "path": "/databases/{id}/query",
    "category": "execution",
    "summary": "Run one SQL statement with the database's enabled modes and limits."
  },
  {
    "id": "readAlarm",
    "method": "POST",
    "path": "/alarms/{id}/read",
    "category": "write",
    "summary": "Mark the specified alarm as read."
  },
  {
    "id": "recoverDatabaseMigrationLock",
    "method": "POST",
    "path": "/databases/{id}/migration-lock-recover",
    "category": "write",
    "summary": "Recheck and repair the reviewed abandoned application migration lock."
  },
  {
    "id": "releaseActionsProviderHold",
    "method": "POST",
    "path": "/applications/{id}/actions/{service}/hold/release",
    "category": "write",
    "summary": "Release the provider hold for the Managed Actions service."
  },
  {
    "id": "removeLicense",
    "method": "DELETE",
    "path": "/license",
    "category": "admin",
    "summary": "Remove the installation license."
  },
  {
    "id": "removeShowcase",
    "method": "POST",
    "path": "/showcase/remove",
    "category": "admin",
    "summary": "Request removal of the reviewed demonstration application."
  },
  {
    "id": "renameApplication",
    "method": "PUT",
    "path": "/applications/{id}/name",
    "category": "write",
    "summary": "Rename the application."
  },
  {
    "id": "renameProject",
    "method": "PUT",
    "path": "/projects/{id}/name",
    "category": "admin",
    "summary": "Rename the project."
  },
  {
    "id": "renameService",
    "method": "PUT",
    "path": "/applications/{id}/services/{service}/name",
    "category": "write",
    "summary": "Rename the service."
  },
  {
    "id": "replaceDatabaseConnection",
    "method": "POST",
    "path": "/databases/{id}/connect",
    "category": "write",
    "summary": "Request replacement of a reviewed application binding to a managed database."
  },
  {
    "id": "requestRouting",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/requests/routing",
    "category": "read",
    "summary": "Read observed request routing for the service."
  },
  {
    "id": "resizeManagedDatabase",
    "method": "POST",
    "path": "/databases/{id}/resize",
    "category": "write",
    "summary": "Request a reviewed resize of the managed database."
  },
  {
    "id": "restartService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/restart",
    "category": "write",
    "summary": "Request a restart of the service."
  },
  {
    "id": "restoreBackup",
    "method": "POST",
    "path": "/backup-artifacts/{id}/restore",
    "category": "backup",
    "summary": "Request restoration of the reviewed backup."
  },
  {
    "id": "resumeDeployment",
    "method": "POST",
    "path": "/deployments/{id}/resume",
    "category": "deploy",
    "summary": "Resume failed and dependent services in the accepted application revision."
  },
  {
    "id": "resumeGitHubAppSetup",
    "method": "POST",
    "path": "/git/connections/{id}/github/setup",
    "category": "protocol",
    "summary": "Resume the GitHub App setup."
  },
  {
    "id": "resumeService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/resume",
    "category": "write",
    "summary": "Request resumption of the service."
  },
  {
    "id": "retryManagedDatabaseResize",
    "method": "POST",
    "path": "/databases/{id}/resize-retry",
    "category": "write",
    "summary": "Request a reviewed retry of a managed database resize."
  },
  {
    "id": "retryOracleDatabaseSwitchover",
    "method": "POST",
    "path": "/databases/{id}/switchover-retry",
    "category": "write",
    "summary": "Request a retry of an Oracle database switchover."
  },
  {
    "id": "retryServiceVolumeCleanup",
    "method": "POST",
    "path": "/deployments/{id}/volume-cleanup",
    "category": "write",
    "summary": "Request another attempt to delete retained service volumes."
  },
  {
    "id": "revealDatabaseCredentials",
    "method": "POST",
    "path": "/databases/{id}/credentials",
    "category": "credentials",
    "summary": "Return credentials for the managed database."
  },
  {
    "id": "reviewDatabaseApplicationProvisioning",
    "method": "POST",
    "path": "/databases/{id}/application-provisioning-plan",
    "category": "write",
    "summary": "Review a dedicated PostgreSQL database, login and application binding."
  },
  {
    "id": "reviewDatabaseConnection",
    "method": "POST",
    "path": "/databases/{id}/connection-plan",
    "category": "write",
    "summary": "Prepare a review for an application binding to a managed database."
  },
  {
    "id": "reviewDatabaseImport",
    "method": "POST",
    "path": "/backup-imports",
    "category": "backup",
    "summary": "Prepare a review for importing a database archive."
  },
  {
    "id": "reviewDatabaseMigrationLockRecovery",
    "method": "POST",
    "path": "/databases/{id}/migration-lock-recovery-plan",
    "category": "write",
    "summary": "Inspect the supported application migration lock and review its recovery."
  },
  {
    "id": "reviewDatabasePublicEndpoint",
    "method": "POST",
    "path": "/databases/{id}/public-endpoint-plan",
    "category": "write",
    "summary": "Prepare a review for publishing a database endpoint."
  },
  {
    "id": "reviewDatabaseResize",
    "method": "POST",
    "path": "/databases/{id}/resize-plan",
    "category": "write",
    "summary": "Prepare a review for resizing the managed database."
  },
  {
    "id": "reviewLegacyExternalDatabaseBinding",
    "method": "POST",
    "path": "/external-databases/{id}/connection-plan",
    "category": "write",
    "summary": "Prepare a review for an application binding to a legacy external database."
  },
  {
    "id": "reviewManagedDatabaseRecovery",
    "method": "POST",
    "path": "/databases/{id}/restore-plan",
    "category": "write",
    "summary": "Prepare a recovery plan for the managed database."
  },
  {
    "id": "reviewManagedDatabaseResizeRetry",
    "method": "POST",
    "path": "/databases/{id}/resize-retry-plan",
    "category": "write",
    "summary": "Prepare a review for retrying a managed database resize."
  },
  {
    "id": "reviewManagedPlatform",
    "method": "POST",
    "path": "/managed-platforms/reviews",
    "category": "deploy",
    "summary": "Prepare a review for a managed platform change."
  },
  {
    "id": "reviewManagedPlatformRecovery",
    "method": "POST",
    "path": "/managed-platform-recovery/reviews",
    "category": "deploy",
    "summary": "Prepare a review for recovery of a managed platform."
  },
  {
    "id": "reviewOracleDatabaseSwitchover",
    "method": "POST",
    "path": "/databases/{id}/switchover-plan",
    "category": "write",
    "summary": "Prepare a review for an Oracle database switchover."
  },
  {
    "id": "revokeDatabasePublicEndpoint",
    "method": "DELETE",
    "path": "/databases/{id}/public-endpoints/{endpoint}",
    "category": "write",
    "summary": "Request closure of the database endpoint and its existing sessions."
  },
  {
    "id": "revokeHostAccess",
    "method": "DELETE",
    "path": "/host-access/{user}/{node}",
    "category": "admin",
    "summary": "Revoke the host access."
  },
  {
    "id": "revokeInstallationVPNPeer",
    "method": "DELETE",
    "path": "/installation/access/vpn-peers/{id}",
    "category": "admin",
    "summary": "Revoke a VPN device profile."
  },
  {
    "id": "revokeKey",
    "method": "DELETE",
    "path": "/keys/{id}",
    "category": "admin",
    "summary": "Revoke the specified automation key."
  },
  {
    "id": "revokeNodeEnrollment",
    "method": "DELETE",
    "path": "/nodes/enrollments/{id}",
    "category": "admin",
    "summary": "Revoke the specified node enrollment request."
  },
  {
    "id": "rollbackApplication",
    "method": "POST",
    "path": "/applications/{id}/rollback",
    "category": "deploy",
    "summary": "Request deployment of a previous application revision."
  },
  {
    "id": "rotateExternalDatabaseCredentials",
    "method": "PUT",
    "path": "/external-databases/{id}",
    "category": "write",
    "summary": "Request credential rotation for the legacy external database registration."
  },
  {
    "id": "rotateKey",
    "method": "POST",
    "path": "/keys/{id}/rotate",
    "category": "credentials",
    "summary": "Rotate the specified automation key."
  },
  {
    "id": "runSourceBuild",
    "method": "POST",
    "path": "/builds/{id}/run",
    "category": "write",
    "summary": "Request a new run of the source build."
  },
  {
    "id": "scaleService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/scale",
    "category": "write",
    "summary": "Request a scale change for the service."
  },
  {
    "id": "setAppearance",
    "method": "PATCH",
    "path": "/settings/appearance",
    "category": "admin",
    "summary": "Save the dashboard appearance settings."
  },
  {
    "id": "setProjectMember",
    "method": "PUT",
    "path": "/projects/{project}/members",
    "category": "admin",
    "summary": "Set the identity's membership and role in the project."
  },
  {
    "id": "setProxy",
    "method": "PATCH",
    "path": "/settings/haproxy",
    "category": "admin",
    "summary": "Request a reviewed proxy configuration change."
  },
  {
    "id": "setSlackChannel",
    "method": "PUT",
    "path": "/integrations/slack/channel",
    "category": "admin",
    "summary": "Select the Slack channel for notifications."
  },
  {
    "id": "setSlackEvents",
    "method": "PUT",
    "path": "/integrations/slack/events",
    "category": "admin",
    "summary": "Select the event types sent to Slack."
  },
  {
    "id": "setSource",
    "method": "PUT",
    "path": "/applications/{id}/source",
    "category": "write",
    "summary": "Save the application's source configuration."
  },
  {
    "id": "setTeamMember",
    "method": "PUT",
    "path": "/teams/{id}/members/{user}",
    "category": "admin",
    "summary": "Set the identity's membership and role in the team."
  },
  {
    "id": "setTeamUsername",
    "method": "PUT",
    "path": "/teams/{id}/members/{user}/username",
    "category": "admin",
    "summary": "Set the identity's username within the team."
  },
  {
    "id": "stageApplicationDraft",
    "method": "PUT",
    "path": "/applications/{id}/draft",
    "category": "write",
    "summary": "Stage a configuration change for the application without deploying it."
  },
  {
    "id": "startGitHubAppManifest",
    "method": "POST",
    "path": "/git/github/start",
    "category": "protocol",
    "summary": "Start GitHub App registration from a generated manifest."
  },
  {
    "id": "startGitSourceOAuth",
    "method": "POST",
    "path": "/git/connections/{id}/authorize",
    "category": "protocol",
    "summary": "Start authorization of the Git connection with its provider."
  },
  {
    "id": "startServiceMove",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/move",
    "category": "write",
    "summary": "Request a reviewed move of an application service."
  },
  {
    "id": "startVolumeResize",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes",
    "category": "write",
    "summary": "Request a reviewed resize of an application volume."
  },
  {
    "id": "stopService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/stop",
    "category": "write",
    "summary": "Request a stop of the service."
  },
  {
    "id": "streamEvents",
    "method": "GET",
    "path": "/deployments/{id}/events",
    "category": "stream",
    "summary": "Stream events for the specified deployment."
  },
  {
    "id": "streamHostTerminal",
    "method": "GET",
    "path": "/nodes/{node}/terminal/{session}/output",
    "category": "terminal",
    "summary": "Stream output from the specified host terminal session."
  },
  {
    "id": "streamLogs",
    "method": "GET",
    "path": "/applications/{id}/logs",
    "category": "stream",
    "summary": "Read or follow logs for an application service."
  },
  {
    "id": "switchoverOracleDatabase",
    "method": "POST",
    "path": "/databases/{id}/switchover",
    "category": "write",
    "summary": "Request a reviewed Oracle database switchover."
  },
  {
    "id": "syncRegistry",
    "method": "POST",
    "path": "/registries/{name}/sync",
    "category": "write",
    "summary": "Refresh workload copies of the stored container registry credentials."
  },
  {
    "id": "terminalInput",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/terminal/{session}/input",
    "category": "terminal",
    "summary": "Send input or a resize request to the application terminal session."
  },
  {
    "id": "terminalOutput",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/terminal/{session}/output",
    "category": "terminal",
    "summary": "Stream output from the specified application terminal session."
  },
  {
    "id": "testActionsCapacity",
    "method": "POST",
    "path": "/settings/actions-capacity/{name}/test",
    "category": "admin",
    "summary": "Test an Actions capacity"
  },
  {
    "id": "testBackupDestination",
    "method": "POST",
    "path": "/backup-destinations/{id}/test",
    "category": "backup",
    "summary": "Test the backup destination."
  },
  {
    "id": "testDeploymentNotification",
    "method": "POST",
    "path": "/applications/{id}/notifications/{target}/test",
    "category": "deploy",
    "summary": "Queue a test notification for the configured deployment target."
  },
  {
    "id": "testInstallationSMTP",
    "method": "POST",
    "path": "/installation/smtp/test",
    "category": "admin",
    "summary": "Send a test message with the installation's SMTP configuration."
  },
  {
    "id": "testServiceBinding",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/bindings/{variable}/test",
    "category": "write",
    "summary": "Test one declared database binding from its running application container. Requires deployments:write in the application scope because the test executes a helper and authenticates to the database. The fixed helper reads the container environment and performs a bounded connection and read-only query check. TLS bindings require certificate and hostname verification; explicitly plaintext bindings report TLS as not configured. No URL, credential, command or SQL is accepted or returned."
  },
  {
    "id": "testSlack",
    "method": "POST",
    "path": "/integrations/slack/test",
    "category": "admin",
    "summary": "Queue a test notification through the Slack integration."
  },
  {
    "id": "updateBackupDestination",
    "method": "PUT",
    "path": "/backup-destinations/{id}",
    "category": "credentials",
    "summary": "Update the backup destination."
  },
  {
    "id": "updateBackupSchedule",
    "method": "PUT",
    "path": "/backup-schedules/{id}",
    "category": "backup",
    "summary": "Update the backup schedule."
  },
  {
    "id": "updateCustomRole",
    "method": "PUT",
    "path": "/roles/{role}",
    "category": "admin",
    "summary": "Update the custom role."
  },
  {
    "id": "updateDeploymentNotification",
    "method": "PUT",
    "path": "/applications/{id}/notifications/{target}",
    "category": "deploy",
    "summary": "Update the notification target for application deployments."
  },
  {
    "id": "updateGitConnection",
    "method": "PUT",
    "path": "/git/connections/{id}",
    "category": "credentials",
    "summary": "Update the Git connection."
  },
  {
    "id": "updateOrganizationSecurity",
    "method": "PUT",
    "path": "/organization/security",
    "category": "admin",
    "summary": "Update security settings for the organization."
  },
  {
    "id": "updateRegistry",
    "method": "PUT",
    "path": "/registries/{name}",
    "category": "write",
    "summary": "Update access settings for the container registry."
  },
  {
    "id": "updateSourceBuild",
    "method": "PUT",
    "path": "/builds/{id}",
    "category": "write",
    "summary": "Update the source build configuration."
  },
  {
    "id": "updateUser",
    "method": "PATCH",
    "path": "/users/{id}",
    "category": "admin",
    "summary": "Update the user."
  },
  {
    "id": "updateVirtualNetwork",
    "method": "PUT",
    "path": "/virtual-networks/{name}",
    "category": "write",
    "summary": "Update the virtual network."
  },
  {
    "id": "upgradeInstallation",
    "method": "POST",
    "path": "/installation/upgrade",
    "category": "admin",
    "summary": "Request an upgrade of the installation."
  },
  {
    "id": "uploadBackendCertificate",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/certificates",
    "category": "binary",
    "summary": "Upload a backend certificate for the service."
  },
  {
    "id": "uploadDatabaseImport",
    "method": "PUT",
    "path": "/backup-imports/{id}/archive",
    "category": "binary",
    "summary": "Upload the archive for the database import."
  },
  {
    "id": "verifyApplicationDomain",
    "method": "POST",
    "path": "/applications/{id}/domains/{hostname}/verify",
    "category": "write",
    "summary": "Verify the application domain."
  },
  {
    "id": "volumeResizeAction",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes/{resize}/{action}",
    "category": "write",
    "summary": "Request a volume resize retry or cancellation, or retention or deletion of its original volume."
  },
  {
    "id": "writeHostTerminal",
    "method": "POST",
    "path": "/nodes/{node}/terminal/{session}/input",
    "category": "terminal",
    "summary": "Send input or a resize request to the host terminal session."
  }
] as const
