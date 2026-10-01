package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func externalDatabaseIDs(apps ...spec.Application) []string {
	unique := map[string]bool{}
	for _, app := range apps {
		for _, svc := range app.Services {
			for _, b := range svc.Bindings {
				if b.ExternalDatabase != "" {
					unique[b.ExternalDatabase] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validateExternalDatabaseBinding(r externaldatabase.Resource, b spec.Binding) error {
	if err := externalDatabaseFresh(r); err != nil {
		return err
	}
	if b.ExternalDatabase != r.ID || b.ExternalDatabaseRevision != r.Revision || r.Spec.Engine == "mysql" && b.Protocol != "mysql" || r.Spec.Engine == "postgresql" && b.Protocol != "postgres" {
		return fmt.Errorf("%w: review the current external database revision before deploying this binding", ErrConflict)
	}
	return nil
}

func validateExternalDatabaseBindingsTx(ctx context.Context, tx pgx.Tx, p Principal, a Application, apps ...spec.Application) error {
	reviewID, _ := ctx.Value(externalDatabaseReviewContext{}).(string)
	loaded := map[string]externaldatabase.Resource{}
	for _, id := range externalDatabaseIDs(apps...) {
		r, err := scanExternalDatabase(tx.QueryRow(ctx, "SELECT "+externalDatabaseCols+" FROM external_databases WHERE id=$1 AND project=$2 AND environment=$3 FOR UPDATE", id, a.Project, a.Environment))
		if err != nil {
			return fmt.Errorf("%w: external database is unavailable in this scope", ErrConflict)
		}
		loaded[id] = r
	}
	for _, app := range apps {
		for name, svc := range app.Services {
			for variable, b := range svc.Bindings {
				if b.ExternalDatabase == "" {
					continue
				}
				if !reflect.DeepEqual(a.Spec.Services[name].Bindings[variable], b) {
					if reviewID == "" {
						return fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
					}
					continue
				}
				if err := validateExternalDatabaseBinding(loaded[b.ExternalDatabase], b); err != nil {
					return err
				}
			}
		}
	}
	if len(apps) > 0 {
		return consumeExternalDatabaseConnectionReview(ctx, tx, p, a, apps[0])
	}
	return nil
}

func (s *Store) ResolveExternalDatabaseBindings(ctx context.Context, project, environment string, app spec.Application) (map[string]externaldatabase.Resource, error) {
	loaded := map[string]externaldatabase.Resource{}
	for _, id := range externalDatabaseIDs(app) {
		r, err := s.ExternalDatabaseInternal(ctx, id)
		if err != nil || r.Project != project || r.Environment != environment {
			return nil, fmt.Errorf("external database is unavailable in this scope")
		}
		loaded[id] = r
	}
	for _, svc := range app.Services {
		for _, b := range svc.Bindings {
			if b.ExternalDatabase != "" {
				if err := validateExternalDatabaseBinding(loaded[b.ExternalDatabase], b); err != nil {
					return nil, err
				}
			}
		}
	}
	return loaded, nil
}

type ExternalDatabaseConnectionPlan struct {
	ID                  string        `json:"id"`
	Kind                string        `json:"kind"`
	DatabaseID          string        `json:"database_id"`
	DatabaseName        string        `json:"database_name"`
	DatabaseRevision    int64         `json:"database_revision"`
	CredentialRevision  int64         `json:"credential_revision"`
	ApplicationID       string        `json:"application_id"`
	ApplicationName     string        `json:"application_name"`
	ApplicationRevision int64         `json:"application_revision"`
	Service             string        `json:"service"`
	Variable            string        `json:"variable"`
	Binding             *spec.Binding `json:"binding,omitempty"`
	ExpiresAt           time.Time     `json:"expires_at"`
	Warnings            []string      `json:"warnings"`
}
type externalDatabaseReview struct {
	Plan ExternalDatabaseConnectionPlan `json:"plan"`
	Spec spec.Application               `json:"spec"`
}
type externalDatabaseReviewContext struct{}

func (s *Store) PlanExternalDatabaseConnection(ctx context.Context, p Principal, id, appID, service, variable string, disconnect bool) (ExternalDatabaseConnectionPlan, error) {
	r, err := s.ExternalDatabase(ctx, p, id, true)
	if err != nil {
		return ExternalDatabaseConnectionPlan{}, err
	}
	a, err := s.Application(ctx, appID)
	if err != nil {
		return ExternalDatabaseConnectionPlan{}, err
	}
	if a.Project != r.Project || a.Environment != r.Environment || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return ExternalDatabaseConnectionPlan{}, ErrForbidden
	}
	var next spec.Application
	if err = json.Unmarshal(JSON(a.Spec), &next); err != nil {
		return ExternalDatabaseConnectionPlan{}, err
	}
	svc, ok := next.Services[service]
	if !ok {
		return ExternalDatabaseConnectionPlan{}, ErrInput
	}
	current := svc.Bindings[variable]
	plan := ExternalDatabaseConnectionPlan{ID: NewID(), Kind: "refresh", DatabaseID: id, DatabaseName: r.Spec.Name, DatabaseRevision: r.Revision, CredentialRevision: r.CredentialRevision, ApplicationID: a.ID, ApplicationName: a.Name, ApplicationRevision: a.Revision, Service: service, Variable: variable, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if current.ExternalDatabase != id {
		if !disconnect {
			return plan, fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
		}
		return plan, fmt.Errorf("%w: this variable is not connected to the selected external database", ErrInput)
	}
	if disconnect {
		delete(svc.Bindings, variable)
		plan.Kind = "disconnect"
		plan.Warnings = []string{"The deployment removes this service's saved connection. Existing pods keep their previous credentials until replaced.", "Removing the binding does not revoke credentials at the provider. Revoke them only after every deployment has released them."}
	} else {
		binding := current
		binding.ExternalDatabaseRevision = r.Revision
		if err = validateExternalDatabaseBinding(r, binding); err != nil {
			return plan, err
		}
		svc.Bindings[variable] = binding
		plan.Binding = &binding
		plan.Warnings = []string{"The deployment replaces this service's saved credential revision. Existing pods keep their previous credentials until replaced."}
	}
	next.Services[service] = svc
	next, err = spec.Normalize(next)
	if err != nil {
		return plan, fmt.Errorf("%w: %v", ErrInput, err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,57))", p.ID); err != nil {
		return plan, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM external_database_reviews WHERE identity_id=$1 AND expires_at<now()", p.ID); err != nil {
		return plan, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM external_database_reviews WHERE identity_id=$1", p.ID).Scan(&count); err != nil {
		return plan, err
	}
	if count >= 64 {
		return plan, fmt.Errorf("%w: too many active connection reviews", ErrConflict)
	}
	_, err = tx.Exec(ctx, "INSERT INTO external_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", plan.ID, id, p.ID, r.Revision, plan.Kind, JSON(externalDatabaseReview{plan, next}), plan.ExpiresAt)
	if err != nil {
		return plan, err
	}
	return plan, tx.Commit(ctx)
}

func (s *Store) AcceptExternalDatabaseConnection(ctx context.Context, p Principal, id, reviewID, confirmation, idem string) (Deployment, error) {
	var review externalDatabaseReview
	err := s.Pool.QueryRow(ctx, "SELECT payload FROM external_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3", reviewID, id, p.ID).Scan(&review)
	if err != nil {
		return Deployment{}, err
	}
	if review.Plan.Kind != "disconnect" && review.Plan.Kind != "refresh" {
		return Deployment{}, fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
	}
	if confirmation != review.Plan.ApplicationName {
		return Deployment{}, fmt.Errorf("%w: confirm the application name", ErrInput)
	}
	r, err := s.ExternalDatabase(ctx, p, id, true)
	if err != nil {
		return Deployment{}, err
	}
	ctx = context.WithValue(ctx, externalDatabaseReviewContext{}, reviewID)
	return s.Accept(ctx, p, r.Project, r.Environment, review.Spec, review.Plan.ApplicationRevision, idem)
}

func consumeExternalDatabaseConnectionReview(ctx context.Context, tx pgx.Tx, p Principal, a Application, next spec.Application) error {
	id, _ := ctx.Value(externalDatabaseReviewContext{}).(string)
	if id == "" {
		return nil
	}
	var review externalDatabaseReview
	err := tx.QueryRow(ctx, "SELECT payload FROM external_database_reviews WHERE id=$1 AND identity_id=$2 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", id, p.ID).Scan(&review)
	if err != nil {
		return fmt.Errorf("%w: connection review is expired or already used", ErrConflict)
	}
	plan := review.Plan
	if plan.Kind != "disconnect" && plan.Kind != "refresh" {
		return fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
	}
	if a.ID != plan.ApplicationID || a.Revision != plan.ApplicationRevision || !reflect.DeepEqual(next, review.Spec) || !p.AllowsDatabase(a.Project, a.Environment, true) {
		return ErrConflict
	}
	r, err := scanExternalDatabase(tx.QueryRow(ctx, "SELECT "+externalDatabaseCols+" FROM external_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", plan.DatabaseID))
	if err != nil {
		return err
	}
	if r.Project != a.Project || r.Environment != a.Environment || r.Revision != plan.DatabaseRevision || r.CredentialRevision != plan.CredentialRevision {
		return fmt.Errorf("%w: external database changed; review this connection again", ErrConflict)
	}
	var expected spec.Application
	if err = json.Unmarshal(JSON(a.Spec), &expected); err != nil {
		return err
	}
	svc, ok := expected.Services[plan.Service]
	if !ok || svc.Bindings[plan.Variable].ExternalDatabase != plan.DatabaseID {
		return fmt.Errorf("%w: the reviewed legacy binding is no longer saved", ErrConflict)
	}
	if plan.Kind == "refresh" {
		if plan.Binding == nil {
			return ErrConflict
		}
		binding := svc.Bindings[plan.Variable]
		binding.ExternalDatabaseRevision = r.Revision
		if !reflect.DeepEqual(binding, *plan.Binding) {
			return ErrConflict
		}
		svc.Bindings[plan.Variable] = binding
		if err = validateExternalDatabaseBinding(r, binding); err != nil {
			return err
		}
	} else {
		delete(svc.Bindings, plan.Variable)
	}
	expected.Services[plan.Service] = svc
	expected, err = spec.Normalize(expected)
	if err != nil || !reflect.DeepEqual(next, expected) {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE external_database_reviews SET consumed_at=now() WHERE id=$1", id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, "external_database."+plan.Kind, plan.DatabaseID, JSON(map[string]any{"application_id": a.ID, "service": plan.Service, "variable": plan.Variable, "review_id": id, "database_revision": r.Revision, "credential_revision": r.CredentialRevision}))
	return err
}

// ExternalDatabaseConnections describes configuration evidence, not live sockets.
func (s *Store) ExternalDatabaseConnections(ctx context.Context, p Principal, id string) (DatabaseConnections, error) {
	out := DatabaseConnections{Items: []DatabaseConnectionReference{}, Limit: MaxDatabaseConnections}
	r, err := s.ExternalDatabase(ctx, p, id, false)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.name,a.display_name,svc.key,b.key,
 COALESCE(max(v.revision) FILTER(WHERE v.kind='saved'),0),COALESCE(max(v.revision) FILTER(WHERE v.kind='last_successful'),0),COALESCE(max(v.revision) FILTER(WHERE v.kind='latest_attempt'),0),COALESCE(max(v.status) FILTER(WHERE v.kind='latest_attempt'),'')
 FROM applications a CROSS JOIN LATERAL (
 SELECT 'saved' AS kind,a.revision,'' AS status,a.spec AS definition
 UNION ALL SELECT history.kind,history.revision,history.status,body.definition FROM (
 (SELECT 'last_successful' AS kind,revision,status,spec,resolved_spec,recovery_spec FROM deployments WHERE application_id=a.id AND status='succeeded' ORDER BY revision DESC LIMIT 1)
 UNION ALL (SELECT 'latest_attempt',revision,status,spec,resolved_spec,recovery_spec FROM deployments WHERE application_id=a.id ORDER BY revision DESC LIMIT 1)
 ) history CROSS JOIN LATERAL (VALUES(history.spec),(history.resolved_spec),(history.recovery_spec)) body(definition)
 WHERE history.kind='last_successful' OR history.status<>'succeeded')v
 CROSS JOIN LATERAL jsonb_each(v.definition->'services')svc CROSS JOIN LATERAL jsonb_each(svc.value->'bindings')b
 WHERE a.project=$1 AND a.environment=$2 AND b.value->>'external_database'=$3
 GROUP BY a.id,a.name,a.display_name,svc.key,b.key ORDER BY COALESCE(max(v.revision) FILTER(WHERE v.kind='saved'),0)>0 DESC,a.name,a.id,svc.key,b.key LIMIT $4`, r.Project, r.Environment, id, MaxDatabaseConnections+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item DatabaseConnectionReference
		if err = rows.Scan(&item.ApplicationID, &item.ApplicationName, &item.ApplicationDisplayName, &item.Service, &item.Variable, &item.SavedRevision, &item.LastSuccessfulRevision, &item.LatestAttemptRevision, &item.LatestAttemptStatus); err != nil {
			return out, err
		}
		if len(out.Items) == MaxDatabaseConnections {
			out.Truncated = true
			break
		}
		item.Project, item.Environment, item.Endpoint = r.Project, r.Environment, "provider"
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
