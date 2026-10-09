// Generated from api/openapi.json x-hakopod-agent. Do not edit.
export const agentRoutes = [
  {
    "id": "getIdentity",
    "method": "GET",
    "path": "/me",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listProjects",
    "method": "GET",
    "path": "/projects",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createProject",
    "method": "POST",
    "path": "/projects",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listApplications",
    "method": "GET",
    "path": "/applications",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getApplication",
    "method": "GET",
    "path": "/applications/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deleteEmptyApplication",
    "method": "DELETE",
    "path": "/applications/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listNodes",
    "method": "GET",
    "path": "/nodes",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listKeys",
    "method": "GET",
    "path": "/keys",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createKey",
    "method": "POST",
    "path": "/keys",
    "category": "credentials",
    "boundary": "installation"
  },
  {
    "id": "revokeKey",
    "method": "DELETE",
    "path": "/keys/{id}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "rotateKey",
    "method": "POST",
    "path": "/keys/{id}/rotate",
    "category": "credentials",
    "boundary": "installation"
  },
  {
    "id": "listAudit",
    "method": "GET",
    "path": "/audit",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getManagedActions",
    "method": "GET",
    "path": "/applications/{id}/actions",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedActionsCapabilities",
    "method": "GET",
    "path": "/actions/capabilities",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listActionsJobs",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/jobs",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getActionsJobLogs",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/jobs/{slot}/logs",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "cancelActionsJob",
    "method": "POST",
    "path": "/applications/{id}/actions/{service}/jobs/{slot}/cancel",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getActionsProviderHold",
    "method": "GET",
    "path": "/applications/{id}/actions/{service}/hold",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "releaseActionsProviderHold",
    "method": "POST",
    "path": "/applications/{id}/actions/{service}/hold/release",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getDatabaseQueryCapabilities",
    "method": "GET",
    "path": "/databases/{id}/query-capabilities",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listAlarms",
    "method": "GET",
    "path": "/alarms",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "acknowledgeAlarm",
    "method": "POST",
    "path": "/alarms/{id}/acknowledge",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "readAlarm",
    "method": "POST",
    "path": "/alarms/{id}/read",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getAlarmSettings",
    "method": "GET",
    "path": "/alarm-settings",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "putAlarmSettings",
    "method": "PUT",
    "path": "/alarm-settings",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listUserAuditHistory",
    "method": "GET",
    "path": "/audit/history",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listUsers",
    "method": "GET",
    "path": "/users",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "updateUser",
    "method": "PATCH",
    "path": "/users/{id}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listTeams",
    "method": "GET",
    "path": "/teams",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createTeam",
    "method": "POST",
    "path": "/teams",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "deleteTeam",
    "method": "DELETE",
    "path": "/teams/{id}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listTeamMembers",
    "method": "GET",
    "path": "/teams/{id}/members",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setTeamMember",
    "method": "PUT",
    "path": "/teams/{id}/members/{user}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createTeamInvite",
    "method": "POST",
    "path": "/teams/{id}/invites",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createProjectInvite",
    "method": "POST",
    "path": "/projects/{project}/invites",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listProjectMembers",
    "method": "GET",
    "path": "/projects/{project}/members",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setProjectMember",
    "method": "PUT",
    "path": "/projects/{project}/members",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listBackupDestinations",
    "method": "GET",
    "path": "/backup-destinations",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createBackupDestination",
    "method": "POST",
    "path": "/backup-destinations",
    "category": "credentials",
    "boundary": "project"
  },
  {
    "id": "updateBackupDestination",
    "method": "PUT",
    "path": "/backup-destinations/{id}",
    "category": "credentials",
    "boundary": "project"
  },
  {
    "id": "deleteBackupDestination",
    "method": "DELETE",
    "path": "/backup-destinations/{id}",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "testBackupDestination",
    "method": "POST",
    "path": "/backup-destinations/{id}/test",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "listBackupTargets",
    "method": "GET",
    "path": "/backup-targets",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listBackups",
    "method": "GET",
    "path": "/backups",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createBackup",
    "method": "POST",
    "path": "/backups",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "getBackup",
    "method": "GET",
    "path": "/backups/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "cancelBackup",
    "method": "POST",
    "path": "/backups/{id}/cancel",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "listBackupArtifacts",
    "method": "GET",
    "path": "/backup-artifacts",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getBackupArtifact",
    "method": "GET",
    "path": "/backup-artifacts/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deleteBackupArtifact",
    "method": "DELETE",
    "path": "/backup-artifacts/{id}",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "planBackupRestore",
    "method": "POST",
    "path": "/backup-artifacts/{id}/restore-plan",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "restoreBackup",
    "method": "POST",
    "path": "/backup-artifacts/{id}/restore",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "listBackupSchedules",
    "method": "GET",
    "path": "/backup-schedules",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createBackupSchedule",
    "method": "POST",
    "path": "/backup-schedules",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "updateBackupSchedule",
    "method": "PUT",
    "path": "/backup-schedules/{id}",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "deleteBackupSchedule",
    "method": "DELETE",
    "path": "/backup-schedules/{id}",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "testServiceBinding",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/bindings/{variable}/test",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listSourceBuilds",
    "method": "GET",
    "path": "/builds",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createSourceBuild",
    "method": "POST",
    "path": "/builds",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getSourceBuild",
    "method": "GET",
    "path": "/builds/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "updateSourceBuild",
    "method": "PUT",
    "path": "/builds/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "previewBuildWorkflow",
    "method": "POST",
    "path": "/builds/{id}/preview",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "installBuildWorkflow",
    "method": "POST",
    "path": "/builds/{id}/install",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "runSourceBuild",
    "method": "POST",
    "path": "/builds/{id}/run",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listSourceBuildRuns",
    "method": "GET",
    "path": "/builds/{id}/runs",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "observeSourceBuild",
    "method": "GET",
    "path": "/builds/{id}/runs/{run}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "cancelSourceBuild",
    "method": "POST",
    "path": "/builds/{id}/runs/{run}/cancel",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getCloudCapabilities",
    "method": "GET",
    "path": "/cloud/capabilities",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "convertCompose",
    "method": "POST",
    "path": "/compose/convert",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getServiceDelivery",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/delivery",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listBackendCertificates",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/certificates",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listDNSProviders",
    "method": "GET",
    "path": "/dns-providers",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "putDNSProvider",
    "method": "PUT",
    "path": "/dns-providers/{name}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "deleteDNSProvider",
    "method": "DELETE",
    "path": "/dns-providers/{name}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listApplicationDomains",
    "method": "GET",
    "path": "/applications/{id}/domains",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "beginDomainVerification",
    "method": "POST",
    "path": "/applications/{id}/domains",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "verifyApplicationDomain",
    "method": "POST",
    "path": "/applications/{id}/domains/{hostname}/verify",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listApplicationDNSProviders",
    "method": "GET",
    "path": "/applications/{id}/domains/dns-providers",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createApplicationDNSRecords",
    "method": "POST",
    "path": "/applications/{id}/domains/dns-records",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listGitConnections",
    "method": "GET",
    "path": "/git/connections",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createGitConnection",
    "method": "POST",
    "path": "/git/connections",
    "category": "credentials",
    "boundary": "project"
  },
  {
    "id": "getGitConnection",
    "method": "GET",
    "path": "/git/connections/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "updateGitConnection",
    "method": "PUT",
    "path": "/git/connections/{id}",
    "category": "credentials",
    "boundary": "project"
  },
  {
    "id": "deleteGitConnection",
    "method": "DELETE",
    "path": "/git/connections/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getGitProviderSetup",
    "method": "GET",
    "path": "/git/setup",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listGitRepositories",
    "method": "GET",
    "path": "/git/connections/{id}/repositories",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getInstallationStatus",
    "method": "GET",
    "path": "/installation/status",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getInstallationLogs",
    "method": "GET",
    "path": "/installation/logs",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getInstallationSetup",
    "method": "GET",
    "path": "/installation/setup",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "upgradeInstallation",
    "method": "POST",
    "path": "/installation/upgrade",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "discardDomainVerification",
    "method": "DELETE",
    "path": "/applications/{id}/domains/{hostname}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "queryInstallationLogs",
    "method": "POST",
    "path": "/installation/logs/query",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getInstallationLoginProvider",
    "method": "GET",
    "path": "/installation/login-providers/{provider}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "putInstallationLoginProvider",
    "method": "PUT",
    "path": "/installation/login-providers/{provider}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getInstallationSMTP",
    "method": "GET",
    "path": "/installation/smtp",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "putInstallationSMTP",
    "method": "PUT",
    "path": "/installation/smtp",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "testInstallationSMTP",
    "method": "POST",
    "path": "/installation/smtp/test",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getLicenseStatus",
    "method": "GET",
    "path": "/license",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "activateLicense",
    "method": "PUT",
    "path": "/license",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "removeLicense",
    "method": "DELETE",
    "path": "/license",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listManagedDatabases",
    "method": "GET",
    "path": "/databases",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createManagedDatabase",
    "method": "POST",
    "path": "/databases",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listDatabasePlacementNodes",
    "method": "GET",
    "path": "/database-placement/nodes",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "planDatabaseCapacity",
    "method": "POST",
    "path": "/database-capacity-plan",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedDatabase",
    "method": "GET",
    "path": "/databases/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deleteManagedDatabase",
    "method": "DELETE",
    "path": "/databases/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getDatabaseFailureHistory",
    "method": "GET",
    "path": "/databases/{id}/failures",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listDatabaseOperations",
    "method": "GET",
    "path": "/databases/{id}/operations",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getDatabasePublicEndpointCapabilities",
    "method": "GET",
    "path": "/databases/{id}/public-endpoint-capabilities",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listDatabasePublicEndpoints",
    "method": "GET",
    "path": "/databases/{id}/public-endpoints",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "publishDatabasePublicEndpoint",
    "method": "POST",
    "path": "/databases/{id}/public-endpoints",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewDatabasePublicEndpoint",
    "method": "POST",
    "path": "/databases/{id}/public-endpoint-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "revokeDatabasePublicEndpoint",
    "method": "DELETE",
    "path": "/databases/{id}/public-endpoints/{endpoint}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getDatabasePublicEndpointOperation",
    "method": "GET",
    "path": "/database-public-endpoint-operations/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listDatabaseConnections",
    "method": "GET",
    "path": "/databases/{id}/connections",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getDatabasePublicTrust",
    "method": "GET",
    "path": "/databases/{id}/trust",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "revealDatabaseCredentials",
    "method": "POST",
    "path": "/databases/{id}/credentials",
    "category": "credentials",
    "boundary": "project"
  },
  {
    "id": "reviewDatabaseResize",
    "method": "POST",
    "path": "/databases/{id}/resize-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "resizeManagedDatabase",
    "method": "POST",
    "path": "/databases/{id}/resize",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewManagedDatabaseResizeRetry",
    "method": "POST",
    "path": "/databases/{id}/resize-retry-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "retryManagedDatabaseResize",
    "method": "POST",
    "path": "/databases/{id}/resize-retry",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewOracleDatabaseSwitchover",
    "method": "POST",
    "path": "/databases/{id}/switchover-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "switchoverOracleDatabase",
    "method": "POST",
    "path": "/databases/{id}/switchover",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "retryOracleDatabaseSwitchover",
    "method": "POST",
    "path": "/databases/{id}/switchover-retry",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewManagedDatabaseRecovery",
    "method": "POST",
    "path": "/databases/{id}/restore-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewDatabaseConnection",
    "method": "POST",
    "path": "/databases/{id}/connection-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "replaceDatabaseConnection",
    "method": "POST",
    "path": "/databases/{id}/connect",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "attestDatabaseInspection",
    "method": "POST",
    "path": "/databases/{id}/inspect",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "reviewDatabaseImport",
    "method": "POST",
    "path": "/backup-imports",
    "category": "backup",
    "boundary": "project"
  },
  {
    "id": "getDatabaseImport",
    "method": "GET",
    "path": "/backup-imports/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getDatabaseMetricHistory",
    "method": "GET",
    "path": "/databases/{id}/metrics",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listExternalDatabases",
    "method": "GET",
    "path": "/external-databases",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getExternalDatabase",
    "method": "GET",
    "path": "/external-databases/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "rotateExternalDatabaseCredentials",
    "method": "PUT",
    "path": "/external-databases/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "deleteExternalDatabase",
    "method": "DELETE",
    "path": "/external-databases/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listExternalDatabaseConnections",
    "method": "GET",
    "path": "/external-databases/{id}/connections",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getExternalDatabaseTrust",
    "method": "GET",
    "path": "/external-databases/{id}/trust",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "reviewLegacyExternalDatabaseBinding",
    "method": "POST",
    "path": "/external-databases/{id}/connection-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "applyLegacyExternalDatabaseBindingReview",
    "method": "POST",
    "path": "/external-databases/{id}/connect",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getExternalDatabaseOperation",
    "method": "GET",
    "path": "/external-database-operations/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listManagedPlatforms",
    "method": "GET",
    "path": "/managed-platforms",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedPlatformCatalog",
    "method": "GET",
    "path": "/managed-platforms/catalog",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedPlatform",
    "method": "GET",
    "path": "/managed-platforms/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedPlatformPublicTrust",
    "method": "GET",
    "path": "/managed-platforms/{id}/trust",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listManagedPlatformOperations",
    "method": "GET",
    "path": "/managed-platforms/{id}/operations",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedPlatformOperation",
    "method": "GET",
    "path": "/managed-platform-operations/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "reviewManagedPlatform",
    "method": "POST",
    "path": "/managed-platforms/reviews",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "acceptManagedPlatform",
    "method": "POST",
    "path": "/managed-platforms/operations",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "listManagedPlatformRecoveryOperations",
    "method": "GET",
    "path": "/managed-platforms/{id}/recovery-operations",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "getManagedPlatformRecoveryOperation",
    "method": "GET",
    "path": "/managed-platform-recovery-operations/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "reviewManagedPlatformRecovery",
    "method": "POST",
    "path": "/managed-platform-recovery/reviews",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "acceptManagedPlatformRecovery",
    "method": "POST",
    "path": "/managed-platform-recovery/operations",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "cancelManagedPlatformRecoveryOperation",
    "method": "POST",
    "path": "/managed-platform-recovery-operations/{id}/cancel",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "applicationProvenance",
    "method": "GET",
    "path": "/applications/{id}/provenance",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "queryLogs",
    "method": "POST",
    "path": "/applications/{id}/logs/query",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listCustomRoles",
    "method": "GET",
    "path": "/roles",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createCustomRole",
    "method": "POST",
    "path": "/roles",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "updateCustomRole",
    "method": "PUT",
    "path": "/roles/{role}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "deleteCustomRole",
    "method": "DELETE",
    "path": "/roles/{role}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getOrganizationSecurity",
    "method": "GET",
    "path": "/organization/security",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "updateOrganizationSecurity",
    "method": "PUT",
    "path": "/organization/security",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setTeamUsername",
    "method": "PUT",
    "path": "/teams/{id}/members/{user}/username",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getHostAccess",
    "method": "GET",
    "path": "/host-access",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "grantHostAccess",
    "method": "PUT",
    "path": "/host-access/{user}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "revokeHostAccess",
    "method": "DELETE",
    "path": "/host-access/{user}/{node}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createEnvironment",
    "method": "POST",
    "path": "/projects/{project}/environments",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getProxy",
    "method": "GET",
    "path": "/settings/haproxy",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setProxy",
    "method": "PATCH",
    "path": "/settings/haproxy",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listRequests",
    "method": "GET",
    "path": "/requests",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "requestRouting",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/requests/routing",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deleteEmptyProject",
    "method": "DELETE",
    "path": "/projects/{id}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "deleteEmptyEnvironment",
    "method": "DELETE",
    "path": "/projects/{project}/environments/{environment}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listRetainedApplicationData",
    "method": "GET",
    "path": "/storage/retained",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deleteRetainedApplicationData",
    "method": "DELETE",
    "path": "/storage/retained/{id}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "retryServiceVolumeCleanup",
    "method": "POST",
    "path": "/deployments/{id}/volume-cleanup",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getServiceRuntime",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/runtime",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "restartService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/restart",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "scaleService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/scale",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listRegistries",
    "method": "GET",
    "path": "/registries",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createRegistry",
    "method": "POST",
    "path": "/registries",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "updateRegistry",
    "method": "PUT",
    "path": "/registries/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "deleteRegistry",
    "method": "DELETE",
    "path": "/registries/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "syncRegistry",
    "method": "POST",
    "path": "/registries/{name}/sync",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getServiceTLS",
    "method": "GET",
    "path": "/applications/{id}/services/{service}/tls",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "attachServiceTLS",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/tls",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listApplicationTLSIssuers",
    "method": "GET",
    "path": "/applications/{id}/tls/issuers",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createApplicationTLSIssuer",
    "method": "POST",
    "path": "/applications/{id}/tls/issuers",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listTLSIssuers",
    "method": "GET",
    "path": "/tls/issuers",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createTLSIssuer",
    "method": "POST",
    "path": "/tls/issuers",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "cordonNode",
    "method": "POST",
    "path": "/nodes/{name}/cordon",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "drainNode",
    "method": "POST",
    "path": "/nodes/{name}/drain",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listNodeEnrollments",
    "method": "GET",
    "path": "/nodes/enrollments",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "createNodeEnrollment",
    "method": "POST",
    "path": "/nodes/enrollments",
    "category": "credentials",
    "boundary": "installation"
  },
  {
    "id": "revokeNodeEnrollment",
    "method": "DELETE",
    "path": "/nodes/enrollments/{id}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "stopService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/stop",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "resumeService",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/resume",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getDatabaseOperation",
    "method": "GET",
    "path": "/database-operations/{id}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "listSecretProviders",
    "method": "GET",
    "path": "/secret-providers",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getSecretProvider",
    "method": "GET",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "putSecretProvider",
    "method": "PUT",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "deleteSecretProvider",
    "method": "DELETE",
    "path": "/secret-providers/{name}",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getAppearance",
    "method": "GET",
    "path": "/settings/appearance",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setAppearance",
    "method": "PATCH",
    "path": "/settings/appearance",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "getSlackIntegration",
    "method": "GET",
    "path": "/integrations/slack",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "disconnectSlack",
    "method": "DELETE",
    "path": "/integrations/slack",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listSlackChannels",
    "method": "GET",
    "path": "/integrations/slack/channels",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setSlackChannel",
    "method": "PUT",
    "path": "/integrations/slack/channel",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "setSlackEvents",
    "method": "PUT",
    "path": "/integrations/slack/events",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listSlackDeliveries",
    "method": "GET",
    "path": "/integrations/slack/deliveries",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "testSlack",
    "method": "POST",
    "path": "/integrations/slack/test",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "githubStatus",
    "method": "GET",
    "path": "/integrations/github",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "configureGitHub",
    "method": "PUT",
    "path": "/integrations/github",
    "category": "credentials",
    "boundary": "installation"
  },
  {
    "id": "getSource",
    "method": "GET",
    "path": "/applications/{id}/source",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "setSource",
    "method": "PUT",
    "path": "/applications/{id}/source",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "planSource",
    "method": "POST",
    "path": "/applications/{id}/source/plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "gitlabStatus",
    "method": "GET",
    "path": "/integrations/gitlab",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "configureGitLab",
    "method": "PUT",
    "path": "/integrations/gitlab",
    "category": "credentials",
    "boundary": "installation"
  },
  {
    "id": "planSourceImport",
    "method": "POST",
    "path": "/sources/plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listTemplates",
    "method": "GET",
    "path": "/templates",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "planTemplate",
    "method": "POST",
    "path": "/templates/{id}/plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "putTemplateSecret",
    "method": "PUT",
    "path": "/templates/{id}/secrets/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getShowcase",
    "method": "GET",
    "path": "/showcase",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "removeShowcase",
    "method": "POST",
    "path": "/showcase/remove",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "listVirtualNetworks",
    "method": "GET",
    "path": "/virtual-networks",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createVirtualNetwork",
    "method": "POST",
    "path": "/virtual-networks",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getVirtualNetwork",
    "method": "GET",
    "path": "/virtual-networks/{name}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "updateVirtualNetwork",
    "method": "PUT",
    "path": "/virtual-networks/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "deleteVirtualNetwork",
    "method": "DELETE",
    "path": "/virtual-networks/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listVirtualNetworkCandidates",
    "method": "GET",
    "path": "/virtual-networks/{name}/candidates",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "planVirtualNetwork",
    "method": "POST",
    "path": "/virtual-networks/plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "planVolumeResize",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes/plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "startVolumeResize",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listVolumeResizes",
    "method": "GET",
    "path": "/applications/{id}/volume-resizes",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "volumeResizeAction",
    "method": "POST",
    "path": "/applications/{id}/volume-resizes/{resize}/{action}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "detectBuildFramework",
    "method": "POST",
    "path": "/builds/detect",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listApplicationPreviews",
    "method": "GET",
    "path": "/applications/{id}/previews",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "createPreview",
    "method": "POST",
    "path": "/applications/{id}/previews",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "getPreview",
    "method": "GET",
    "path": "/previews/{preview}",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "deletePreview",
    "method": "DELETE",
    "path": "/previews/{preview}",
    "category": "deploy",
    "boundary": "project"
  },
  {
    "id": "listWorkloadSecrets",
    "method": "GET",
    "path": "/secrets",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "putWorkloadSecret",
    "method": "PUT",
    "path": "/secrets/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "deleteWorkloadSecret",
    "method": "DELETE",
    "path": "/secrets/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "createWorkloadSecret",
    "method": "POST",
    "path": "/secrets/{name}",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listPlacementNodes",
    "method": "GET",
    "path": "/placement/nodes",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "checkSecretRequirements",
    "method": "POST",
    "path": "/deployment-secret-requirements",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "renameApplication",
    "method": "PUT",
    "path": "/applications/{id}/name",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "renameService",
    "method": "PUT",
    "path": "/applications/{id}/services/{service}/name",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "renameProject",
    "method": "PUT",
    "path": "/projects/{id}/name",
    "category": "admin",
    "boundary": "installation"
  },
  {
    "id": "planServiceMove",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/move-plan",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "startServiceMove",
    "method": "POST",
    "path": "/applications/{id}/services/{service}/move",
    "category": "write",
    "boundary": "project"
  },
  {
    "id": "listServiceMoves",
    "method": "GET",
    "path": "/applications/{id}/service-moves",
    "category": "read",
    "boundary": "project"
  },
  {
    "id": "finishServiceMove",
    "method": "POST",
    "path": "/applications/{id}/service-moves/{move}/finish",
    "category": "write",
    "boundary": "project"
  }
] as const
