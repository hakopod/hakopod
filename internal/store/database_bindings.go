package store

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func managedDatabaseIDs(app spec.Application) []string {
	unique := map[string]bool{}
	for _, svc := range app.Services {
		for _, b := range svc.Bindings {
			if b.ManagedDatabase != "" {
				unique[b.ManagedDatabase] = true
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

func validateDatabaseBinding(d database.Resource, b spec.Binding) error {
	if d.DeletedAt != nil || d.Status != "ready" || d.Observation.Status != "ready" {
		return fmt.Errorf("%w: managed database is not ready", ErrConflict)
	}
	if d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		return fmt.Errorf("%w: inspect the completed recovery before connecting an application", ErrConflict)
	}
	if d.Spec.Engine == "postgresql" {
		if b.Protocol != "postgres" || b.Endpoint != "read_write" && (b.Endpoint != "read_only" || d.Spec.Replicas == 0) || b.ClusterAware {
			return ErrInput
		}
	} else if b.Protocol != "redis" || d.Spec.Mode == "cluster" && (b.Endpoint != "cluster" || !b.ClusterAware) || d.Spec.Mode == "standalone" && (b.Endpoint != "read_write" || b.ClusterAware) {
		return ErrInput
	}
	return nil
}

// Lock the same database rows as deletion before accepting a new saved grant.
// Application keys may reuse a grant, but cannot add or change one.
func validateDatabaseBindingsTx(ctx context.Context, tx pgx.Tx, p Principal, app Application, specs ...spec.Application) error {
	loaded := map[string]database.Resource{}
	unique := map[string]bool{}
	for _, next := range specs {
		for _, id := range managedDatabaseIDs(next) {
			unique[id] = true
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND project=$2 AND environment=$3 FOR UPDATE", id, app.Project, app.Environment))
		if err != nil {
			return fmt.Errorf("%w: managed database is unavailable in this scope", ErrConflict)
		}
		loaded[id] = d
	}
	for _, next := range specs {
		for name, svc := range next.Services {
			for variable, b := range svc.Bindings {
				if b.ManagedDatabase == "" {
					continue
				}
				if !reflect.DeepEqual(app.Spec.Services[name].Bindings[variable], b) && !p.AllowsDatabase(app.Project, app.Environment, true) {
					return ErrForbidden
				}
				if err := validateDatabaseBinding(loaded[b.ManagedDatabase], b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ResolveDatabaseBindings is for the trusted deployment worker. Public readers
// use Database; the worker already owns an accepted immutable application spec.
func (s *Store) ResolveDatabaseBindings(ctx context.Context, project, environment string, app spec.Application) (map[string]database.Resource, error) {
	loaded := map[string]database.Resource{}
	for _, id := range managedDatabaseIDs(app) {
		d, err := s.DatabaseInternal(ctx, id)
		if err != nil || d.Project != project || d.Environment != environment {
			return nil, fmt.Errorf("managed database is unavailable in this scope")
		}
		loaded[id] = d
	}
	for _, svc := range app.Services {
		for _, b := range svc.Bindings {
			if b.ManagedDatabase != "" {
				if err := validateDatabaseBinding(loaded[b.ManagedDatabase], b); err != nil {
					return nil, err
				}
			}
		}
	}
	return loaded, nil
}

func databaseUnreferenced(ctx context.Context, tx pgx.Tx, id string) error {
	var used bool
	// A failed deployment can leave a previous connection live. Include the latest
	// success and attempted revision until a replacement deployment succeeds.
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM applications a, jsonb_each(a.spec->'services') s, jsonb_each(s.value->'bindings') b WHERE b.value->>'managed_database'=$1
 UNION ALL SELECT 1 FROM deployments d,
 jsonb_array_elements(jsonb_build_array(d.spec,d.resolved_spec,d.recovery_spec)) versions(value),
 jsonb_each(versions.value->'services') s, jsonb_each(s.value->'bindings') b
 WHERE (d.status IN ('queued','running')
 OR d.id=(SELECT id FROM deployments WHERE application_id=d.application_id ORDER BY revision DESC LIMIT 1)
 OR d.id=(SELECT id FROM deployments WHERE application_id=d.application_id AND status='succeeded' ORDER BY revision DESC LIMIT 1))
 AND b.value->>'managed_database'=$1)`, id).Scan(&used)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: this database still has saved or deployed application connections", ErrConflict)
	}
	return nil
}
