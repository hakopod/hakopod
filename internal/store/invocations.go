package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/jackc/pgx/v5"
)

// InvocationAuthority requires explicit machine-key scope and permission, even
// for administrators. Reads are additionally restricted to the creating identity.
func InvocationAuthority(p Principal, a Application, service, permission string) bool {
	svc, ok := a.Spec.Services[service]
	return ok && svc.Job != nil && invocation.AllowsIdentity(svc.Job.Invocation, p.ID) &&
		(p.Admin || p.Email == "") &&
		p.CredentialType == "machine" && p.KeyID != "" && p.Project == a.Project &&
		p.Environment == a.Environment && p.Application == a.Name && contains(p.Permissions, permission) &&
		p.Allows(permission, a.Project, a.Environment, a.Name)
}

const invocationCols = `j.id,j.application_id,j.deployment_id,j.service,j.revision,j.image,j.correlation_id,
j.identity_id,j.key_id,j.owner_hash,j.input_hash,j.encrypted_input,j.input_bytes,COALESCE(j.encrypted_logs,''::bytea),
j.log_truncated,j.status,j.message,j.cancel_requested,j.cleanup_pending,j.exit_code,j.namespace_uid,j.runtime_uid,
j.lease_token,j.lease_until,j.source,j.created_at,j.started_at,j.finished_at,j.expires_at,a.project,a.environment,a.name`

func scanInvocation(row scanner) (invocation.Record, error) {
	var v invocation.Record
	err := row.Scan(&v.ID, &v.ApplicationID, &v.DeploymentID, &v.Service, &v.Revision, &v.Image, &v.CorrelationID,
		&v.IdentityID, &v.KeyID, &v.OwnerHash, &v.InputHash, &v.EncryptedInput, &v.InputBytes, &v.EncryptedLogs,
		&v.LogTruncated, &v.Status, &v.Message, &v.CancelRequested, &v.CleanupPending, &v.ExitCode, &v.NamespaceUID, &v.RuntimeUID,
		&v.LeaseToken, &v.LeaseUntil, &v.Source, &v.CreatedAt, &v.StartedAt, &v.FinishedAt, &v.ExpiresAt, &v.Project, &v.Environment, &v.ApplicationName)
	return v, err
}

func (s *Store) EnqueueInvocation(ctx context.Context, p Principal, app, service, idem string, in invocation.CreateRequest, key []byte) (invocation.Record, error) {
	var zero invocation.Record
	if len(idem) < 8 || len(idem) > 128 || len(key) != 32 {
		return zero, fmt.Errorf("%w: invalid invocation configuration or Idempotency-Key", ErrInput)
	}
	owner, err := invocation.OwnerHash(in.OwnerScope)
	if err != nil {
		return zero, err
	}
	if err = invocation.ValidateCorrelationID(in.CorrelationID); err != nil {
		return zero, err
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
	if !InvocationAuthority(p, a, service, "jobs:invoke") {
		return zero, ErrForbidden
	}
	// Revalidate the credential after waiting for the application row lock.
	valid, err := invocationCredential(ctx, tx, p.KeyID, p.ID, a)
	if err != nil {
		return zero, err
	}
	if !valid {
		return zero, ErrForbidden
	}
	base, err := scanDep(tx.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE application_id=$1 AND revision=$2", app, in.ExpectedRevision))
	if err != nil {
		return zero, err
	}
	if base.Status != "succeeded" || base.ResolvedSpec == nil {
		return zero, fmt.Errorf("%w: invocation requires a successful resolved revision", ErrConflict)
	}
	svc, ok := base.ResolvedSpec.Services[service]
	if !ok || svc.Job == nil || svc.Job.Invocation == nil || svc.Suspended || !invocation.AllowsIdentity(svc.Job.Invocation, p.ID) || !strings.Contains(svc.Image, "@sha256:") || svc.Image != in.ExpectedImage {
		return zero, fmt.Errorf("%w: invocation template or image does not match", ErrConflict)
	}
	input, err := invocation.NormalizeInputs(svc.Job.Invocation, in.Inputs)
	if err != nil {
		return zero, fmt.Errorf("%w: invocation inputs do not match the template", ErrInput)
	}
	sum := sha256.Sum256(input)
	hash := hex.EncodeToString(sum[:])
	old, err := scanInvocation(tx.QueryRow(ctx, "SELECT "+invocationCols+" FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.identity_id=$1 AND j.application_id=$2 AND j.service=$3 AND j.owner_hash=$4 AND j.revision=$5 AND j.idempotency_key=$6", p.ID, app, service, owner, in.ExpectedRevision, idem))
	if err == nil {
		if old.InputHash != hash || old.Image != in.ExpectedImage || old.CorrelationID != in.CorrelationID {
			return zero, fmt.Errorf("%w: idempotency key has different input", ErrConflict)
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	var retained int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM job_invocations WHERE application_id=$1`, app).Scan(&retained); err != nil {
		return zero, err
	}
	if retained >= 1000 {
		return zero, fmt.Errorf("%w: application invocation history reached its 1000-receipt limit; wait for retention cleanup", ErrConflict)
	}
	if a.Revision != in.ExpectedRevision {
		return zero, fmt.Errorf("%w: application revision changed", ErrConflict)
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND `+resizeBlocking+`)`, app).Scan(&busy)
	if err != nil {
		return zero, err
	}
	if busy {
		return zero, fmt.Errorf("%w: application maintenance is pending", ErrConflict)
	}
	var pending, owned int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE service=$2),count(*) FILTER(WHERE identity_id=$3 AND owner_hash=$4) FROM job_invocations WHERE application_id=$1 AND (status IN ('queued','starting','running') OR cleanup_pending)`, app, service, p.ID, owner).Scan(&pending, &owned)
	if err != nil {
		return zero, err
	}
	limit := svc.Job.Invocation.QueueLimit
	if limit == 0 {
		limit = 16
	}
	if pending >= limit || owned >= invocation.MaxOwnerPending {
		return zero, fmt.Errorf("%w: invocation queue is full", ErrConflict)
	}
	id := NewID()
	sealed, err := invocation.Seal(key, id, "input", input)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_invocations(id,application_id,deployment_id,service,revision,image,correlation_id,identity_id,key_id,owner_hash,idempotency_key,input_hash,encrypted_input,input_bytes,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, app, base.ID, service, in.ExpectedRevision, svc.Image, in.CorrelationID, p.ID, p.KeyID, owner, idem, hash, sealed, len(input), JSON(base.ResolvedSpec))
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'jobs:invoke',$3,$4)`, p.ID, p.KeyID, id, JSON(map[string]any{"application_id": app, "service": service, "revision": in.ExpectedRevision}))
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return s.invocation(ctx, id)
}

func (s *Store) invocation(ctx context.Context, id string) (invocation.Record, error) {
	return scanInvocation(s.Pool.QueryRow(ctx, "SELECT "+invocationCols+" FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.id=$1", id))
}

func (s *Store) ReadInvocation(ctx context.Context, p Principal, app, service, owner, id, permission string) (invocation.Record, error) {
	a, err := s.Application(ctx, app)
	if err != nil {
		return invocation.Record{}, err
	}
	if !InvocationAuthority(p, a, service, permission) {
		return invocation.Record{}, ErrForbidden
	}
	v, err := s.invocation(ctx, id)
	if err != nil {
		return v, err
	}
	if v.IdentityID != p.ID || v.ApplicationID != app || v.Service != service || v.OwnerHash != owner || time.Now().After(v.ExpiresAt) {
		return invocation.Record{}, pgx.ErrNoRows
	}
	return v, nil
}
func (s *Store) ListInvocations(ctx context.Context, p Principal, app, service, owner, correlation, idem string, active bool) ([]invocation.Record, error) {
	a, err := s.Application(ctx, app)
	if err != nil {
		return nil, err
	}
	if !InvocationAuthority(p, a, service, "jobs:read") {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+invocationCols+` FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.application_id=$1 AND j.service=$2 AND j.identity_id=$3 AND j.owner_hash=$4 AND j.correlation_id=$5 AND j.expires_at>now() AND ($6='' OR j.idempotency_key=$6) AND (NOT $7 OR j.status IN ('queued','starting','running') OR j.cleanup_pending) ORDER BY j.created_at DESC LIMIT 100`, app, service, p.ID, owner, correlation, idem, active)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []invocation.Record{}
	for rows.Next() {
		v, e := scanInvocation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
		if idem != "" {
			break
		}
	}
	return out, rows.Err()
}
func (s *Store) CancelInvocation(ctx context.Context, p Principal, app, service, owner, id string) (invocation.Record, error) {
	v, err := s.ReadInvocation(ctx, p, app, service, owner, id, "jobs:cancel")
	if err != nil {
		return v, err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE job_invocations SET cancel_requested=true WHERE id=$1 AND status IN ('queued','starting','running')`, id)
	if err != nil {
		return v, err
	}
	return s.invocation(ctx, id)
}

// InvocationClaim holds the same application lock as deployments. A dropped
// database session releases the lock; a new worker resumes the durable receipt.
type InvocationClaim struct {
	claim  *Claim
	Record invocation.Record
	store  *Store
}

func (c *InvocationClaim) Release() { c.claim.Release() }

func (s *Store) ClaimInvocation(ctx context.Context) (*InvocationClaim, error) {
	// Delete only expired, fully cleaned receipts in bounded batches.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM job_invocations WHERE id IN (SELECT id FROM job_invocations WHERE expires_at<now() AND status IN ('succeeded','failed','cancelled') AND NOT cleanup_pending LIMIT 100)`); err != nil {
		return nil, err
	}
	// Select one candidate per application so a busy application's queue cannot
	// hide runnable work in other applications. Recover active work before queues.
	rows, err := s.Pool.Query(ctx, `SELECT id,application_id FROM (
 SELECT DISTINCT ON(application_id) id,application_id,created_at,
 CASE WHEN status IN ('starting','running') OR cleanup_pending THEN 0 WHEN status='queued' THEN 1 ELSE 2 END priority
 FROM job_invocations WHERE status IN ('queued','starting','running') OR cleanup_pending OR (finished_at>now()-interval '10 minutes' AND lease_until<now())
 ORDER BY application_id,priority,created_at
 ) candidates ORDER BY priority,created_at LIMIT 16`)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, app string }
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.app); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range candidates {
		conn, e := s.Pool.Acquire(ctx)
		if e != nil {
			return nil, e
		}
		var locked bool
		e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", v.app).Scan(&locked)
		if e != nil || !locked {
			conn.Release()
			if e != nil {
				return nil, e
			}
			continue
		}
		cl := &Claim{Conn: conn}
		tx, e := conn.Begin(ctx)
		if e != nil {
			cl.Release()
			return nil, e
		}
		a, e := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", v.app))
		if e != nil {
			tx.Rollback(ctx)
			cl.Release()
			return nil, e
		}
		r, e := scanInvocation(tx.QueryRow(ctx, "SELECT "+invocationCols+" FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.id=$1 FOR UPDATE OF j", v.id))
		if e != nil {
			tx.Rollback(ctx)
			cl.Release()
			return nil, e
		}
		var busy bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM job_invocations WHERE application_id=$1 AND id<>$2 AND (status IN ('starting','running') OR cleanup_pending))`, v.app, v.id).Scan(&busy)
		if e != nil {
			tx.Rollback(ctx)
			cl.Release()
			return nil, e
		}
		if r.Status == invocation.Queued && (a.Revision != r.Revision || r.CancelRequested) {
			_, e = tx.Exec(ctx, `UPDATE job_invocations SET status='cancelled',message='Invocation cancelled before execution',finished_at=now(),encrypted_input=''::bytea WHERE id=$1`, r.ID)
			if e == nil {
				e = tx.Commit(ctx)
			} else {
				tx.Rollback(ctx)
			}
			cl.Release()
			if e != nil {
				return nil, e
			}
			continue
		}
		if busy || r.Terminal() && !r.CleanupPending && (r.FinishedAt == nil || time.Since(*r.FinishedAt) > 10*time.Minute || time.Now().Before(r.LeaseUntil)) {
			tx.Rollback(ctx)
			cl.Release()
			continue
		}
		r.LeaseToken = NewID()
		r.LeaseUntil = time.Now().Add(time.Minute)
		_, e = tx.Exec(ctx, `UPDATE job_invocations SET lease_token=$2,lease_until=$3,status=CASE WHEN status='queued' THEN 'starting' ELSE status END,started_at=COALESCE(started_at,now()) WHERE id=$1`, r.ID, r.LeaseToken, r.LeaseUntil)
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if e != nil {
			cl.Release()
			return nil, e
		}
		cl.App = a
		r, e = scanInvocation(conn.QueryRow(ctx, "SELECT "+invocationCols+" FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.id=$1", r.ID))
		if e != nil {
			cl.Release()
			return nil, e
		}
		return &InvocationClaim{claim: cl, Record: r, store: s}, nil
	}
	return nil, nil
}

var ErrInvocationCancelled = errors.New("invocation cancellation requested")

func (c *InvocationClaim) Check(ctx context.Context) error {
	if err := c.claim.Check(ctx); err != nil {
		return err
	}
	var cancelled, eligible bool
	err := c.claim.Conn.QueryRow(ctx, `UPDATE job_invocations SET lease_until=now()+interval '1 minute' WHERE id=$1 AND lease_token=$2 RETURNING cancel_requested,revision=(SELECT revision FROM applications WHERE id=application_id)`, c.Record.ID, c.Record.LeaseToken).Scan(&cancelled, &eligible)
	if err != nil {
		return err
	}
	if cancelled || !eligible {
		return ErrInvocationCancelled
	}
	a, err := scanApp(c.claim.Conn.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", c.Record.ApplicationID))
	if err != nil {
		return err
	}
	valid, err := invocationCredential(ctx, c.claim.Conn, c.Record.KeyID, c.Record.IdentityID, a)
	if err != nil {
		return err
	}
	svc, ok := a.Spec.Services[c.Record.Service]
	if !valid || !ok || svc.Job == nil || svc.Suspended || !invocation.AllowsIdentity(svc.Job.Invocation, c.Record.IdentityID) {
		return ErrForbidden
	}
	return nil
}
func (c *InvocationClaim) Save(ctx context.Context, state invocation.RuntimeState, encryptedLogs []byte) error {
	if state.Status != invocation.Starting && state.Status != invocation.Running && state.Status != invocation.Succeeded && state.Status != invocation.Failed && state.Status != invocation.Cancelled {
		return errors.New("invalid invocation runtime status")
	}
	terminal := state.Status == invocation.Succeeded || state.Status == invocation.Failed || state.Status == invocation.Cancelled
	tag, err := c.claim.Conn.Exec(ctx, `UPDATE job_invocations SET status=$3,message=$4,namespace_uid=CASE WHEN $5='' THEN namespace_uid ELSE $5 END,runtime_uid=CASE WHEN $6='' THEN runtime_uid ELSE $6 END,exit_code=$7,encrypted_logs=CASE WHEN $8::bytea IS NULL THEN encrypted_logs ELSE $8 END,log_truncated=$9,cleanup_pending=$10,finished_at=CASE WHEN $10 THEN COALESCE(finished_at,now()) ELSE finished_at END WHERE id=$1 AND lease_token=$2`, c.Record.ID, c.Record.LeaseToken, state.Status, state.Message, state.NamespaceUID, state.RuntimeUID, state.ExitCode, encryptedLogs, state.LogTruncated, terminal)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	c.Record, err = scanInvocation(c.claim.Conn.QueryRow(ctx, "SELECT "+invocationCols+" FROM job_invocations j JOIN applications a ON a.id=j.application_id WHERE j.id=$1", c.Record.ID))
	return err
}

// Recheck authority on the already-held connection. Taking another pool slot
// while holding an application transaction can deadlock a saturated pool.
func invocationCredential(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, keyID, identityID string, a Application) (bool, error) {
	var valid bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys k JOIN identities i ON i.id=k.identity_id
 WHERE k.id=$1 AND i.id=$2 AND k.kind='machine' AND k.revoked_at IS NULL AND NOT i.disabled
 AND (k.expires_at>now() OR (k.never_expires AND EXISTS(SELECT 1 FROM automation_key_scopes s WHERE s.key_id=k.id)))
 AND k.project=$3 AND k.environment=$4 AND k.application=$5 AND 'jobs:invoke'=ANY(k.permissions)
 AND (i.project='' OR i.project=$3) AND (i.environment='' OR i.environment=$4)
 AND (i.admin OR (COALESCE(i.email,'')='' AND 'jobs:invoke'=ANY(i.permissions))))`, keyID, identityID, a.Project, a.Environment, a.Name).Scan(&valid)
	return valid, err
}
func (c *InvocationClaim) Cleaned(ctx context.Context) error {
	tag, err := c.claim.Conn.Exec(ctx, `UPDATE job_invocations SET cleanup_pending=false,encrypted_input=''::bytea,lease_token='',lease_until=now()+interval '1 minute' WHERE id=$1 AND lease_token=$2 AND status IN ('succeeded','failed','cancelled')`, c.Record.ID, c.Record.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

func fenceInvocationsForDeployment(ctx context.Context, tx pgx.Tx, app string) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_invocations WHERE application_id=$1 AND (status IN ('starting','running') OR cleanup_pending))`, app).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: cancel or finish active invocations before deploying", ErrConflict)
	}
	_, err := tx.Exec(ctx, `UPDATE job_invocations SET status='cancelled',cancel_requested=true,message='Application revision changed before execution',finished_at=now(),encrypted_input=''::bytea WHERE application_id=$1 AND status='queued'`, app)
	return err
}
