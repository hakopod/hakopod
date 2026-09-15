package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func (s *Store) LastHealthyRelease(ctx context.Context, app string, before int64) (*Deployment, error) {
	d, err := scanDep(s.Pool.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE application_id=$1 AND revision<$2 AND status='succeeded' AND resolved_spec IS NOT NULL ORDER BY revision DESC LIMIT 1", app, before))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Recovery phase and selected digest-pinned revision survive process restarts.
// Only the exact session holding the deployment lock can advance this phase.
func (c *Claim) BeginRecovery(ctx context.Context, previous *Deployment, failure, skip string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil || c.Conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	state := "running"
	var body any
	var revision int64
	if skip != "" {
		state = "skipped"
	} else {
		if previous == nil || previous.ApplicationID != c.Deployment.ApplicationID || previous.Revision >= c.Deployment.Revision || previous.ResolvedSpec == nil || previous.Status != "succeeded" {
			return ErrInput
		}
		body = JSON(*previous.ResolvedSpec)
		revision = previous.Revision
	}
	d, err := scanDep(c.Conn.QueryRow(ctx, "UPDATE deployments SET recovery_state=$2,recovery_revision=$3,recovery_spec=$4,recovery_error=$5,error=$6 WHERE id=$1 AND status='running' AND recovery_state='' RETURNING "+depCols, c.Deployment.ID, state, revision, body, skip, failure))
	if err != nil {
		return err
	}
	c.Deployment = d
	return nil
}
func (c *Claim) FinishRecovery(ctx context.Context, state, message string, result any) error {
	if state != "succeeded" && state != "failed" {
		return ErrInput
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil || c.Conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	d, err := scanDep(c.Conn.QueryRow(ctx, "UPDATE deployments SET recovery_state=$2,recovery_error=$3,result=$4 WHERE id=$1 AND status='running' AND recovery_state='running' RETURNING "+depCols, c.Deployment.ID, state, message, JSON(result)))
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: recovery phase changed", ErrClaimLost)
	}
	if err != nil {
		return err
	}
	c.Deployment = d
	return nil
}
func (s *Store) ObservedSpec(ctx context.Context, a Application) (spec.Application, error) {
	var recovered *spec.Application
	err := s.Pool.QueryRow(ctx, "SELECT recovery_spec FROM deployments WHERE application_id=$1 AND revision=$2 AND recovery_state='succeeded'", a.ID, a.Revision).Scan(&recovered)
	if errors.Is(err, pgx.ErrNoRows) {
		return a.Spec, nil
	}
	if err != nil {
		return spec.Application{}, err
	}
	if recovered == nil {
		return a.Spec, nil
	}
	return *recovered, nil
}
