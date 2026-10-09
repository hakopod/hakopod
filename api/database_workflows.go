package contract

const (
	DatabaseApplicationProvisioningPlanPath = "/api/v1/databases/{id}/application-provisioning-plan"
	DatabaseApplicationProvisionPath        = "/api/v1/databases/{id}/application-provision"
	DatabaseMigrationLockRecoveryPlanPath   = "/api/v1/databases/{id}/migration-lock-recovery-plan"
	DatabaseMigrationLockRecoverPath        = "/api/v1/databases/{id}/migration-lock-recover"
)

type DatabaseApplicationProvisioningPlanRequest struct {
	ApplicationID   string `json:"application_id"`
	Service         string `json:"service"`
	Variable        string `json:"variable"`
	Endpoint        string `json:"endpoint"`
	Role            string `json:"role"`
	Database        string `json:"database"`
	SecretReference string `json:"secret_reference"`
}
type DatabaseApplicationProvisionRequest struct {
	ReviewID           string `json:"review_id"`
	ConfirmApplication string `json:"confirm_application"`
}
type DatabaseMigrationLockRecoveryPlanRequest struct {
	ApplicationID string `json:"application_id"`
	Service       string `json:"service"`
	Variable      string `json:"variable"`
	Profile       string `json:"profile"`
}
type DatabaseMigrationLockRecoverRequest struct {
	ReviewID           string `json:"review_id"`
	ConfirmApplication string `json:"confirm_application"`
	ConfirmDatabase    string `json:"confirm_database"`
}
