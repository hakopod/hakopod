# Interface coverage inventory

Contract operations: 329. Literal dashboard calls: 368.

This is source coverage. It is not runtime verification. Dynamic and alternate transports require manual review.

| Operation | Method and path | Current interface | Dashboard call sites |
| --- | --- | --- | --- |
| acceptHumanInvite | POST /auth/invites/accept | human or protocol prerequisite | No literal typed call |
| acceptManagedPlatform | POST /managed-platforms/operations | generic scoped invocation | web/src/routes/platforms.new.tsx:343<br>web/src/routes/platforms.$platformId.delete.tsx:13 |
| acceptManagedPlatformRecovery | POST /managed-platform-recovery/operations | generic scoped invocation | web/src/components/platform-recovery.tsx:197 |
| acknowledgeAlarm | POST /alarms/{id}/acknowledge | generic scoped invocation | No literal typed call |
| activateLicense | PUT /license | separate installation invocation | web/src/components/license-settings.tsx:104 |
| applicationProvenance | GET /applications/{id}/provenance | generic scoped invocation | No literal typed call |
| applyLegacyExternalDatabaseBindingReview | POST /external-databases/{id}/connect | generic scoped invocation | web/src/routes/databases.external.$externalDatabaseId.connect.tsx:63 |
| approveDeviceAuthorization | POST /auth/device/approve | human or protocol prerequisite | web/src/routes/login.device.tsx:50 |
| attachServiceTLS | POST /applications/{id}/services/{service}/tls | generic scoped invocation | web/src/components/tls-settings.tsx:448 |
| attestDatabaseInspection | POST /databases/{id}/inspect | generic scoped invocation | web/src/components/database-inspection.tsx:30 |
| beginDomainVerification | POST /applications/{id}/domains | generic scoped invocation | web/src/routes/applications.$applicationId.domains.tsx:513 |
| beginPasskeyLogin | POST /auth/passkeys/login/start | human or protocol prerequisite | web/src/components/auth-screen.tsx:193 |
| beginPasskeyRegistration | POST /auth/passkeys/register/start | human or protocol prerequisite | web/src/components/account-settings.tsx:404 |
| beginTOTP | POST /auth/mfa/totp/start | human or protocol prerequisite | web/src/components/account-settings.tsx:377 |
| cancelActionsJob | POST /applications/{id}/actions/{service}/jobs/{slot}/cancel | generic scoped invocation | web/src/lib/actions-cancellation.ts:72 |
| cancelBackup | POST /backups/{id}/cancel | generic scoped invocation | web/src/routes/backups.$jobId.tsx:142 |
| cancelDeployment | POST /deployments/{id}/cancel | dedicated workflow | web/src/routes/deployments.$deploymentId.tsx:570 |
| cancelManagedPlatformRecoveryOperation | POST /managed-platform-recovery-operations/{id}/cancel | generic scoped invocation | web/src/components/platform-recovery.tsx:447 |
| cancelSourceBuild | POST /builds/{id}/runs/{run}/cancel | generic scoped invocation | web/src/routes/builds.$buildId.tsx:879 |
| checkSecretRequirements | POST /deployment-secret-requirements | generic scoped invocation | web/src/components/deployment-secrets.tsx:74 |
| closeHostTerminal | DELETE /nodes/{node}/terminal/{session} | dedicated workflow | No literal typed call |
| closeMCPSession | DELETE /mcp | human or protocol prerequisite | No literal typed call |
| completeGitHubAppInstallation | POST /git/github/install/complete | human or protocol prerequisite | web/src/components/git-app-setup.tsx:237 |
| completeGitHubAppManifest | POST /git/github/complete | human or protocol prerequisite | web/src/components/git-app-setup.tsx:248 |
| completeGitSourceOAuth | POST /git/oauth/complete | human or protocol prerequisite | web/src/components/git-oauth.tsx:107 |
| completeNamedGitSourceOAuth | POST /git/connections/{id}/oauth/complete | human or protocol prerequisite | No literal typed call |
| completeOnboarding | POST /auth/onboarding | human or protocol prerequisite | No literal typed call |
| completeProviderMFA | POST /auth/mfa/complete | human or protocol prerequisite | No literal typed call |
| configureGitHub | PUT /integrations/github | separate installation invocation | No literal typed call |
| configureGitLab | PUT /integrations/gitlab | separate installation invocation | No literal typed call |
| confirmTOTP | POST /auth/mfa/totp/confirm | human or protocol prerequisite | web/src/components/account-settings.tsx:381 |
| connectSlack | POST /integrations/slack/connect | human or protocol prerequisite | web/src/components/slack-settings.tsx:300<br>web/src/components/slack-settings.tsx:906<br>web/src/components/slack-settings.tsx:908 |
| convertCompose | POST /compose/convert | generic scoped invocation | web/src/components/compose-import.tsx:61 |
| cordonNode | POST /nodes/{name}/cordon | separate installation invocation | web/src/components/node-controls.tsx:174 |
| createApplicationDNSRecords | POST /applications/{id}/domains/dns-records | generic scoped invocation | web/src/routes/applications.$applicationId.domains.tsx:109 |
| createApplicationTLSIssuer | POST /applications/{id}/tls/issuers | generic scoped invocation | web/src/components/tls-settings.tsx:660 |
| createBackup | POST /backups | generic scoped invocation | web/src/components/backup-run-form.tsx:47 |
| createBackupDestination | POST /backup-destinations | generic scoped invocation | web/src/components/backup-destination-form.tsx:141 |
| createBackupSchedule | POST /backup-schedules | generic scoped invocation | web/src/components/backup-schedule-form.tsx:81 |
| createCustomRole | POST /roles | separate installation invocation | web/src/components/pro-access.tsx:191 |
| createDeployment | POST /deployments | dedicated workflow | web/src/components/backend-certificate-form.tsx:124<br>web/src/components/delete-service-dialog.tsx:71<br>web/src/components/service-environment-form.tsx:139<br>web/src/components/deploy-dialog.tsx:318<br>web/src/components/virtual-network-connect.tsx:130<br>web/src/components/managed-actions-form.tsx:474<br>web/src/routes/applications.$applicationId.domains.tsx:598 |
| createDeploymentNotification | POST /applications/{id}/notifications | dedicated workflow | web/src/routes/applications.$applicationId.notifications.tsx:161 |
| createEnvironment | POST /projects/{project}/environments | separate installation invocation | web/src/components/project-environment.tsx:58 |
| createGitConnection | POST /git/connections | generic scoped invocation | web/src/components/git-connections.tsx:402 |
| createKey | POST /keys | separate installation invocation | web/src/routes/settings.tsx:505 |
| createManagedDatabase | POST /databases | generic scoped invocation | web/src/components/database-form.tsx:139<br>web/src/components/database-create.tsx:180 |
| createNodeEnrollment | POST /nodes/enrollments | separate installation invocation | web/src/components/node-controls.tsx:370 |
| createPreview | POST /applications/{id}/previews | generic scoped invocation | web/src/routes/applications.$applicationId.previews.new.tsx:125 |
| createProject | POST /projects | separate installation invocation | web/src/components/project-wizard.tsx:110 |
| createProjectInvite | POST /projects/{project}/invites | separate installation invocation | web/src/components/team-settings.tsx:820 |
| createRegistry | POST /registries | generic scoped invocation | web/src/components/registry-settings.tsx:255 |
| createSourceBuild | POST /builds | generic scoped invocation | web/src/components/build-form.tsx:431 |
| createTLSIssuer | POST /tls/issuers | separate installation invocation | web/src/components/tls-settings.tsx:664 |
| createTeam | POST /teams | separate installation invocation | web/src/components/team-settings.tsx:560 |
| createTeamInvite | POST /teams/{id}/invites | separate installation invocation | web/src/components/team-settings.tsx:814 |
| createTerminal | POST /applications/{id}/services/{service}/terminal | dedicated workflow | web/src/components/pod-terminal.tsx:202 |
| createVirtualNetwork | POST /virtual-networks | generic scoped invocation | web/src/components/virtual-network-form.tsx:171 |
| createWorkloadSecret | POST /secrets/{name} | generic scoped invocation | web/src/components/template-secret-field.tsx:85<br>web/src/components/deployment-secrets.tsx:87<br>web/src/lib/database-binding.ts:88 |
| deleteBackupArtifact | DELETE /backup-artifacts/{id} | generic scoped invocation | web/src/routes/backups.tsx:347 |
| deleteBackupDestination | DELETE /backup-destinations/{id} | generic scoped invocation | web/src/routes/backups.tsx:704 |
| deleteBackupSchedule | DELETE /backup-schedules/{id} | generic scoped invocation | web/src/routes/backups.tsx:711 |
| deleteCustomRole | DELETE /roles/{role} | separate installation invocation | web/src/components/pro-access.tsx:120 |
| deleteDNSProvider | DELETE /dns-providers/{name} | separate installation invocation | web/src/routes/settings.dns-providers.tsx:190 |
| deleteDeploymentNotification | DELETE /applications/{id}/notifications/{target} | dedicated workflow | web/src/routes/applications.$applicationId.notifications.tsx:200 |
| deleteEmptyApplication | DELETE /applications/{id} | generic scoped invocation | web/src/components/delete-resource.tsx:63 |
| deleteEmptyEnvironment | DELETE /projects/{project}/environments/{environment} | separate installation invocation | web/src/components/project-settings.tsx:96 |
| deleteEmptyProject | DELETE /projects/{id} | separate installation invocation | web/src/components/delete-resource.tsx:74 |
| deleteExternalDatabase | DELETE /external-databases/{id} | generic scoped invocation | web/src/routes/databases.external.$externalDatabaseId.tsx:98 |
| deleteGitConnection | DELETE /git/connections/{id} | generic scoped invocation | web/src/components/git-connections.tsx:758 |
| deleteManagedDatabase | DELETE /databases/{id} | generic scoped invocation | web/src/routes/databases.$databaseId.tsx:319 |
| deletePasskey | DELETE /auth/passkeys/{id} | human or protocol prerequisite | web/src/components/account-settings.tsx:395 |
| deletePreview | DELETE /previews/{preview} | generic scoped invocation | web/src/components/application-previews.tsx:161 |
| deleteRegistry | DELETE /registries/{name} | generic scoped invocation | web/src/components/registry-settings.tsx:177 |
| deleteRetainedApplicationData | DELETE /storage/retained/{id} | generic scoped invocation | web/src/components/retained-storage.tsx:48 |
| deleteSecretProvider | DELETE /secret-providers/{name} | separate installation invocation | web/src/components/secret-providers.tsx:156 |
| deleteTeam | DELETE /teams/{id} | separate installation invocation | web/src/components/team-settings.tsx:521 |
| deleteTerminal | DELETE /applications/{id}/services/{service}/terminal/{session} | dedicated workflow | No literal typed call |
| deleteVirtualNetwork | DELETE /virtual-networks/{name} | generic scoped invocation | web/src/routes/networks.$networkName.tsx:326 |
| deleteWorkloadSecret | DELETE /secrets/{name} | generic scoped invocation | web/src/components/application-secrets.tsx:165 |
| deploySource | POST /applications/{id}/source/deploy | dedicated workflow | web/src/components/application-source.tsx:232 |
| deploySourceBuild | POST /builds/{id}/runs/{run}/deploy | dedicated workflow | web/src/routes/builds.$buildId.tsx:830 |
| deploySourceImport | POST /sources/deploy | dedicated workflow | web/src/routes/applications.import.tsx:78 |
| deployTemplate | POST /templates/{id}/deploy | dedicated workflow | web/src/components/template-form.tsx:189 |
| deploymentNotifications | GET /applications/{id}/notifications | dedicated workflow | web/src/routes/applications.$applicationId.notifications.tsx:62 |
| detectBuildFramework | POST /builds/detect | generic scoped invocation | web/src/components/build-form.tsx:175 |
| disableTOTP | POST /auth/mfa/totp/disable | human or protocol prerequisite | web/src/components/account-settings.tsx:390 |
| discardDomainVerification | DELETE /applications/{id}/domains/{hostname} | generic scoped invocation | web/src/routes/applications.$applicationId.domains.tsx:465 |
| disconnectSlack | DELETE /integrations/slack | separate installation invocation | web/src/components/slack-settings.tsx:463 |
| drainNode | POST /nodes/{name}/drain | separate installation invocation | web/src/components/node-controls.tsx:168 |
| executePodCommand | POST /applications/{id}/services/{service}/exec | dedicated workflow | No literal typed call |
| exportUserAuditHistory | GET /audit/export | dedicated workflow | No literal typed call |
| finishPasskeyLogin | POST /auth/passkeys/login/finish | human or protocol prerequisite | web/src/components/auth-screen.tsx:196 |
| finishPasskeyRegistration | POST /auth/passkeys/register/finish | human or protocol prerequisite | web/src/components/account-settings.tsx:410 |
| finishProviderLogin | GET /auth/oauth/{provider}/callback | human or protocol prerequisite | No literal typed call |
| finishServiceMove | POST /applications/{id}/service-moves/{move}/finish | generic scoped invocation | web/src/components/move-service-dialog.tsx:385 |
| getAccountSecurity | GET /auth/security | human or protocol prerequisite | web/src/components/account-settings.tsx:27 |
| getActionsJobLogs | GET /applications/{id}/actions/{service}/jobs/{slot}/logs | generic scoped invocation | web/src/components/managed-actions-workflows.tsx:347 |
| getActionsProviderHold | GET /applications/{id}/actions/{service}/hold | generic scoped invocation | web/src/components/managed-actions-hold.tsx:145 |
| getAlarmSettings | GET /alarm-settings | generic scoped invocation | web/src/lib/alarms.ts:44 |
| getAppearance | GET /settings/appearance | separate installation invocation | No literal typed call |
| getApplication | GET /applications/{id} | generic scoped invocation | web/src/components/backend-certificate-form.tsx:79<br>web/src/components/shell.tsx:236<br>web/src/components/build-form.tsx:79<br>web/src/components/service-environment-form.tsx:63<br>web/src/components/move-service-dialog.tsx:62<br>web/src/components/virtual-network-connect.tsx:66<br>web/src/components/virtual-network-connect.tsx:86<br>web/src/routes/templates.$templateId.tsx:17<br>web/src/routes/applications.$applicationId.previews.new.tsx:27<br>web/src/routes/applications.$applicationId.previews.new.tsx:238<br>web/src/routes/databases.$databaseId.connect.tsx:75<br>web/src/routes/deployments.$deploymentId.tsx:56<br>web/src/routes/applications.$applicationId.services.new.tsx:27<br>web/src/routes/applications.$applicationId.source.tsx:19<br>web/src/routes/applications.$applicationId.configure.tsx:31<br>web/src/routes/applications.$applicationId.tsx:126<br>web/src/routes/builds.new.tsx:22<br>web/src/routes/applications.$applicationId.notifications.tsx:55<br>web/src/routes/applications.$applicationId.domains.tsx:38<br>web/src/routes/applications.$applicationId.domains.tsx:133<br>web/src/routes/applications.$applicationId.environment.tsx:22<br>web/src/routes/applications.$applicationId.certificates.tsx:21 |
| getAuthStatus | GET /auth/status | human or protocol prerequisite | web/src/components/team-settings.tsx:784<br>web/src/components/auth-screen.tsx:67<br>web/src/lib/installation-settings.ts:22 |
| getBackup | GET /backups/{id} | generic scoped invocation | web/src/routes/backups.$jobId.tsx:21 |
| getBackupArtifact | GET /backup-artifacts/{id} | generic scoped invocation | web/src/routes/databases.import.tsx:62<br>web/src/routes/backups.artifacts.$artifactId.restore.tsx:35 |
| getCloudCapabilities | GET /cloud/capabilities | generic scoped invocation | No literal typed call |
| getDatabaseImport | GET /backup-imports/{id} | generic scoped invocation | web/src/routes/databases.import.tsx:53 |
| getDatabaseMetricHistory | GET /databases/{id}/metrics | generic scoped invocation | web/src/components/database-cockpit.tsx:106 |
| getDatabaseOperation | GET /database-operations/{id} | generic scoped invocation | web/src/routes/databases.$databaseId.resize-retry.tsx:31<br>web/src/routes/databases.$databaseId.switchover.tsx:33 |
| getDatabasePublicEndpointCapabilities | GET /databases/{id}/public-endpoint-capabilities | generic scoped invocation | web/src/lib/databases.ts:55 |
| getDatabasePublicEndpointOperation | GET /database-public-endpoint-operations/{id} | generic scoped invocation | web/src/routes/databases.$databaseId.public-endpoints.tsx:91 |
| getDatabasePublicTrust | GET /databases/{id}/trust | generic scoped invocation | web/src/components/database-security.tsx:125 |
| getDatabaseQueryCapabilities | GET /databases/{id}/query-capabilities | generic scoped invocation | web/src/lib/databases.ts:22 |
| getDeployment | GET /deployments/{id} | dedicated workflow | web/src/components/shell.tsx:210<br>web/src/components/parent-navigation.tsx:28<br>web/src/routes/deployments.$deploymentId.tsx:43<br>web/src/routes/deployments.$deploymentId.tsx:71<br>web/src/routes/builds.$buildId.tsx:568 |
| getDeviceConsent | GET /auth/device | human or protocol prerequisite | web/src/routes/login.device.tsx:26 |
| getExternalDatabase | GET /external-databases/{id} | generic scoped invocation | web/src/lib/external-databases.ts:20 |
| getExternalDatabaseOperation | GET /external-database-operations/{id} | generic scoped invocation | No literal typed call |
| getExternalDatabaseTrust | GET /external-databases/{id}/trust | generic scoped invocation | No literal typed call |
| getGitConnection | GET /git/connections/{id} | generic scoped invocation | web/src/components/git-connections.tsx:125 |
| getGitProviderSetup | GET /git/setup | generic scoped invocation | web/src/lib/git-connections.ts:51 |
| getHostAccess | GET /host-access | separate installation invocation | web/src/routes/settings.host-access.tsx:25<br>web/src/routes/infrastructure.nodes.$node.terminal.tsx:14 |
| getIdempotentDeployment | GET /idempotency/{key} | dedicated workflow | No literal typed call |
| getIdentity | GET /me | separate installation invocation | web/src/components/shell.tsx:71 |
| getInstallationLoginProvider | GET /installation/login-providers/{provider} | separate installation invocation | web/src/components/login-provider-settings.tsx:37 |
| getInstallationLogs | GET /installation/logs | separate installation invocation | No literal typed call |
| getInstallationSMTP | GET /installation/smtp | separate installation invocation | web/src/components/smtp-settings.tsx:28 |
| getInstallationSetup | GET /installation/setup | separate installation invocation | web/src/components/installation.tsx:42 |
| getInstallationStatus | GET /installation/status | separate installation invocation | web/src/components/installation.tsx:149<br>web/src/components/installation.tsx:283 |
| getLicenseStatus | GET /license | separate installation invocation | web/src/lib/license.ts:6 |
| getManagedActions | GET /applications/{id}/actions | generic scoped invocation | web/src/components/managed-actions-workflows.tsx:93<br>web/src/components/managed-actions-status.tsx:23 |
| getManagedActionsCapabilities | GET /actions/capabilities | generic scoped invocation | web/src/components/managed-actions-form.tsx:154 |
| getManagedDatabase | GET /databases/{id} | generic scoped invocation | web/src/lib/databases.ts:86 |
| getManagedPlatform | GET /managed-platforms/{id} | generic scoped invocation | web/src/lib/managed-platforms.ts:61 |
| getManagedPlatformCatalog | GET /managed-platforms/catalog | generic scoped invocation | web/src/lib/managed-platforms.ts:47 |
| getManagedPlatformOperation | GET /managed-platform-operations/{id} | generic scoped invocation | No literal typed call |
| getManagedPlatformPublicTrust | GET /managed-platforms/{id}/trust | generic scoped invocation | web/src/components/platform-security.tsx:31 |
| getManagedPlatformRecoveryOperation | GET /managed-platform-recovery-operations/{id} | generic scoped invocation | No literal typed call |
| getOnboarding | GET /auth/onboarding | human or protocol prerequisite | web/src/routes/login.onboarding.tsx:16 |
| getOrganizationSecurity | GET /organization/security | separate installation invocation | web/src/components/pro-access.tsx:284 |
| getPreview | GET /previews/{preview} | generic scoped invocation | No literal typed call |
| getProfile | GET /auth/profile | human or protocol prerequisite | web/src/routes/settings.profile.tsx:17 |
| getProxy | GET /settings/haproxy | separate installation invocation | web/src/components/proxy-settings.tsx:38<br>web/src/components/proxy-settings.tsx:251 |
| getSecretProvider | GET /secret-providers/{name} | separate installation invocation | web/src/components/secret-provider-form.tsx:121<br>web/src/components/secret-provider-editor.tsx:15 |
| getServiceDelivery | GET /applications/{id}/services/{service}/delivery | generic scoped invocation | web/src/components/service-delivery.tsx:23 |
| getServiceRuntime | GET /applications/{id}/services/{service}/runtime | generic scoped invocation | web/src/components/pod-terminal.tsx:65<br>web/src/components/service-detail.tsx:66<br>web/src/components/logs.tsx:69 |
| getServiceTLS | GET /applications/{id}/services/{service}/tls | generic scoped invocation | web/src/components/tls-settings.tsx:60 |
| getShowcase | GET /showcase | generic scoped invocation | web/src/components/sample-banner.tsx:19 |
| getSlackIntegration | GET /integrations/slack | separate installation invocation | web/src/components/slack-settings.tsx:59<br>web/src/components/slack-settings.tsx:998 |
| getSource | GET /applications/{id}/source | generic scoped invocation | web/src/components/application-source.tsx:36<br>web/src/routes/applications.$applicationId.source.tsx:26 |
| getSourceBuild | GET /builds/{id} | generic scoped invocation | web/src/components/shell.tsx:225<br>web/src/routes/builds.$buildId.edit.tsx:16<br>web/src/routes/builds.$buildId.tsx:67 |
| getVirtualNetwork | GET /virtual-networks/{name} | generic scoped invocation | web/src/components/virtual-network-connect.tsx:92<br>web/src/components/virtual-network-form.tsx:97<br>web/src/routes/networks.$networkName.tsx:57<br>web/src/routes/networks.$networkName.tsx:283<br>web/src/routes/networks.$networkName.connect.tsx:18 |
| gitHubAppWebhook | POST /webhooks/github-app/{app} | human or protocol prerequisite | No literal typed call |
| githubStatus | GET /integrations/github | separate installation invocation | No literal typed call |
| githubWebhook | POST /webhooks/github | human or protocol prerequisite | No literal typed call |
| gitlabStatus | GET /integrations/gitlab | separate installation invocation | No literal typed call |
| gitlabWebhook | POST /webhooks/gitlab | human or protocol prerequisite | No literal typed call |
| grantHostAccess | PUT /host-access/{user} | separate installation invocation | web/src/routes/settings.host-access.tsx:76 |
| inspectInvite | POST /auth/invites/inspect | human or protocol prerequisite | web/src/components/auth-screen.tsx:87 |
| installBuildWorkflow | POST /builds/{id}/install | generic scoped invocation | web/src/routes/builds.$buildId.tsx:437 |
| listActionsJobs | GET /applications/{id}/actions/{service}/jobs | generic scoped invocation | web/src/components/managed-actions-workflows.tsx:80 |
| listAlarms | GET /alarms | generic scoped invocation | web/src/lib/alarms.ts:27 |
| listApplicationDNSProviders | GET /applications/{id}/domains/dns-providers | generic scoped invocation | web/src/routes/applications.$applicationId.domains.tsx:56 |
| listApplicationDomains | GET /applications/{id}/domains | generic scoped invocation | web/src/routes/applications.$applicationId.tsx:135<br>web/src/routes/applications.$applicationId.domains.tsx:45 |
| listApplicationPreviews | GET /applications/{id}/previews | generic scoped invocation | web/src/components/application-previews.tsx:35 |
| listApplicationTLSIssuers | GET /applications/{id}/tls/issuers | generic scoped invocation | web/src/components/tls-settings.tsx:35 |
| listApplications | GET /applications | generic scoped invocation | web/src/components/command-palette.tsx:48<br>web/src/components/move-service-dialog.tsx:43<br>web/src/components/compute-notice.tsx:14<br>web/src/components/application-list.tsx:62<br>web/src/routes/databases.$databaseId.connect.tsx:62 |
| listAudit | GET /audit | separate installation invocation | web/src/routes/settings.tsx:722 |
| listBackendCertificates | GET /applications/{id}/services/{service}/certificates | generic scoped invocation | web/src/components/service-delivery.tsx:31 |
| listBackupArtifacts | GET /backup-artifacts | generic scoped invocation | web/src/components/database-backups.tsx:13<br>web/src/routes/backups.tsx:155<br>web/src/routes/databases.$databaseId.recover.tsx:25 |
| listBackupDestinations | GET /backup-destinations | generic scoped invocation | web/src/lib/backups.ts:63 |
| listBackupSchedules | GET /backup-schedules | generic scoped invocation | web/src/lib/backups.ts:77 |
| listBackupTargets | GET /backup-targets | generic scoped invocation | web/src/lib/backups.ts:70 |
| listBackups | GET /backups | generic scoped invocation | web/src/routes/backups.tsx:146 |
| listCustomRoles | GET /roles | separate installation invocation | web/src/components/pro-access.tsx:24 |
| listDNSProviders | GET /dns-providers | separate installation invocation | web/src/components/dns-provider-credential-fields.tsx:320<br>web/src/routes/settings.dns-providers.tsx:58 |
| listDatabaseConnections | GET /databases/{id}/connections | generic scoped invocation | web/src/lib/databases.ts:73 |
| listDatabaseOperations | GET /databases/{id}/operations | generic scoped invocation | web/src/lib/databases.ts:36 |
| listDatabasePlacementNodes | GET /database-placement/nodes | generic scoped invocation | web/src/lib/databases.ts:29 |
| listDatabasePublicEndpoints | GET /databases/{id}/public-endpoints | generic scoped invocation | web/src/lib/databases.ts:44 |
| listExternalDatabaseConnections | GET /external-databases/{id}/connections | generic scoped invocation | web/src/lib/external-databases.ts:27 |
| listExternalDatabases | GET /external-databases | generic scoped invocation | web/src/lib/external-databases.ts:13 |
| listGitConnections | GET /git/connections | generic scoped invocation | web/src/lib/git-connections.ts:18 |
| listGitRepositories | GET /git/connections/{id}/repositories | generic scoped invocation | web/src/components/git-repository-field.tsx:50 |
| listHumanSessions | GET /auth/sessions | human or protocol prerequisite | web/src/components/account-settings.tsx:33 |
| listKeys | GET /keys | separate installation invocation | web/src/routes/settings.tsx:210 |
| listManagedDatabases | GET /databases | generic scoped invocation | web/src/lib/databases.ts:96 |
| listManagedPlatformOperations | GET /managed-platforms/{id}/operations | generic scoped invocation | web/src/lib/managed-platforms.ts:72 |
| listManagedPlatformRecoveryOperations | GET /managed-platforms/{id}/recovery-operations | generic scoped invocation | web/src/lib/managed-platforms.ts:84 |
| listManagedPlatforms | GET /managed-platforms | generic scoped invocation | web/src/lib/managed-platforms.ts:32 |
| listNodeEnrollments | GET /nodes/enrollments | separate installation invocation | web/src/components/node-controls.tsx:213 |
| listNodes | GET /nodes | separate installation invocation | web/src/components/node-controls.tsx:140<br>web/src/routes/infrastructure.tsx:208 |
| listPlacementNodes | GET /placement/nodes | generic scoped invocation | web/src/components/service-execution-fields.tsx:76<br>web/src/components/managed-actions-form.tsx:200<br>web/src/routes/applications.$applicationId.services.new.tsx:35 |
| listProjectMembers | GET /projects/{project}/members | separate installation invocation | web/src/components/team-settings.tsx:90 |
| listProjects | GET /projects | separate installation invocation | web/src/components/team-settings.tsx:73<br>web/src/lib/projects.ts:8 |
| listRegistries | GET /registries | generic scoped invocation | web/src/components/build-form.tsx:121<br>web/src/components/deploy-dialog.tsx:93<br>web/src/components/registry-settings.tsx:27<br>web/src/components/registry-editor-page.tsx:14 |
| listRequests | GET /requests | generic scoped invocation | web/src/components/requests.tsx:74 |
| listRetainedApplicationData | GET /storage/retained | generic scoped invocation | web/src/components/retained-storage.tsx:36 |
| listSecretProviders | GET /secret-providers | separate installation invocation | web/src/components/secret-providers.tsx:26 |
| listServiceMoves | GET /applications/{id}/service-moves | generic scoped invocation | web/src/components/move-service-dialog.tsx:271 |
| listSlackChannels | GET /integrations/slack/channels | separate installation invocation | web/src/components/slack-settings.tsx:789<br>web/src/components/slack-settings.tsx:814 |
| listSlackDeliveries | GET /integrations/slack/deliveries | separate installation invocation | web/src/components/slack-settings.tsx:70 |
| listSourceBuildRuns | GET /builds/{id}/runs | generic scoped invocation | web/src/routes/builds.$buildId.tsx:73 |
| listSourceBuilds | GET /builds | generic scoped invocation | web/src/routes/builds.tsx:25 |
| listTLSIssuers | GET /tls/issuers | separate installation invocation | web/src/components/tls-settings.tsx:33 |
| listTeamMembers | GET /teams/{id}/members | separate installation invocation | web/src/components/team-settings.tsx:82 |
| listTeams | GET /teams | separate installation invocation | web/src/components/team-settings.tsx:52 |
| listTemplates | GET /templates | generic scoped invocation | web/src/routes/templates.tsx:46<br>web/src/routes/templates.$templateId.tsx:26 |
| listUserAuditHistory | GET /audit/history | separate installation invocation | web/src/components/audit-history.tsx:30 |
| listUsers | GET /users | separate installation invocation | web/src/components/audit-history.tsx:23<br>web/src/components/team-settings.tsx:932<br>web/src/routes/settings.host-access.tsx:30 |
| listVirtualNetworkCandidates | GET /virtual-networks/{name}/candidates | generic scoped invocation | web/src/components/virtual-network-connect.tsx:50 |
| listVirtualNetworks | GET /virtual-networks | generic scoped invocation | web/src/routes/networks.tsx:26<br>web/src/routes/networks.new.tsx:18 |
| listVolumeResizes | GET /applications/{id}/volume-resizes | generic scoped invocation | web/src/components/volume-resize.tsx:46 |
| listWorkloadSecrets | GET /secrets | generic scoped invocation | web/src/components/service-secrets.tsx:34<br>web/src/components/application-secrets.tsx:33<br>web/src/components/template-form.tsx:96<br>web/src/routes/databases.$databaseId.connect.tsx:92 |
| loginWithPassword | POST /auth/login | human or protocol prerequisite | No literal typed call |
| logoutHuman | POST /auth/logout | human or protocol prerequisite | No literal typed call |
| mcpMessage | POST /mcp | human or protocol prerequisite | No literal typed call |
| namedGitWebhook | POST /webhooks/git/{connection} | human or protocol prerequisite | No literal typed call |
| observeSourceBuild | GET /builds/{id}/runs/{run} | generic scoped invocation | web/src/routes/builds.$buildId.tsx:555 |
| openHostTerminal | POST /nodes/{node}/terminal | dedicated workflow | web/src/components/pod-terminal.tsx:193 |
| planBackupRestore | POST /backup-artifacts/{id}/restore-plan | generic scoped invocation | web/src/routes/backups.artifacts.$artifactId.restore.tsx:275 |
| planDeployment | POST /plan | dedicated workflow | web/src/components/backend-certificate-form.tsx:96<br>web/src/components/delete-service-dialog.tsx:56<br>web/src/components/service-environment-form.tsx:104<br>web/src/components/deploy-dialog.tsx:249<br>web/src/components/deploy-dialog.tsx:287<br>web/src/components/virtual-network-connect.tsx:108<br>web/src/components/managed-actions-form.tsx:438<br>web/src/routes/applications.$applicationId.domains.tsx:140 |
| planServiceMove | POST /applications/{id}/services/{service}/move-plan | generic scoped invocation | web/src/components/move-service-dialog.tsx:71 |
| planSource | POST /applications/{id}/source/plan | generic scoped invocation | web/src/components/application-source.tsx:132 |
| planSourceBuildDeployment | POST /builds/{id}/runs/{run}/plan | dedicated workflow | web/src/routes/builds.$buildId.tsx:727 |
| planSourceImport | POST /sources/plan | generic scoped invocation | web/src/routes/applications.import.tsx:90 |
| planTemplate | POST /templates/{id}/plan | generic scoped invocation | web/src/components/template-form.tsx:118<br>web/src/components/template-form.tsx:142 |
| planVirtualNetwork | POST /virtual-networks/plan | generic scoped invocation | web/src/components/virtual-network-form.tsx:60 |
| planVolumeResize | POST /applications/{id}/volume-resizes/plan | generic scoped invocation | web/src/components/volume-resize.tsx:198 |
| pollDeviceAuthorization | POST /auth/device/token | human or protocol prerequisite | No literal typed call |
| pollHostTerminal | GET /nodes/{node}/terminal/{session}/poll | dedicated workflow | No literal typed call |
| pollTerminal | GET /applications/{id}/services/{service}/terminal/{session}/poll | dedicated workflow | No literal typed call |
| previewBuildWorkflow | POST /builds/{id}/preview | generic scoped invocation | web/src/routes/builds.$buildId.tsx:90<br>web/src/routes/builds.$buildId.tsx:290 |
| publishDatabasePublicEndpoint | POST /databases/{id}/public-endpoints | generic scoped invocation | web/src/routes/databases.$databaseId.public-endpoints.tsx:243 |
| putAlarmSettings | PUT /alarm-settings | generic scoped invocation | web/src/routes/alarms.settings.tsx:154 |
| putDNSProvider | PUT /dns-providers/{name} | separate installation invocation | web/src/components/dns-provider-credential-fields.tsx:374 |
| putInstallationLoginProvider | PUT /installation/login-providers/{provider} | separate installation invocation | web/src/components/login-provider-settings.tsx:193 |
| putInstallationSMTP | PUT /installation/smtp | separate installation invocation | web/src/components/smtp-settings.tsx:152 |
| putSecretProvider | PUT /secret-providers/{name} | separate installation invocation | web/src/components/secret-provider-form.tsx:86 |
| putTemplateSecret | PUT /templates/{id}/secrets/{name} | generic scoped invocation | web/src/components/template-secret-field.tsx:90 |
| putWorkloadSecret | PUT /secrets/{name} | generic scoped invocation | web/src/components/application-secrets.tsx:210<br>web/src/lib/save-environment.ts:35 |
| queryInstallationLogs | POST /installation/logs/query | separate installation invocation | web/src/components/logs.tsx:83 |
| queryLogs | POST /applications/{id}/logs/query | generic scoped invocation | web/src/components/logs.tsx:89 |
| queryManagedDatabase | POST /databases/{id}/query | dedicated workflow | web/src/routes/databases.$databaseId.query.tsx:116 |
| readAlarm | POST /alarms/{id}/read | generic scoped invocation | No literal typed call |
| registerAccount | POST /auth/register | human or protocol prerequisite | No literal typed call |
| releaseActionsProviderHold | POST /applications/{id}/actions/{service}/hold/release | generic scoped invocation | web/src/lib/actions-hold.ts:36 |
| removeLicense | DELETE /license | separate installation invocation | web/src/components/license-settings.tsx:223 |
| removeShowcase | POST /showcase/remove | separate installation invocation | web/src/components/sample-banner.tsx:114 |
| renameApplication | PUT /applications/{id}/name | generic scoped invocation | web/src/components/rename-resource.tsx:174 |
| renameProject | PUT /projects/{id}/name | separate installation invocation | web/src/components/rename-resource.tsx:183 |
| renameService | PUT /applications/{id}/services/{service}/name | generic scoped invocation | web/src/components/rename-resource.tsx:167 |
| replaceDatabaseConnection | POST /databases/{id}/connect | generic scoped invocation | web/src/routes/databases.$databaseId.connect.tsx:211 |
| requestPasswordReset | POST /auth/password/forgot | human or protocol prerequisite | No literal typed call |
| requestRouting | GET /applications/{id}/services/{service}/requests/routing | generic scoped invocation | web/src/components/requests.tsx:450 |
| resetPassword | POST /auth/password/reset | human or protocol prerequisite | No literal typed call |
| resizeManagedDatabase | POST /databases/{id}/resize | generic scoped invocation | web/src/components/database-form.tsx:130 |
| restartService | POST /applications/{id}/services/{service}/restart | generic scoped invocation | web/src/components/service-detail.tsx:1160 |
| restoreBackup | POST /backup-artifacts/{id}/restore | generic scoped invocation | web/src/routes/databases.$databaseId.recover.tsx:104<br>web/src/routes/backups.artifacts.$artifactId.restore.tsx:256 |
| resumeGitHubAppSetup | POST /git/connections/{id}/github/setup | human or protocol prerequisite | web/src/components/git-app-setup.tsx:102 |
| resumeService | POST /applications/{id}/services/{service}/resume | generic scoped invocation | No literal typed call |
| retryManagedDatabaseResize | POST /databases/{id}/resize-retry | generic scoped invocation | web/src/routes/databases.$databaseId.resize-retry.tsx:72 |
| retryOracleDatabaseSwitchover | POST /databases/{id}/switchover-retry | generic scoped invocation | web/src/routes/databases.$databaseId.switchover.tsx:79 |
| retryServiceVolumeCleanup | POST /deployments/{id}/volume-cleanup | generic scoped invocation | web/src/routes/deployments.$deploymentId.tsx:259 |
| revealDatabaseCredentials | POST /databases/{id}/credentials | generic scoped invocation | web/src/routes/databases.$databaseId.tsx:429 |
| reviewDatabaseConnection | POST /databases/{id}/connection-plan | generic scoped invocation | web/src/routes/databases.$databaseId.connect.tsx:194 |
| reviewDatabaseImport | POST /backup-imports | generic scoped invocation | web/src/routes/databases.import.tsx:210 |
| reviewDatabasePublicEndpoint | POST /databases/{id}/public-endpoint-plan | generic scoped invocation | web/src/routes/databases.$databaseId.public-endpoints.tsx:196 |
| reviewDatabaseResize | POST /databases/{id}/resize-plan | generic scoped invocation | web/src/components/database-form.tsx:118 |
| reviewLegacyExternalDatabaseBinding | POST /external-databases/{id}/connection-plan | generic scoped invocation | web/src/routes/databases.external.$externalDatabaseId.connect.tsx:59 |
| reviewManagedDatabaseRecovery | POST /databases/{id}/restore-plan | generic scoped invocation | web/src/routes/databases.$databaseId.recover.tsx:94<br>web/src/routes/backups.artifacts.$artifactId.restore.tsx:265 |
| reviewManagedDatabaseResizeRetry | POST /databases/{id}/resize-retry-plan | generic scoped invocation | web/src/routes/databases.$databaseId.resize-retry.tsx:66 |
| reviewManagedPlatform | POST /managed-platforms/reviews | generic scoped invocation | web/src/routes/platforms.new.tsx:334<br>web/src/routes/platforms.$platformId.delete.tsx:13 |
| reviewManagedPlatformRecovery | POST /managed-platform-recovery/reviews | generic scoped invocation | web/src/components/platform-recovery.tsx:180 |
| reviewOracleDatabaseSwitchover | POST /databases/{id}/switchover-plan | generic scoped invocation | web/src/routes/databases.$databaseId.switchover.tsx:72 |
| revokeDatabasePublicEndpoint | DELETE /databases/{id}/public-endpoints/{endpoint} | generic scoped invocation | web/src/routes/databases.$databaseId.public-endpoints.tsx:280 |
| revokeHostAccess | DELETE /host-access/{user}/{node} | separate installation invocation | web/src/routes/settings.host-access.tsx:237 |
| revokeHumanSession | DELETE /auth/sessions/{id} | human or protocol prerequisite | web/src/components/account-settings.tsx:293 |
| revokeKey | DELETE /keys/{id} | separate installation invocation | web/src/routes/settings.tsx:381 |
| revokeNodeEnrollment | DELETE /nodes/enrollments/{id} | separate installation invocation | web/src/components/node-controls.tsx:321 |
| rollbackApplication | POST /applications/{id}/rollback | dedicated workflow | web/src/routes/applications.$applicationId.tsx:760 |
| rotateExternalDatabaseCredentials | PUT /external-databases/{id} | generic scoped invocation | web/src/routes/databases.external.$externalDatabaseId.edit.tsx:40 |
| rotateKey | POST /keys/{id}/rotate | separate installation invocation | web/src/routes/settings.tsx:499 |
| runSourceBuild | POST /builds/{id}/run | generic scoped invocation | web/src/routes/builds.$buildId.tsx:491 |
| scaleService | POST /applications/{id}/services/{service}/scale | generic scoped invocation | No literal typed call |
| setAppearance | PATCH /settings/appearance | separate installation invocation | No literal typed call |
| setProjectMember | PUT /projects/{project}/members | separate installation invocation | web/src/components/team-settings.tsx:309<br>web/src/components/team-settings.tsx:347 |
| setProxy | PATCH /settings/haproxy | separate installation invocation | web/src/components/proxy-settings.tsx:214 |
| setSlackChannel | PUT /integrations/slack/channel | separate installation invocation | web/src/components/slack-settings.tsx:957 |
| setSlackEvents | PUT /integrations/slack/events | separate installation invocation | web/src/components/slack-settings.tsx:944 |
| setSource | PUT /applications/{id}/source | generic scoped invocation | web/src/components/application-source.tsx:306 |
| setTeamMember | PUT /teams/{id}/members/{user} | separate installation invocation | web/src/components/team-settings.tsx:211 |
| setTeamUsername | PUT /teams/{id}/members/{user}/username | separate installation invocation | web/src/components/team-settings.tsx:441 |
| setupInstallerOwner | POST /auth/setup | human or protocol prerequisite | No literal typed call |
| startDeviceAuthorization | POST /auth/device/start | human or protocol prerequisite | No literal typed call |
| startGitHubAppManifest | POST /git/github/start | human or protocol prerequisite | web/src/components/git-app-setup.tsx:108 |
| startGitSourceOAuth | POST /git/connections/{id}/authorize | human or protocol prerequisite | web/src/components/git-oauth.tsx:56 |
| startProviderLogin | GET /auth/oauth/{provider}/start | human or protocol prerequisite | No literal typed call |
| startServiceMove | POST /applications/{id}/services/{service}/move | generic scoped invocation | web/src/components/move-service-dialog.tsx:80 |
| startVolumeResize | POST /applications/{id}/volume-resizes | generic scoped invocation | web/src/components/volume-resize.tsx:202 |
| stopService | POST /applications/{id}/services/{service}/stop | generic scoped invocation | No literal typed call |
| streamEvents | GET /deployments/{id}/events | dedicated workflow | No literal typed call |
| streamHostTerminal | GET /nodes/{node}/terminal/{session}/output | dedicated workflow | No literal typed call |
| streamLogs | GET /applications/{id}/logs | dedicated workflow | No literal typed call |
| switchoverOracleDatabase | POST /databases/{id}/switchover | generic scoped invocation | web/src/routes/databases.$databaseId.switchover.tsx:80 |
| syncRegistry | POST /registries/{name}/sync | generic scoped invocation | web/src/components/registry-settings.tsx:105 |
| terminalInput | POST /applications/{id}/services/{service}/terminal/{session}/input | dedicated workflow | No literal typed call |
| terminalOutput | GET /applications/{id}/services/{service}/terminal/{session}/output | dedicated workflow | No literal typed call |
| testBackupDestination | POST /backup-destinations/{id}/test | generic scoped invocation | web/src/routes/backups.tsx:431 |
| testDeploymentNotification | POST /applications/{id}/notifications/{target}/test | dedicated workflow | web/src/routes/applications.$applicationId.notifications.tsx:181 |
| testInstallationSMTP | POST /installation/smtp/test | separate installation invocation | web/src/components/smtp-settings.tsx:90 |
| testSlack | POST /integrations/slack/test | separate installation invocation | web/src/components/slack-settings.tsx:322 |
| updateBackupDestination | PUT /backup-destinations/{id} | generic scoped invocation | web/src/components/backup-destination-form.tsx:134 |
| updateBackupSchedule | PUT /backup-schedules/{id} | generic scoped invocation | web/src/components/backup-schedule-form.tsx:76 |
| updateCustomRole | PUT /roles/{role} | separate installation invocation | web/src/components/pro-access.tsx:190 |
| updateDeploymentNotification | PUT /applications/{id}/notifications/{target} | dedicated workflow | web/src/routes/applications.$applicationId.notifications.tsx:156 |
| updateGitConnection | PUT /git/connections/{id} | generic scoped invocation | web/src/components/git-connections.tsx:397 |
| updateOrganizationSecurity | PUT /organization/security | separate installation invocation | web/src/components/pro-access.tsx:366 |
| updateProfile | PATCH /auth/profile | human or protocol prerequisite | web/src/routes/settings.profile.tsx:59 |
| updateRegistry | PUT /registries/{name} | generic scoped invocation | web/src/components/registry-settings.tsx:254 |
| updateSourceBuild | PUT /builds/{id} | generic scoped invocation | web/src/components/build-form.tsx:429 |
| updateUser | PATCH /users/{id} | separate installation invocation | web/src/components/team-settings.tsx:1054 |
| updateVirtualNetwork | PUT /virtual-networks/{name} | generic scoped invocation | web/src/components/virtual-network-form.tsx:165 |
| upgradeInstallation | POST /installation/upgrade | separate installation invocation | web/src/components/installation.tsx:171 |
| uploadBackendCertificate | POST /applications/{id}/services/{service}/certificates | dedicated workflow | web/src/components/backend-certificate-form.tsx:60 |
| uploadDatabaseImport | PUT /backup-imports/{id}/archive | dedicated workflow | No literal typed call |
| verifyApplicationDomain | POST /applications/{id}/domains/{hostname}/verify | generic scoped invocation | web/src/routes/applications.$applicationId.domains.tsx:430 |
| verifyRegistration | POST /auth/register/verify | human or protocol prerequisite | No literal typed call |
| verifySessionMFA | POST /auth/mfa/verify | human or protocol prerequisite | web/src/components/account-settings.tsx:83 |
| volumeResizeAction | POST /applications/{id}/volume-resizes/{resize}/{action} | generic scoped invocation | web/src/components/volume-resize.tsx:232 |
| writeHostTerminal | POST /nodes/{node}/terminal/{session}/input | dedicated workflow | No literal typed call |
