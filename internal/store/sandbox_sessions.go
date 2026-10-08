package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/jackc/pgx/v5"
)

func SessionAuthority(p Principal, a Application, service, permission string) bool {
	svc, ok := a.Spec.Services[service]
	return ok && sandbox.AllowsIdentity(svc.Session, p.ID) && (p.Admin || p.Email == "") && p.CredentialType == "machine" && p.KeyID != "" && p.Project == a.Project && p.Environment == a.Environment && p.Application == a.Name && contains(p.Permissions, permission) && p.Allows(permission, a.Project, a.Environment, a.Name)
}

const sessionCols = `j.id,j.application_id,j.deployment_id,j.service,j.revision,j.image,j.generation,j.identity_id,j.key_id,j.owner_hash,j.runtime_hash,j.status,j.message,j.cleanup_pending,j.namespace_uid,j.pod_uid,j.container_id,j.source,j.call_token,j.call_request_id,j.call_until,j.lease_token,j.lease_until,j.created_at,j.idle_until,j.expires_at,j.closed_at,a.project,a.environment,a.name`

func scanSession(row scanner) (sandbox.Record, error) {
	var v sandbox.Record
	err := row.Scan(&v.ID, &v.ApplicationID, &v.DeploymentID, &v.Service, &v.Revision, &v.Image, &v.Generation, &v.IdentityID, &v.KeyID, &v.OwnerHash, &v.RuntimeHash, &v.Status, &v.Message, &v.CleanupPending, &v.NamespaceUID, &v.PodUID, &v.ContainerID, &v.Source, &v.CallToken, &v.CallRequestID, &v.CallUntil, &v.LeaseToken, &v.LeaseUntil, &v.CreatedAt, &v.IdleUntil, &v.ExpiresAt, &v.ClosedAt, &v.Project, &v.Environment, &v.ApplicationName)
	return v, err
}
func sessionCredential(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key, identity string, a Application, permission string) (bool, error) {
	var valid bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys k JOIN identities i ON i.id=k.identity_id WHERE k.id=$1 AND k.identity_id=$2 AND k.revoked_at IS NULL AND (k.expires_at>now() OR (k.never_expires AND EXISTS(SELECT 1 FROM automation_key_scopes s WHERE s.key_id=k.id))) AND k.kind='machine' AND k.project=$3 AND k.environment=$4 AND k.application=$5 AND $6=ANY(k.permissions) AND NOT i.disabled AND (i.project='' OR i.project=$3) AND (i.environment='' OR i.environment=$4) AND (i.admin OR (COALESCE(i.email,'')='' AND $6=ANY(i.permissions))))`, key, identity, a.Project, a.Environment, a.Name, permission).Scan(&valid)
	return valid, err
}
func (s *Store) CreateSession(ctx context.Context, p Principal, app, service, owner, idem string, in sandbox.CreateRequest) (sandbox.Record, error) {
	var zero sandbox.Record
	if len(owner) != 64 || len(idem) < 8 || len(idem) > 128 || in.ExpectedRevision < 1 || !sandbox.ValidImage(in.ExpectedImage) {
		return zero, ErrInput
	}
	runtime, err := sandbox.HashKey(in.RuntimeKey)
	if err != nil {
		return zero, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", app))
	if err != nil {
		return zero, err
	}
	if !SessionAuthority(p, a, service, "sessions:create") {
		return zero, ErrForbidden
	}
	valid, err := sessionCredential(ctx, tx, p.KeyID, p.ID, a, "sessions:create")
	if err != nil {
		return zero, err
	}
	if !valid {
		return zero, ErrForbidden
	}
	old, err := scanSession(tx.QueryRow(ctx, "SELECT "+sessionCols+" FROM sandbox_sessions j JOIN applications a ON a.id=j.application_id WHERE j.identity_id=$1 AND j.application_id=$2 AND j.service=$3 AND j.owner_hash=$4 AND j.idempotency_key=$5", p.ID, app, service, owner, idem))
	if err == nil {
		if old.Revision != in.ExpectedRevision || old.Image != in.ExpectedImage || old.RuntimeHash != runtime {
			return zero, fmt.Errorf("%w: session idempotency key belongs to another request", ErrConflict)
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if a.Revision != in.ExpectedRevision {
		return zero, fmt.Errorf("%w: application revision changed", ErrConflict)
	}
	base, err := scanDep(tx.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE application_id=$1 AND revision=$2", app, in.ExpectedRevision))
	if err != nil {
		return zero, err
	}
	if base.Status != "succeeded" || base.ResolvedSpec == nil {
		return zero, fmt.Errorf("%w: session requires a successful resolved deployment", ErrConflict)
	}
	svc, ok := base.ResolvedSpec.Services[service]
	if !ok || svc.Session == nil || svc.Suspended || !sandbox.AllowsIdentity(svc.Session, p.ID) || svc.Image != in.ExpectedImage {
		return zero, fmt.Errorf("%w: session template does not match", ErrConflict)
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND `+resizeBlocking+`)`, app).Scan(&pending); err != nil {
		return zero, err
	}
	if pending {
		return zero, fmt.Errorf("%w: application maintenance is pending", ErrConflict)
	}
	var active, retained, owned int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status<>'closed' OR cleanup_pending),count(*),count(*) FILTER(WHERE (status<>'closed' OR cleanup_pending) AND identity_id=$2 AND owner_hash=$3 AND runtime_hash=$4 AND service=$5) FROM sandbox_sessions WHERE application_id=$1`, app, p.ID, owner, runtime, service).Scan(&active, &retained, &owned)
	if err != nil {
		return zero, err
	}
	if active >= sandbox.MaxSessions || retained >= sandbox.MaxHistory || owned != 0 {
		return zero, fmt.Errorf("%w: session capacity is exhausted or the owner runtime already exists", ErrConflict)
	}
	id, generation := NewID(), NewID()
	_, err = tx.Exec(ctx, `INSERT INTO sandbox_sessions(id,application_id,deployment_id,service,revision,image,generation,identity_id,key_id,owner_hash,runtime_hash,idempotency_key,source,idle_until,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now()+$14::interval,now()+$15::interval)`, id, app, base.ID, service, in.ExpectedRevision, svc.Image, generation, p.ID, p.KeyID, owner, runtime, idem, JSON(base.ResolvedSpec), fmt.Sprintf("%d seconds", svc.Session.IdleSeconds), fmt.Sprintf("%d seconds", svc.Session.LifetimeSeconds))
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'sessions:create',$3,$4)`, p.ID, p.KeyID, id, JSON(map[string]any{"application_id": app, "service": service, "revision": in.ExpectedRevision}))
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return s.session(ctx, id)
}
func (s *Store) session(ctx context.Context, id string) (sandbox.Record, error) {
	return scanSession(s.Pool.QueryRow(ctx, "SELECT "+sessionCols+" FROM sandbox_sessions j JOIN applications a ON a.id=j.application_id WHERE j.id=$1", id))
}
func (s *Store) ReadSession(ctx context.Context, p Principal, app, service, owner, id, permission string) (sandbox.Record, error) {
	var zero sandbox.Record
	a, err := s.Application(ctx, app)
	if err != nil {
		return zero, err
	}
	if !SessionAuthority(p, a, service, permission) {
		return zero, ErrForbidden
	}
	valid, err := sessionCredential(ctx, s.Pool, p.KeyID, p.ID, a, permission)
	if err != nil {
		return zero, err
	}
	if !valid {
		return zero, ErrForbidden
	}
	r, err := s.session(ctx, id)
	if err != nil {
		return zero, err
	}
	if r.ApplicationID != app || r.Service != service || r.IdentityID != p.ID || r.OwnerHash != owner {
		return zero, pgx.ErrNoRows
	}
	return r, nil
}
func (s *Store) ListSessions(ctx context.Context, p Principal, app, service, owner, runtimeKey string) ([]sandbox.Record, error) {
	runtime, err := sandbox.HashKey(runtimeKey)
	if err != nil {
		return nil, ErrInput
	}
	a, err := s.Application(ctx, app)
	if err != nil {
		return nil, err
	}
	if !SessionAuthority(p, a, service, "sessions:read") {
		return nil, ErrForbidden
	}
	valid, err := sessionCredential(ctx, s.Pool, p.KeyID, p.ID, a, "sessions:read")
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+sessionCols+" FROM sandbox_sessions j JOIN applications a ON a.id=j.application_id WHERE j.application_id=$1 AND j.service=$2 AND j.identity_id=$3 AND j.owner_hash=$4 AND j.runtime_hash=$5 ORDER BY j.created_at DESC LIMIT 8", app, service, p.ID, owner, runtime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sandbox.Record{}
	for rows.Next() {
		r, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) CloseSession(ctx context.Context, p Principal, app, service, owner, id string) (sandbox.Record, error) {
	r, err := s.ReadSession(ctx, p, app, service, owner, id, "sessions:delete")
	if err != nil {
		return r, err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE sandbox_sessions SET status='closing',message='Session cancellation requested',cleanup_pending=true WHERE id=$1 AND status<>'closed'`, id)
	if err != nil {
		return r, err
	}
	return s.session(ctx, id)
}
func (s *Store) HeartbeatSession(ctx context.Context, p Principal, app, service, owner, id, generation string) (sandbox.Record, error) {
	r, err := s.ReadSession(ctx, p, app, service, owner, id, "sessions:call")
	if err != nil {
		return r, err
	}
	if r.Generation != generation {
		return r, fmt.Errorf("%w: session generation changed", ErrConflict)
	}
	idle := r.Source.Services[r.Service].Session.IdleSeconds
	tag, err := s.Pool.Exec(ctx, `UPDATE sandbox_sessions SET idle_until=LEAST(expires_at,now()+$2::interval) WHERE id=$1 AND status IN ('starting','ready') AND idle_until>now() AND expires_at>now()`, id, fmt.Sprintf("%d seconds", idle))
	if err != nil {
		return r, err
	}
	if tag.RowsAffected() != 1 {
		return r, fmt.Errorf("%w: session expired or is closing", ErrConflict)
	}
	return s.session(ctx, id)
}

// ClaimSessionCall rejects every repeated request ID. Ambiguous code is never replayed.
func (s *Store) ClaimSessionCall(ctx context.Context, p Principal, app, service, owner, id, generation, requestID, inputHash string) (sandbox.Record, error) {
	var zero sandbox.Record
	if _, err := sandbox.HashKey(requestID); err != nil {
		return zero, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", app))
	if err != nil {
		return zero, err
	}
	if !SessionAuthority(p, a, service, "sessions:call") {
		return zero, ErrForbidden
	}
	r, err := scanSession(tx.QueryRow(ctx, "SELECT "+sessionCols+" FROM sandbox_sessions j JOIN applications a ON a.id=j.application_id WHERE j.id=$1 FOR UPDATE OF j", id))
	if err != nil {
		return zero, err
	}
	valid, err := sessionCredential(ctx, tx, p.KeyID, p.ID, a, "sessions:call")
	if err != nil {
		return zero, err
	}
	if !valid {
		return zero, ErrForbidden
	}
	if r.ApplicationID != app || r.Service != service || r.IdentityID != p.ID || r.OwnerHash != owner {
		return zero, pgx.ErrNoRows
	}
	if r.Generation != generation || r.Status != sandbox.Ready || r.CallToken != "" || !time.Now().Before(r.IdleUntil) || !time.Now().Before(r.ExpiresAt) {
		return zero, fmt.Errorf("%w: session generation is unavailable or busy", ErrConflict)
	}
	var calls int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM sandbox_session_calls WHERE session_id=$1`, id).Scan(&calls); err != nil {
		return zero, err
	}
	if calls >= 1024 {
		return zero, fmt.Errorf("%w: session call history is full", ErrConflict)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sandbox_session_calls(session_id,request_id,input_hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, requestID, inputHash)
	if err != nil {
		return zero, err
	}
	if tag.RowsAffected() != 1 {
		return zero, fmt.Errorf("%w: call was already submitted; code will not be replayed", ErrConflict)
	}
	r.CallToken = NewID()
	r.CallRequestID = requestID
	r.CallKeyID = p.KeyID
	_, err = tx.Exec(ctx, `UPDATE sandbox_sessions SET call_token=$2,call_request_id=$3,call_until=now()+interval '150 seconds' WHERE id=$1`, id, r.CallToken, requestID)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return r, nil
}
func (s *Store) CheckSessionCall(ctx context.Context, r sandbox.Record) error {
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT status='ready' AND generation=$3 AND call_token=$2 AND idle_until>now() AND expires_at>now() AND call_until>now() FROM sandbox_sessions WHERE id=$1`, r.ID, r.CallToken, r.Generation).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	a, err := s.Application(ctx, r.ApplicationID)
	if err != nil {
		return err
	}
	if a.Revision != r.Revision || a.Spec.Services[r.Service].Suspended || !sandbox.AllowsIdentity(a.Spec.Services[r.Service].Session, r.IdentityID) {
		return ErrForbidden
	}
	valid, err = sessionCredential(ctx, s.Pool, r.CallKeyID, r.IdentityID, a, "sessions:call")
	if err != nil {
		return err
	}
	if !valid {
		return ErrForbidden
	}
	return nil
}
func (s *Store) FinishSessionCall(ctx context.Context, r sandbox.Record, complete bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status := "complete"
	if !complete {
		status = "interrupted"
	}
	tag, err := tx.Exec(ctx, `UPDATE sandbox_sessions SET call_token='',call_request_id='',call_until='1970-01-01 UTC',status=CASE WHEN $3 THEN status ELSE 'closing' END,cleanup_pending=CASE WHEN $3 THEN cleanup_pending ELSE true END,message=CASE WHEN $3 THEN message ELSE 'Call outcome is uncertain; session cleanup is pending' END WHERE id=$1 AND call_token=$2`, r.ID, r.CallToken, complete)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE sandbox_session_calls SET outcome=$3 WHERE session_id=$1 AND request_id=$2`, r.ID, r.CallRequestID, status)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
