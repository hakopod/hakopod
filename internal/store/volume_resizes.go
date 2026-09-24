package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VolumeResize struct {
	ID               string           `json:"id"`
	ApplicationID    string           `json:"application_id"`
	IdentityID       string           `json:"-"`
	KeyID            string           `json:"-"`
	ExpectedRevision int64            `json:"expected_revision"`
	Claim            string           `json:"claim"`
	TargetClaim      string           `json:"target_claim"`
	SizeGiB          int64            `json:"size_gib"`
	OldGiB           int64            `json:"old_gib"`
	SourceSpec       spec.Application `json:"-"`
	TargetSpec       spec.Application `json:"-"`
	Phase            string           `json:"phase"`
	Error            string           `json:"error"`
	Runtime          json.RawMessage  `json:"-"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	Services         []string         `json:"services"`
	Verified         bool             `json:"verified"`
	CopiedBytes      int64            `json:"copied_bytes"`
}

const resizeCols = `id,application_id,identity_id,key_id,expected_revision,claim,target_claim,size_gib,old_gib,source_spec,target_spec,phase,error,runtime,created_at,updated_at`
const resizeBlocking = `phase IN ('queued','copying','switching','cancelling','deleting_original')`

func scanResize(row pgx.Row) (VolumeResize, error) {
	var r VolumeResize
	err := row.Scan(&r.ID, &r.ApplicationID, &r.IdentityID, &r.KeyID, &r.ExpectedRevision, &r.Claim, &r.TargetClaim, &r.SizeGiB, &r.OldGiB, &r.SourceSpec, &r.TargetSpec, &r.Phase, &r.Error, &r.Runtime, &r.CreatedAt, &r.UpdatedAt)
	if err == nil {
		var report struct {
			Verified bool  `json:"verified"`
			Bytes    int64 `json:"bytes"`
		}
		if e := json.Unmarshal(r.Runtime, &report); e != nil {
			return r, e
		}
		r.Verified = report.Verified
		r.CopiedBytes = report.Bytes
		_, plan, e := spec.ResizeVolume(r.SourceSpec, r.Claim, r.SizeGiB, r.ID)
		if e != nil {
			return r, e
		}
		r.Services = plan.Services
	}
	return r, err
}
func ResizeID(identity, idem string) string {
	sum := sha256.Sum256([]byte("volume-resize:" + identity + ":" + idem))
	return hex.EncodeToString(sum[:16])
}
func (s *Store) VolumeResize(ctx context.Context, id string) (VolumeResize, error) {
	return scanResize(s.Pool.QueryRow(ctx, "SELECT "+resizeCols+" FROM volume_resizes WHERE id=$1", id))
}
func (s *Store) VolumeResizes(ctx context.Context, app string) ([]VolumeResize, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+resizeCols+" FROM volume_resizes WHERE application_id=$1 ORDER BY CASE WHEN phase IN ('completed','cancelled') THEN 1 ELSE 0 END, created_at DESC LIMIT 100", app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VolumeResize{}
	for rows.Next() {
		r, e := scanResize(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) resizeAuthority(ctx context.Context, p Principal, a Application) error {
	if !p.CanManageApplication(a.Project, a.Environment, a.Name) || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return ErrForbidden
	}
	if s.AuthorizeRetainedCleanup != nil {
		return s.AuthorizeRetainedCleanup(ctx, p, a.Project, a.Environment)
	}
	return nil
}
func (s *Store) StartVolumeResize(ctx context.Context, p Principal, app, claim string, size, revision int64, idem string, runtime json.RawMessage) (VolumeResize, error) {
	if len(idem) < 8 || len(idem) > 128 {
		return VolumeResize{}, fmt.Errorf("Idempotency-Key must contain 8–128 characters")
	}
	a, err := s.Application(ctx, app)
	if err != nil {
		return VolumeResize{}, err
	}
	if err = s.resizeAuthority(ctx, p, a); err != nil {
		return VolumeResize{}, err
	}
	id := ResizeID(p.ID, idem)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return VolumeResize{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,1))", p.ID+":"+idem); err != nil {
		return VolumeResize{}, err
	}
	prior, err := scanResize(tx.QueryRow(ctx, "SELECT "+resizeCols+" FROM volume_resizes WHERE id=$1", id))
	if err == nil {
		if prior.ApplicationID != app || prior.Claim != claim || prior.SizeGiB != size || prior.ExpectedRevision != revision {
			return prior, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return VolumeResize{}, err
	}
	if s.AdmitDeployment != nil {
		if err = s.AdmitDeployment(ctx, tx, p, a.Project, a.Environment, idem); err != nil {
			return VolumeResize{}, err
		}
	}
	var runtimeLocked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,3))", app).Scan(&runtimeLocked); err != nil {
		return VolumeResize{}, err
	}
	if !runtimeLocked {
		return VolumeResize{}, fmt.Errorf("%w: application maintenance is advancing", ErrConflict)
	}
	a, err = scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", app))
	if err != nil {
		return VolumeResize{}, err
	}
	if a.Revision != revision {
		return VolumeResize{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044231)"); err != nil {
		return VolumeResize{}, err
	}
	var backupBusy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE status='running' AND (source->>'application_id'=$1 OR target->>'application_id'=$1))", app).Scan(&backupBusy); err != nil {
		return VolumeResize{}, err
	}
	if backupBusy {
		return VolumeResize{}, fmt.Errorf("wait for the active database backup or restore before resizing")
	}
	var ephemeral bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM previews WHERE application_id=$1) OR EXISTS(SELECT 1 FROM showcase WHERE application_id=$1)", app).Scan(&ephemeral); err != nil {
		return VolumeResize{}, err
	}
	if ephemeral {
		return VolumeResize{}, fmt.Errorf("resize is available for permanent applications, not expiring previews or sample applications")
	}
	var blocked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND `+resizeBlocking+`) OR EXISTS(SELECT 1 FROM deployment_volume_cleanup c JOIN deployments d ON d.id=c.deployment_id WHERE d.application_id=$1 AND NOT c.completed AND d.status IN ('queued','running','succeeded'))`, app).Scan(&blocked); err != nil {
		return VolumeResize{}, err
	}
	if blocked {
		return VolumeResize{}, fmt.Errorf("%w: wait for pending application work", ErrConflict)
	}
	var source spec.Application
	if err = tx.QueryRow(ctx, "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded' AND resolved_spec IS NOT NULL", app, revision).Scan(&source); err != nil {
		return VolumeResize{}, fmt.Errorf("resize requires a successful current deployment")
	}
	next, plan, err := spec.ResizeVolume(source, claim, size, id)
	if err != nil {
		return VolumeResize{}, err
	}
	if s.ValidateDeployment != nil {
		if err = s.ValidateDeployment(ctx, a, next); err != nil {
			return VolumeResize{}, err
		}
	}
	if err = s.reserveStorage(ctx, tx, a, source); err != nil {
		return VolumeResize{}, err
	}
	// Both claims remain charged. Only this bounded migration may temporarily
	// exceed the workspace quota by the size of its existing source claim.
	if err = s.reserveStorage(ctx, tx, a, next, plan.OldGiB); err != nil {
		return VolumeResize{}, err
	}
	if len(runtime) == 0 {
		runtime = json.RawMessage(`{}`)
	}
	_, err = tx.Exec(ctx, `INSERT INTO volume_resizes(id,application_id,identity_id,key_id,idempotency_key,expected_revision,claim,target_claim,size_gib,old_gib,source_spec,target_spec,runtime) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, app, p.ID, p.KeyID, idem, revision, claim, plan.TargetClaim, size, plan.OldGiB, JSON(source), JSON(next), runtime)
	if err != nil {
		return VolumeResize{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'volume.resize-requested',$3,$4)", p.ID, p.KeyID, app, JSON(map[string]any{"operation": id, "claim": claim, "from_gib": plan.OldGiB, "to_gib": size})); err != nil {
		return VolumeResize{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return VolumeResize{}, err
	}
	return s.VolumeResize(ctx, id)
}

// ResizeAction only requests durable work. Destructive runtime changes still
// recheck the current owner and the exact PVC identities at every step.
func (s *Store) ResizeAction(ctx context.Context, p Principal, app, id, action, confirm string) error {
	a, err := s.Application(ctx, app)
	if err != nil {
		return err
	}
	if err = s.resizeAuthority(ctx, p, a); err != nil {
		return err
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseClaimConnection(conn)
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", app).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("%w: resize is currently advancing; retry shortly", ErrConflict)
	}
	r, err := scanResize(conn.QueryRow(ctx, "SELECT "+resizeCols+" FROM volume_resizes WHERE id=$1 AND application_id=$2", id, app))
	if err != nil {
		return err
	}
	phase := r.Phase
	runtime := r.Runtime
	switch action {
	case "retry":
		if r.Error == "" {
			return ErrConflict
		}
		var state map[string]any
		_ = json.Unmarshal(runtime, &state)
		if state == nil {
			state = map[string]any{}
		}
		state["retry"] = true
		runtime = JSON(state)
	case "cancel":
		if phase != "queued" && phase != "copying" {
			return ErrConflict
		}
		phase = "cancelling"
	case "retain":
		if phase != "switching" || r.Error == "" {
			return ErrConflict
		}
		phase = "original_retained"
	case "delete-original":
		if phase != "original_retained" || confirm != r.Claim {
			return ErrConflict
		}

		var busy bool
		if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND id<>$2 AND "+resizeBlocking+")", app, id).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return fmt.Errorf("%w: another volume maintenance operation is active", ErrConflict)
		}
		phase = "deleting_original"
	default:
		return fmt.Errorf("unknown resize action")
	}
	_, err = conn.Exec(ctx, "UPDATE volume_resizes SET phase=$2,error='',key_id=$3,runtime=$4,updated_at=now() WHERE id=$1", id, phase, p.KeyID, runtime)
	return err
}

type ResizeClaim struct {
	mu     sync.Mutex
	conn   *pgxpool.Conn
	store  *Store
	Resize VolumeResize
	App    Application
}

func (s *Store) ClaimVolumeResize(ctx context.Context) (*ResizeClaim, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,application_id FROM volume_resizes WHERE "+resizeBlocking+" AND error='' ORDER BY updated_at LIMIT 16")
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, app string }
	items := []candidate{}
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.app); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range items {
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", v.app).Scan(&locked); err != nil || !locked {
			conn.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		c := &ResizeClaim{conn: conn, store: s}
		c.Resize, err = scanResize(conn.QueryRow(ctx, "SELECT "+resizeCols+" FROM volume_resizes WHERE id=$1 AND "+resizeBlocking+" AND error=''", v.id))
		if err == nil {
			c.App, err = s.Application(ctx, v.app)
		}
		if err != nil {
			c.Release()
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		return c, nil
	}
	return nil, nil
}
func (c *ResizeClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		releaseClaimConnection(c.conn)
		c.conn = nil
	}
}
func (c *ResizeClaim) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if err := c.conn.Ping(ctx); err != nil {
		return err
	}
	p, err := c.store.KeyPrincipal(ctx, c.Resize.KeyID)
	if err != nil {
		return err
	}
	if err = c.store.resizeAuthority(ctx, p, c.App); err != nil {
		return err
	}
	var revision int64
	if err = c.conn.QueryRow(ctx, "SELECT revision FROM applications WHERE id=$1", c.App.ID).Scan(&revision); err != nil {
		return err
	}
	if revision != c.App.Revision {
		return ErrConflict
	}
	return nil
}
func (c *ResizeClaim) Save(ctx context.Context, phase string, runtime any, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return ErrClaimLost
	}
	data := JSON(runtime)
	_, err := c.conn.Exec(ctx, "UPDATE volume_resizes SET phase=$2,runtime=$3,error=$4,updated_at=now() WHERE id=$1", c.Resize.ID, phase, data, message)
	if err == nil {
		c.Resize.Phase = phase
		c.Resize.Error = message
		c.Resize.Runtime = data
	}
	return err
}

func (c *ResizeClaim) BeginCutover(ctx context.Context) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return ErrClaimLost
	}
	r := c.Resize
	var verified struct {
		Verified bool `json:"verified"`
	}
	if json.Unmarshal(r.Runtime, &verified) != nil || !verified.Verified || r.Phase != "copying" {
		return fmt.Errorf("%w: a verified offline copy is required before cutover", ErrConflict)
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO deployments(id,application_id,identity_id,key_id,idempotency_key,request_hash,revision,spec,resolved_spec,status,started_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,'running',now())`, r.ID, r.ApplicationID, r.IdentityID, r.KeyID, "volume-resize:"+r.ID, []byte(r.ID), r.ExpectedRevision+1, JSON(r.TargetSpec)); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE applications SET revision=$2,spec=$3,status='running',updated_at=now() WHERE id=$1 AND revision=$4", r.ApplicationID, r.ExpectedRevision+1, JSON(r.TargetSpec), r.ExpectedRevision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE volume_resizes SET phase='switching',updated_at=now() WHERE id=$1", r.ID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err == nil {
		c.Resize.Phase = "switching"
		c.App.Revision = r.ExpectedRevision + 1
		c.App.Spec = r.TargetSpec
	}
	return err
}
func (c *ResizeClaim) CutoverResult(ctx context.Context, result any, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return ErrClaimLost
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status, appStatus, phase := "succeeded", "healthy", "original_retained"
	if message != "" {
		status = "failed"
		appStatus = "failed"
		phase = "switching"
	}
	if _, err = tx.Exec(ctx, "UPDATE deployments SET status=$2,error=$3,result=$4,finished_at=now() WHERE id=$1", c.Resize.ID, status, message, JSON(result)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE applications SET status=$2,observed=$3,updated_at=now() WHERE id=$1 AND revision=$4", c.App.ID, appStatus, JSON(result), c.App.Revision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE volume_resizes SET phase=$2,error=$3,updated_at=now() WHERE id=$1", c.Resize.ID, phase, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (c *ResizeClaim) Reclaimed(ctx context.Context, claim, phase string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return ErrClaimLost
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,42))", c.App.Project+":"+c.App.Environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE application_id=$1 AND claim=$2", c.App.ID, claim); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE volume_resizes SET phase=$2,error='',updated_at=now() WHERE id=$1", c.Resize.ID, phase); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'volume.resize-reclaimed',$3,$4)", c.Resize.IdentityID, c.Resize.KeyID, c.App.ID, JSON(map[string]any{"claim": claim, "operation": c.Resize.ID})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
