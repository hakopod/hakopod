package database

import "time"

const ApplicationProvisioningSchemaVersion = 1

type ApplicationProvisioningPlan struct {
	SchemaVersion       int       `json:"schema_version"`
	ID                  string    `json:"id"`
	DatabaseID          string    `json:"database_id"`
	DatabaseRevision    int64     `json:"database_revision"`
	ApplicationID       string    `json:"application_id"`
	ApplicationName     string    `json:"application_name"`
	ApplicationRevision int64     `json:"application_revision"`
	Service             string    `json:"service"`
	Variable            string    `json:"variable"`
	Endpoint            string    `json:"endpoint"`
	Role                string    `json:"role"`
	LogicalDatabase     string    `json:"logical_database"`
	SecretReference     string    `json:"secret_reference"`
	Privileges          []string  `json:"privileges"`
	Warnings            []string  `json:"warnings"`
	ExpiresAt           time.Time `json:"expires_at"`
}

type ApplicationProvisioningOperation struct {
	ID                string                      `json:"id"`
	DatabaseID        string                      `json:"database_id"`
	DatabaseRevision  int64                       `json:"database_revision"`
	Kind              string                      `json:"kind"`
	Status            string                      `json:"status"`
	Phase             string                      `json:"phase"`
	Message           string                      `json:"message"`
	Plan              ApplicationProvisioningPlan `json:"plan"`
	CreatedAt         time.Time                   `json:"created_at"`
	StartedAt         *time.Time                  `json:"started_at,omitempty"`
	FinishedAt        *time.Time                  `json:"finished_at,omitempty"`
	IdentityID        string                      `json:"-"`
	KeyID             string                      `json:"-"`
	Lease             string                      `json:"-"`
	EncryptedPassword []byte                      `json:"-"`
}
