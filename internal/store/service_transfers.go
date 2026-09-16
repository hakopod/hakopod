package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type ServiceTransfer struct {
	ID                  string `json:"id"`
	SourceID            string `json:"source_id"`
	DestinationID       string `json:"destination_id"`
	SourceRevision      int64  `json:"source_revision"`
	DestinationRevision int64  `json:"destination_revision"`
	Service             string `json:"service"`
	DestinationService  string `json:"destination_service"`
	RemovalID           string `json:"removal_id"`
	Status              string `json:"status"`
}

func (s *Store) CheckServiceTransfer(ctx context.Context, p Principal, project, environment string, t ServiceTransfer) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,31))", "virtual-network:"+project+":"+environment); err != nil {
		return err
	}
	return t.check(ctx, tx, p, project, environment)
}

func (s *Store) ServiceTransfers(ctx context.Context, source string) ([]ServiceTransfer, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.id,t.source_id,t.destination_id,t.source_revision,t.destination_revision,t.service,t.destination_service,COALESCE(t.removal_id,''),CASE WHEN t.removal_id IS NULL THEN d.status ELSE r.status END FROM service_transfers t JOIN deployments d ON d.id=t.id LEFT JOIN deployments r ON r.id=t.removal_id WHERE source_id=$1 ORDER BY t.created_at DESC LIMIT 20`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ServiceTransfer{}
	for rows.Next() {
		var t ServiceTransfer
		if err = rows.Scan(&t.ID, &t.SourceID, &t.DestinationID, &t.SourceRevision, &t.DestinationRevision, &t.Service, &t.DestinationService, &t.RemovalID, &t.Status); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

func (s *Store) ServiceTransfer(ctx context.Context, id string) (ServiceTransfer, error) {
	var t ServiceTransfer
	err := s.Pool.QueryRow(ctx, `SELECT id,source_id,destination_id,source_revision,destination_revision,service,destination_service,COALESCE(removal_id,'') FROM service_transfers WHERE id=$1`, id).Scan(&t.ID, &t.SourceID, &t.DestinationID, &t.SourceRevision, &t.DestinationRevision, &t.Service, &t.DestinationService, &t.RemovalID)
	return t, err
}

// AcceptServiceTransfer uses the ordinary immutable deployment transaction plus
// revision checks on both applications. A finish request can only remove the
// original after the exact destination deployment succeeded.
func (s *Store) AcceptServiceTransfer(ctx context.Context, p Principal, source, destination Application, next spec.Application, t ServiceTransfer, idem string) (Deployment, error) {
	app, expected := destination, t.DestinationRevision
	if t.ID != "" {
		app, expected = source, t.SourceRevision
	}
	return s.acceptGuarded(ctx, p, app.Project, app.Environment, next, expected, idem, nil, nil, nil, &t)
}

func (t *ServiceTransfer) check(ctx context.Context, tx pgx.Tx, p Principal, project, environment string) error {
	if t.SourceID == t.DestinationID {
		return fmt.Errorf("choose a different application")
	}
	// Accept already holds the scope's network transaction lock. All deployment
	// paths acquire it first, so cross-application checks cannot invert locks.
	// Take application advisory locks before row locks, matching deletion.
	locks := []string{}
	for _, id := range []string{t.SourceID, t.DestinationID} {
		a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", id))
		if err != nil {
			return err
		}
		if a.Project != project || a.Environment != environment || !p.Allows("deployments:write", project, environment, a.Name) {
			return ErrForbidden
		}
		locks = append(locks, project+":"+environment+":"+a.Name)
	}
	sort.Strings(locks)
	for _, key := range locks {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", key); err != nil {
			return err
		}
	}
	for _, id := range []string{t.SourceID, t.DestinationID} {
		a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if a.Project != project || a.Environment != environment || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
			return ErrForbidden
		}
		expected := t.SourceRevision
		if id == t.DestinationID {
			expected = t.DestinationRevision
			if t.ID != "" {
				expected++
			}
		}
		if a.Revision != expected || (a.Status != "healthy" && a.Status != "empty") {
			return fmt.Errorf("%w: both applications must still be at the reviewed, successful revisions", ErrConflict)
		}
		var automatic bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM application_sources WHERE application_id=$1 AND auto_deploy) OR EXISTS(SELECT 1 FROM build_configs WHERE (application_id=$1 OR (project=$2 AND environment=$3 AND name=$4)) AND config->>'auto_deploy'='true')`, id, project, environment, a.Name).Scan(&automatic); err != nil {
			return err
		}
		if automatic {
			return fmt.Errorf("pause automatic deployments on both applications and update their source configuration before moving a service")
		}
		var preview bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM previews WHERE application_id=$1)`, id).Scan(&preview); err != nil {
			return err
		}
		if preview {
			return fmt.Errorf("preview applications expire; move services between permanent applications")
		}
	}
	if t.ID != "" {
		var status string
		var cancelled bool
		if err := tx.QueryRow(ctx, "SELECT status,cancel_requested FROM deployments WHERE id=$1 AND application_id=$2 AND revision=$3", t.ID, t.DestinationID, t.DestinationRevision+1).Scan(&status, &cancelled); err != nil {
			return err
		}
		if status != "succeeded" || cancelled {
			return fmt.Errorf("%w: the destination deployment has not succeeded", ErrConflict)
		}
		var removed bool
		if err := tx.QueryRow(ctx, "SELECT removal_id IS NOT NULL FROM service_transfers WHERE id=$1 FOR UPDATE", t.ID).Scan(&removed); err != nil {
			return err
		}
		if removed {
			return fmt.Errorf("%w: removal was already submitted; open its deployment", ErrConflict)
		}
	}
	return nil
}

func (t *ServiceTransfer) record(ctx context.Context, tx pgx.Tx, p Principal, deployment string) error {
	if t.ID == "" {
		_, err := tx.Exec(ctx, `INSERT INTO service_transfers(id,source_id,destination_id,source_revision,destination_revision,service,destination_service) VALUES($1,$2,$3,$4,$5,$6,$7)`, deployment, t.SourceID, t.DestinationID, t.SourceRevision, t.DestinationRevision, t.Service, t.DestinationService)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE applications SET service_display_names=service_display_names || COALESCE((SELECT jsonb_build_object($3::text,service_display_names->$4) FROM applications WHERE id=$2 AND service_display_names ? $4),'{}'::jsonb),metadata_revision=metadata_revision+1 WHERE id=$1`, t.DestinationID, t.SourceID, t.DestinationService, t.Service)
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE service_transfers SET removal_id=$2 WHERE id=$1", t.ID, deployment)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE applications SET service_display_names=service_display_names-$2,metadata_revision=metadata_revision+1 WHERE id=$1", t.SourceID, t.Service)
	return err
}
