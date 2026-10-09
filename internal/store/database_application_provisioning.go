package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

var provisionedPostgresIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{2,47}$`)
var provisionedSecretReference = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type databaseApplicationProvisioningReview struct {
	Plan              database.ApplicationProvisioningPlan `json:"plan"`
	EncryptedPassword []byte                               `json:"encrypted_password"`
}

const databaseApplicationProvisioningColumns = `id,database_id,database_revision,kind,status,phase,message,plan,created_at,started_at,finished_at,identity_id,key_id,lease,encrypted_password`

func scanDatabaseApplicationProvisioning(row scanner) (database.ApplicationProvisioningOperation, error) {
	var op database.ApplicationProvisioningOperation
	err := row.Scan(&op.ID, &op.DatabaseID, &op.DatabaseRevision, &op.Kind, &op.Status, &op.Phase, &op.Message, &op.Plan, &op.CreatedAt, &op.StartedAt, &op.FinishedAt, &op.IdentityID, &op.KeyID, &op.Lease, &op.EncryptedPassword)
	return op, err
}

func (s *Store) DatabaseApplicationProvisioningOperation(ctx context.Context, p Principal, id string) (database.ApplicationProvisioningOperation, error) {
	op, err := scanDatabaseApplicationProvisioning(s.Pool.QueryRow(ctx, `SELECT `+databaseApplicationProvisioningColumns+` FROM database_application_provisioning_operations WHERE id=$1`, id))
	if err != nil {
		return op, err
	}
	d, err := s.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return op, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, false) || !p.Allows("deployments:read", d.Project, d.Environment, op.Plan.ApplicationName) {
		return database.ApplicationProvisioningOperation{}, ErrForbidden
	}
	op.EncryptedPassword = nil
	return op, nil
}

func (s *Store) PlanDatabaseApplicationProvisioning(ctx context.Context, p Principal, databaseID, applicationID, service, variable, endpoint, role, logicalDatabase, secret string, encryptedPassword []byte) (database.ApplicationProvisioningPlan, error) {
	d, err := s.Database(ctx, p, databaseID, true)
	if err != nil {
		return database.ApplicationProvisioningPlan{}, err
	}
	if d.Spec.Engine != "postgresql" || !d.Spec.TLSRequired() {
		return database.ApplicationProvisioningPlan{}, fmt.Errorf("%w: application database provisioning currently supports PostgreSQL only", ErrInput)
	}
	a, err := s.Application(ctx, applicationID)
	if err != nil {
		return database.ApplicationProvisioningPlan{}, err
	}
	if a.Project != d.Project || a.Environment != d.Environment || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return database.ApplicationProvisioningPlan{}, ErrForbidden
	}
	if d.Status != "ready" || d.Recovery != nil && d.Recovery.InspectedAt == nil {
		return database.ApplicationProvisioningPlan{}, ErrConflict
	}
	if _, ok := a.Spec.Services[service]; !ok || variable == "" || len(variable) > 128 {
		return database.ApplicationProvisioningPlan{}, ErrInput
	}
	if endpoint == "" {
		endpoint = "read_write"
	}
	if role == "" {
		role = "hp_" + strings.ReplaceAll(a.Name, "-", "_")
	}
	if logicalDatabase == "" {
		logicalDatabase = role
	}
	if secret == "" {
		secret = "database-" + strings.ReplaceAll(d.Spec.Name, "_", "-") + "-password"
	}
	if !provisionedPostgresIdentifier.MatchString(role) || !provisionedPostgresIdentifier.MatchString(logicalDatabase) || !provisionedSecretReference.MatchString(secret) || len(encryptedPassword) < 29 || len(encryptedPassword) > 1024 {
		return database.ApplicationProvisioningPlan{}, ErrInput
	}
	if err = s.validateDatabaseConnection(ctx, p, d.ID, a.ID, service, variable, endpoint, DatabaseConnectionOptions{Username: role, Database: logicalDatabase, Password: &spec.SecretRef{Ref: secret}, SSLMode: "verify-full"}); err != nil {
		return database.ApplicationProvisioningPlan{}, err
	}
	plan := database.ApplicationProvisioningPlan{SchemaVersion: database.ApplicationProvisioningSchemaVersion, ID: NewID(), DatabaseID: d.ID, DatabaseRevision: d.Revision, ApplicationID: a.ID, ApplicationName: a.Name, ApplicationRevision: a.Revision, Service: service, Variable: variable, Endpoint: endpoint, Role: role, LogicalDatabase: logicalDatabase, SecretReference: secret, Privileges: []string{"CONNECT", "TEMPORARY", "CREATE and USAGE on public schema", "all privileges on owned objects"}, Warnings: []string{"Creates one PostgreSQL login and logical database owned by that login.", "The generated password is stored only as encrypted operation material and an application-scoped secret.", "The application deployment is queued only after native privilege verification succeeds."}, ExpiresAt: time.Now().UTC().Add(database.ReviewLifetime)}
	review := databaseApplicationProvisioningReview{Plan: plan, EncryptedPassword: append([]byte(nil), encryptedPassword...)}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,'application-provisioning',$5,$6)`, plan.ID, d.ID, p.ID, d.Revision, JSON(review), plan.ExpiresAt); err != nil {
		return database.ApplicationProvisioningPlan{}, err
	}
	return plan, nil
}

func (s *Store) AcceptDatabaseApplicationProvisioning(ctx context.Context, p Principal, databaseID, reviewID, confirmation, idem string) (database.ApplicationProvisioningOperation, error) {
	var empty database.ApplicationProvisioningOperation
	if len(idem) < 8 || len(idem) > 128 {
		return empty, ErrInput
	}
	var preview databaseApplicationProvisioningReview
	if err := s.Pool.QueryRow(ctx, `SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND kind='application-provisioning'`, reviewID, databaseID, p.ID).Scan(&preview); err != nil {
		return empty, err
	}
	if confirmation != preview.Plan.ApplicationName {
		return empty, fmt.Errorf("%w: confirm the application name", ErrInput)
	}
	hash := sha256.Sum256(JSON(struct{ DatabaseID, ReviewID string }{databaseID, reviewID}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,92))`, p.ID+":"+idem); err != nil {
		return empty, err
	}
	var oldID string
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM database_application_provisioning_operations WHERE identity_id=$1 AND idempotency_key=$2`, p.ID, idem).Scan(&oldID, &oldHash)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return empty, ErrConflict
		}
		op, scanErr := scanDatabaseApplicationProvisioning(tx.QueryRow(ctx, `SELECT `+databaseApplicationProvisioningColumns+` FROM database_application_provisioning_operations WHERE id=$1`, oldID))
		if scanErr != nil {
			return empty, scanErr
		}
		d, databaseErr := s.DatabaseInternal(ctx, op.DatabaseID)
		if databaseErr != nil || !p.AllowsDatabase(d.Project, d.Environment, true) || !p.Allows("deployments:write", d.Project, d.Environment, op.Plan.ApplicationName) {
			return empty, ErrForbidden
		}
		return op, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	d, err := scanDatabase(tx.QueryRow(ctx, `SELECT `+databaseCols+` FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, databaseID))
	if err != nil {
		return empty, err
	}
	var review databaseApplicationProvisioningReview
	err = tx.QueryRow(ctx, `SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND revision=$4 AND kind='application-provisioning' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, reviewID, databaseID, p.ID, d.Revision).Scan(&review)
	if err != nil {
		return empty, ErrConflict
	}
	var appRevision int64
	var appName string
	if err = tx.QueryRow(ctx, `SELECT revision,name FROM applications WHERE id=$1 FOR UPDATE`, review.Plan.ApplicationID).Scan(&appRevision, &appName); err != nil {
		return empty, err
	}
	if d.Revision != review.Plan.DatabaseRevision || d.Status != "ready" || appRevision != review.Plan.ApplicationRevision || appName != review.Plan.ApplicationName || !p.AllowsDatabase(d.Project, d.Environment, true) || !p.Allows("deployments:write", d.Project, d.Environment, appName) {
		return empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1`, reviewID); err != nil {
		return empty, err
	}
	op, err := scanDatabaseApplicationProvisioning(tx.QueryRow(ctx, `INSERT INTO database_application_provisioning_operations(id,database_id,database_revision,application_id,application_revision,identity_id,key_id,idempotency_key,request_hash,plan,encrypted_password) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+databaseApplicationProvisioningColumns, NewID(), d.ID, d.Revision, review.Plan.ApplicationID, review.Plan.ApplicationRevision, p.ID, p.KeyID, idem, hash[:], JSON(review.Plan), review.EncryptedPassword))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.application.provision.accept',$3,$4)`, p.ID, p.KeyID, d.ID, JSON(map[string]any{"operation_id": op.ID, "application_id": review.Plan.ApplicationID, "review_id": reviewID})); err != nil {
		return empty, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) ClaimDatabaseApplicationProvisioning(ctx context.Context) (database.ApplicationProvisioningOperation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.ApplicationProvisioningOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(793044292)`); err != nil {
		return database.ApplicationProvisioningOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE database_application_provisioning_operations SET status='queued',lease='',lease_until=NULL WHERE status='running' AND lease_until<now()`); err != nil {
		return database.ApplicationProvisioningOperation{}, err
	}
	op, err := scanDatabaseApplicationProvisioning(tx.QueryRow(ctx, `UPDATE database_application_provisioning_operations SET status='running',lease=$1,lease_until=now()+interval '45 seconds',started_at=COALESCE(started_at,now()) WHERE id=(SELECT id FROM database_application_provisioning_operations WHERE status='queued' AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+databaseApplicationProvisioningColumns, NewID()))
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) CheckDatabaseApplicationProvisioning(ctx context.Context, op database.ApplicationProvisioningOperation) error {
	var ok bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM database_application_provisioning_operations o JOIN managed_databases d ON d.id=o.database_id JOIN applications a ON a.id=o.application_id WHERE o.id=$1 AND o.lease=$2 AND o.lease_until>now() AND o.status='running' AND d.revision=o.database_revision AND a.revision=o.application_revision)`, op.ID, op.Lease).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	d, err := s.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return err
	}
	return s.Reauthorize(ctx, op.KeyID, d.Project, d.Environment, op.Plan.ApplicationName)
}

func (s *Store) RecordDatabaseApplicationProvisioning(ctx context.Context, op database.ApplicationProvisioningOperation, status, phase, message string) error {
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" || len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE database_application_provisioning_operations SET status=$3,phase=$4,message=$5,next_attempt_at=now()+interval '3 seconds',lease='',lease_until=NULL,finished_at=CASE WHEN $3='queued' THEN NULL ELSE now() END WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>now()`, op.ID, op.Lease, status, phase, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
