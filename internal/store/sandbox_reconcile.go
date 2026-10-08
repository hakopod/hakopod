package store

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/jackc/pgx/v5"
)

// ClaimSession leases one reconciliation step, not the complete session lifetime.
func (s *Store) ClaimSession(ctx context.Context) (sandbox.Record, error) {
	var zero sandbox.Record
	token := NewID()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM sandbox_sessions WHERE lease_until<now() AND (status<>'closed' OR cleanup_pending OR closed_at>now()-interval '10 minutes') ORDER BY CASE WHEN status='closing' OR idle_until<=now() OR expires_at<=now() OR (call_token<>'' AND call_until<=now()) THEN 0 WHEN status='starting' THEN 1 ELSE 2 END,lease_until,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `UPDATE sandbox_sessions SET lease_token=$2,lease_until=now()+interval '45 seconds' WHERE id=$1`, id, token)
	if err != nil {
		return zero, err
	}
	r, err := scanSession(tx.QueryRow(ctx, "SELECT "+sessionCols+" FROM sandbox_sessions j JOIN applications a ON a.id=j.application_id WHERE j.id=$1", id))
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return r, nil
}
func (s *Store) CheckSessionLease(ctx context.Context, r sandbox.Record) error {
	current, err := s.session(ctx, r.ID)
	if err != nil {
		return err
	}
	if current.LeaseToken != r.LeaseToken || !time.Now().Before(current.LeaseUntil) {
		return ErrClaimLost
	}
	if current.Status == sandbox.Closing || current.Status == sandbox.Closed || !time.Now().Before(current.IdleUntil) || !time.Now().Before(current.ExpiresAt) {
		return ErrConflict
	}
	a, err := s.Application(ctx, r.ApplicationID)
	if err != nil {
		return err
	}
	if a.Revision != r.Revision || a.Spec.Services[r.Service].Suspended || !sandbox.AllowsIdentity(a.Spec.Services[r.Service].Session, r.IdentityID) {
		return ErrForbidden
	}
	valid, err := sessionCredential(ctx, s.Pool, r.KeyID, r.IdentityID, a, "sessions:create")
	if err != nil {
		return err
	}
	if !valid {
		return ErrForbidden
	}
	return nil
}
func (s *Store) ReleaseSessionLease(ctx context.Context, r sandbox.Record) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sandbox_sessions SET lease_token='',lease_until=now()+interval '2 seconds' WHERE id=$1 AND lease_token=$2`, r.ID, r.LeaseToken)
	return err
}
func (s *Store) SaveSessionRuntime(ctx context.Context, r sandbox.Record, state sandbox.RuntimeState) error {
	if state.NamespaceUID == "" {
		return fmt.Errorf("session runtime returned no namespace identity")
	}
	if state.Ready && (state.PodUID == "" || state.ContainerID == "") {
		return fmt.Errorf("ready session has no container identity")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE sandbox_sessions SET namespace_uid=$3,pod_uid=$4,container_id=$5,cleanup_pending=false,status=CASE WHEN $6 THEN 'ready' ELSE 'starting' END,message=CASE WHEN $6 THEN 'Session is ready' ELSE 'Session is starting' END WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status IN ('starting','ready') AND (namespace_uid='' OR namespace_uid=$3) AND (pod_uid='' OR pod_uid=$4) AND (container_id='' OR container_id=$5)`, r.ID, r.LeaseToken, state.NamespaceUID, state.PodUID, state.ContainerID, state.Ready)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}
func (s *Store) RequestSessionCleanup(ctx context.Context, r sandbox.Record, message string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE sandbox_sessions SET status='closing',cleanup_pending=true,message=$3 WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status<>'closed'`, r.ID, r.LeaseToken, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}
func (s *Store) SessionCleaned(ctx context.Context, r sandbox.Record) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE sandbox_sessions SET status='closed',cleanup_pending=false,closed_at=COALESCE(closed_at,now()),call_token='',call_request_id='',call_until='1970-01-01 UTC',lease_token='',lease_until=now()+interval '1 minute' WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status IN ('closing','closed')`, r.ID, r.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	_, err = tx.Exec(ctx, `UPDATE sandbox_session_calls SET outcome='interrupted' WHERE session_id=$1 AND outcome='started'`, r.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) PruneSessions(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sandbox_sessions WHERE id IN (SELECT id FROM sandbox_sessions WHERE status='closed' AND NOT cleanup_pending AND closed_at<now()-interval '24 hours' LIMIT 100)`)
	return err
}

func fenceSessionsForDeployment(ctx context.Context, tx pgx.Tx, app string) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sandbox_sessions WHERE application_id=$1 AND (status<>'closed' OR cleanup_pending))`, app).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: close active sandbox sessions before deploying", ErrConflict)
	}
	return nil
}
