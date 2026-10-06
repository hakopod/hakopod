package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type DatabaseConnectionPlan struct {
	ID                  string             `json:"id"`
	DatabaseID          string             `json:"database_id"`
	DatabaseName        string             `json:"database_name"`
	DatabaseRevision    int64              `json:"database_revision"`
	ApplicationID       string             `json:"application_id"`
	ApplicationName     string             `json:"application_name"`
	ApplicationRevision int64              `json:"application_revision"`
	Service             string             `json:"service"`
	Variable            string             `json:"variable"`
	PreviousKind        string             `json:"previous_kind"`
	Binding             spec.Binding       `json:"binding"`
	Recovery            *database.Recovery `json:"recovery,omitempty"`
	ExpiresAt           time.Time          `json:"expires_at"`
	Warnings            []string           `json:"warnings"`
}

// DatabaseConnectionOptions select an existing login and database. Passwords
// remain application-scoped references in both reviews and saved revisions.
type DatabaseConnectionOptions struct {
	Username string          `json:"username,omitempty"`
	Database string          `json:"database,omitempty"`
	Password *spec.SecretRef `json:"password,omitempty"`
	SSLMode  string          `json:"ssl_mode,omitempty"`
}

type databaseConnectionReview struct {
	Plan DatabaseConnectionPlan `json:"plan"`
	Spec spec.Application       `json:"spec"`
}
type databaseConnectionContext struct{}

func databaseConnectionReviewID(ctx context.Context) string {
	id, _ := ctx.Value(databaseConnectionContext{}).(string)
	return id
}

func (s *Store) PlanDatabaseConnection(ctx context.Context, p Principal, id, appID, service, variable, endpoint string, clusterAware bool, options ...DatabaseConnectionOptions) (DatabaseConnectionPlan, error) {
	d, err := s.Database(ctx, p, id, true)
	if err != nil {
		return DatabaseConnectionPlan{}, err
	}
	a, err := s.Application(ctx, appID)
	if err != nil {
		return DatabaseConnectionPlan{}, err
	}
	if a.Project != d.Project || a.Environment != d.Environment || !p.AllowsDatabase(d.Project, d.Environment, true) || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return DatabaseConnectionPlan{}, ErrForbidden
	}
	b := spec.Binding{ManagedDatabase: id, Endpoint: endpoint, ClusterAware: clusterAware, Protocol: "postgres"}
	if d.Spec.Engine == "redis" {
		b.Protocol = "redis"
	}
	if d.Spec.Engine == "mysql" || d.Spec.Engine == "vitess" {
		b.Protocol = "mysql"
	}
	if d.Spec.Engine == "mongodb" {
		b.Protocol = "mongodb"
	}
	if d.Spec.Engine == "clickhouse" {
		b.Protocol = "clickhouse"
	}
	if d.Spec.Engine == "oracle" {
		b.Protocol = "oracle"
	}
	if len(options) > 1 {
		return DatabaseConnectionPlan{}, ErrInput
	}
	if len(options) == 1 {
		b.Username, b.Database, b.Password, b.SSLMode = options[0].Username, options[0].Database, options[0].Password, options[0].SSLMode
	}
	if err = validateDatabaseBinding(d, b); err != nil {
		return DatabaseConnectionPlan{}, err
	}
	// Deep copy before changing maps in the saved revision.
	var next spec.Application
	if err = json.Unmarshal(JSON(a.Spec), &next); err != nil {
		return DatabaseConnectionPlan{}, err
	}
	svc, ok := next.Services[service]
	if !ok {
		return DatabaseConnectionPlan{}, ErrInput
	}
	previous := "unset"
	if _, ok := svc.Env[variable]; ok {
		previous = "environment value"
	}
	if _, ok := svc.Secrets[variable]; ok {
		previous = "secret reference"
	}
	if old, ok := svc.Bindings[variable]; ok {
		previous = "service binding"
		if old.ManagedDatabase != "" {
			previous = "managed database binding"
		}
	}
	delete(svc.Env, variable)
	delete(svc.Secrets, variable)
	if svc.Bindings == nil {
		svc.Bindings = map[string]spec.Binding{}
	}
	svc.Bindings[variable] = b
	next.Services[service] = svc
	next, err = spec.Normalize(next)
	if err != nil {
		return DatabaseConnectionPlan{}, fmt.Errorf("%w: %v", ErrInput, err)
	}
	plan := DatabaseConnectionPlan{ID: NewID(), DatabaseID: id, DatabaseName: d.Spec.Name, DatabaseRevision: d.Revision, ApplicationID: a.ID, ApplicationName: a.Name, ApplicationRevision: a.Revision, Service: service, Variable: variable, PreviousKind: previous, Binding: b, Recovery: d.Recovery, ExpiresAt: time.Now().UTC().Add(database.ReviewLifetime), Warnings: []string{"This replaces the saved connection and queues a new application deployment. Existing pods use their previous connection until replaced.", "Source data remains available. Keep any Git-managed configuration in sync with the new binding."}}
	if b.Username != "" || b.Password != nil || b.Database != "" {
		plan.Warnings = append(plan.Warnings, "This binding uses an existing database and login. It does not create users, databases or grants; verify their access before deploying.")
	}
	if b.SSLMode == "require" {
		plan.Warnings = append(plan.Warnings, "SSL mode require encrypts traffic but does not verify the database certificate identity.")
	}
	if b.SSLMode == "verify-ca" {
		plan.Warnings = append(plan.Warnings, "SSL mode verify-ca verifies the certificate authority but does not verify the endpoint hostname.")
	}
	if d.Spec.Pooling != nil && (endpoint == "pooled_read_write" || endpoint == "pooled_read_only") {
		plan.Warnings = append(plan.Warnings, "PgBouncer uses "+d.Spec.Pooling.Mode+" pooling. Clients must reconnect after failover; replica reads may lag. Routing does not grant read-only database permissions.")
		if d.Spec.Pooling.Mode == "transaction" {
			plan.Warnings = append(plan.Warnings, "Transaction pooling does not preserve session settings, LISTEN subscriptions or temporary tables across transactions. Use a direct or session endpoint when required.")
		}
	}
	if d.Spec.Engine == "mysql" {
		plan.Warnings = append(plan.Warnings, "MySQL Router selects the primary or replica route explicitly. Configure your driver to load the mounted CA and verify the endpoint hostname. Reconnect after a primary election; replica reads may lag.")
	}
	if d.Spec.Engine == "vitess" {
		plan.Warnings = append(plan.Warnings, "Vitess uses the MySQL protocol with app@primary or app@replica as the database target. Configure your driver to verify the gateway hostname using the mounted CA. The gateway does not decide which queries may use replicas; replica reads may lag.")
	}
	if d.Spec.Engine == "mongodb" {
		plan.Warnings = append(plan.Warnings, "Use a MongoDB driver that discovers replica set members and verifies TLS. This binding requests majority writes and primary reads. Secondary reads require an explicit driver read preference and may lag.")
	}
	if d.Spec.Engine == "clickhouse" {
		plan.Warnings = append(plan.Warnings, "Use the ClickHouse native protocol with TLS and the mounted CA. Replication is asynchronous. Sharded queries require Distributed tables; a cluster endpoint does not automatically rewrite SQL or distribute table data.")
	}
	if d.Recovery != nil {
		plan.Warnings = append(plan.Warnings, "Writes after the captured recovery point are absent from this copy. Pause source writes and take a fresh capture before final cutover when necessary.")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "DELETE FROM managed_database_reviews WHERE expires_at<now()"); err != nil {
		return plan, err
	}
	// Bound reviews per identity with the same lane as connection planning.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,47))", p.ID); err != nil {
		return plan, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_database_reviews WHERE identity_id=$1", p.ID).Scan(&count); err != nil {
		return plan, err
	}
	if count >= 64 {
		return plan, fmt.Errorf("%w: too many pending reviews", ErrConflict)
	}
	_, err = tx.Exec(ctx, "INSERT INTO managed_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,'connection',$5,$6)", plan.ID, id, p.ID, d.Revision, JSON(databaseConnectionReview{plan, next}), plan.ExpiresAt)
	if err != nil {
		return plan, err
	}
	return plan, tx.Commit(ctx)
}

func (s *Store) AcceptDatabaseConnection(ctx context.Context, p Principal, id, reviewID, confirmation, idem string) (Deployment, error) {
	var review databaseConnectionReview
	err := s.Pool.QueryRow(ctx, "SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND kind='connection'", reviewID, id, p.ID).Scan(&review)
	if err != nil {
		return Deployment{}, err
	}
	if confirmation != review.Plan.ApplicationName {
		return Deployment{}, fmt.Errorf("%w: confirm the application name", ErrInput)
	}
	d, err := s.Database(ctx, p, id, true)
	if err != nil {
		return Deployment{}, err
	}
	ctx = context.WithValue(ctx, databaseConnectionContext{}, reviewID)
	return s.Accept(ctx, p, d.Project, d.Environment, review.Spec, review.Plan.ApplicationRevision, idem)
}

// Review consumption shares the deployment transaction and its row locks, so
// rejection cannot consume a review or leave a changed spec without a release.
func consumeDatabaseConnectionReview(ctx context.Context, tx pgx.Tx, p Principal, a Application, next spec.Application) error {
	id := databaseConnectionReviewID(ctx)
	if id == "" {
		return nil
	}
	var review databaseConnectionReview
	err := tx.QueryRow(ctx, "SELECT payload FROM managed_database_reviews WHERE id=$1 AND identity_id=$2 AND kind='connection' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", id, p.ID).Scan(&review)
	if err != nil {
		return fmt.Errorf("%w: connection review is expired or already used", ErrConflict)
	}
	plan := review.Plan
	if a.ID != plan.ApplicationID || a.Revision != plan.ApplicationRevision || !reflect.DeepEqual(next, review.Spec) || !p.AllowsDatabase(a.Project, a.Environment, true) {
		return ErrConflict
	}
	d, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 FOR UPDATE", plan.DatabaseID))
	if err != nil {
		return err
	}
	if d.Revision != plan.DatabaseRevision || !reflect.DeepEqual(d.Recovery, plan.Recovery) {
		return fmt.Errorf("%w: database changed; review the connection again", ErrConflict)
	}
	if err = validateDatabaseBinding(d, plan.Binding); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1", id)
	return err
}

func (s *Store) InspectDatabaseRecovery(ctx context.Context, p Principal, id, jobID, confirmation string, revision int64) (database.Resource, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.Resource{}, err
	}
	defer tx.Rollback(ctx)
	d, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id))
	if err != nil {
		return d, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return database.Resource{}, ErrForbidden
	}
	if d.Revision != revision || d.Spec.Name != confirmation || d.Status != "ready" || d.Recovery == nil || d.Recovery.JobID != jobID || d.Recovery.RestoredAt == nil {
		return database.Resource{}, fmt.Errorf("%w: confirm the completed recovery and current revision", ErrConflict)
	}
	if d.Recovery.InspectedAt == nil {
		now := time.Now().UTC()
		d.Recovery.InspectedAt = &now
		if _, err = tx.Exec(ctx, "UPDATE managed_databases SET recovery=$2,updated_at=now() WHERE id=$1", id, JSON(d.Recovery)); err != nil {
			return d, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.recovery.inspect',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"job_id": jobID, "revision": revision, "artifact_id": d.Recovery.ArtifactID})); err != nil {
			return d, err
		}
	}
	d.EncryptedCredentials = nil
	return d, tx.Commit(ctx)
}
