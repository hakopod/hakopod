package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Accept uses one transaction for revision allocation, immutable specification,
// queue entry and audit record. Advisory locking also covers first creation.
func (s *Store) Accept(ctx context.Context, p Principal, project, env string, next spec.Application, expected int64, idem string, seedResolved ...spec.Application) (Deployment, error) {
	return s.accept(ctx, p, project, env, next, expected, idem, nil, nil, nil, seedResolved...)
}

func (s *Store) accept(ctx context.Context, p Principal, project, env string, next spec.Application, expected int64, idem string, initialSource *InitialSource, initialShowcase *Showcase, initialPreview *InitialPreview, seedResolved ...spec.Application) (Deployment, error) {
	if len(idem) < 8 || len(idem) > 128 {
		return Deployment{}, errors.New("Idempotency-Key must contain 8–128 characters")
	}
	if !p.Allows("deployments:write", project, env, next.Name) {
		return Deployment{}, ErrForbidden
	}
	if len(seedResolved) > 1 {
		return Deployment{}, errors.New("at most one trusted resolved revision may be seeded")
	}
	var seed *spec.Application
	var resolvedJSON any
	if len(seedResolved) == 1 {
		resolved, err := spec.Normalize(seedResolved[0])
		if err != nil {
			return Deployment{}, fmt.Errorf("invalid trusted resolved revision: %w", err)
		}
		if resolved.Name != next.Name {
			return Deployment{}, errors.New("resolved revision belongs to a different application")
		}
		for _, service := range resolved.Services {
			if !strings.Contains(service.Image, "@sha256:") {
				return Deployment{}, errors.New("trusted resolved revisions must contain immutable image digests")
			}
		}
		seed = &resolved
		resolvedJSON = JSON(resolved)
	}
	showcaseID := ""
	if initialShowcase != nil {
		showcaseID = initialShowcase.ID
	}
	hash := sha256.Sum256(JSON(struct {
		Project, Environment string
		Spec                 spec.Application
		Expected             int64
		// Omission preserves hashes for ordinary pre-seeding deployment calls;
		// presence distinguishes rollback intent even when the desired spec is identical.
		SeedResolved   *spec.Application `json:",omitempty"`
		InitialSource  *InitialSource    `json:",omitempty"`
		InitialPreview *InitialPreview   `json:",omitempty"`
		ShowcaseID     string            `json:",omitempty"`
	}{project, env, next, expected, seed, initialSource, initialPreview, showcaseID}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback(ctx)
	if initialShowcase != nil {
		if err = s.lockShowcaseAcceptance(ctx, tx, p, project, env, next, *initialShowcase); err != nil {
			return Deployment{}, err
		}
	}
	// Same identity and idempotency key serialize even when a caller changes scope.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,1))", p.ID+":"+idem); err != nil {
		return Deployment{}, err
	}
	var existingID string
	var oldHash []byte
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&existingID, &oldHash)
	if err == nil {
		if !bytes.Equal(hash[:], oldHash) {
			return Deployment{}, fmt.Errorf("%w: idempotency key reused with different input", ErrConflict)
		}
		d, e := scanDep(tx.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE id=$1", existingID))
		if e != nil {
			return d, e
		}
		if initialShowcase != nil {
			if e = s.recordShowcaseAcceptance(ctx, tx, showcaseID, d.ApplicationID, d.ID); e != nil {
				return Deployment{}, e
			}
			if e = tx.Commit(ctx); e != nil {
				return Deployment{}, e
			}
		}
		return d, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, err
	}
	// Serialize network membership acceptance with grants being edited or removed.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,31))", "virtual-network:"+project+":"+env); err != nil {
		return Deployment{}, err
	}
	if _, err = resolveVirtualNetworks(ctx, tx, project, env, next); err != nil {
		return Deployment{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", project+":"+env+":"+next.Name); err != nil {
		return Deployment{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, env).Scan(&exists); err != nil {
		return Deployment{}, err
	}
	if !exists {
		return Deployment{}, errors.New("project/environment does not exist")
	}
	a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE project=$1 AND environment=$2 AND name=$3 FOR UPDATE", project, env, next.Name))
	if errors.Is(err, pgx.ErrNoRows) {
		var retired bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM retired_resource_names WHERE kind='application' AND project=$1 AND environment=$2 AND name=$3)`, project, env, next.Name).Scan(&retired); err != nil {
			return Deployment{}, err
		}
		if retired {
			return Deployment{}, fmt.Errorf("%w: this application name was deleted; choose a new name", ErrConflict)
		}
		if len(next.Services) == 0 {
			return Deployment{}, errors.New("a new application needs at least one service")
		}
		// Per-application locks do not serialize different new names. Lock the
		// environment row before counting so the hard listing limit cannot be
		// exceeded by concurrent first deployments of separate applications.
		var lockedEnvironment string
		if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", project, env).Scan(&lockedEnvironment); err != nil {
			return Deployment{}, err
		}
		var applicationCount int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM applications WHERE project=$1 AND environment=$2", project, env).Scan(&applicationCount); err != nil {
			return Deployment{}, err
		}
		limit := 200
		if s.ApplicationLimit != nil {
			limit, err = s.ApplicationLimit(ctx, project, env)
			if err != nil {
				return Deployment{}, err
			}
			if limit < 1 || limit > 200 {
				return Deployment{}, fmt.Errorf("invalid application limit")
			}
		}
		if applicationCount >= limit {
			return Deployment{}, fmt.Errorf("this environment supports at most %d applications", limit)
		}
		if expected != 0 {
			return Deployment{}, fmt.Errorf("%w: application has revision 0", ErrConflict)
		}
		a = Application{ID: NewID(), Name: next.Name, Project: project, Environment: env}
		if _, err = tx.Exec(ctx, "INSERT INTO applications(id,project,environment,name,spec) VALUES($1,$2,$3,$4,$5)", a.ID, project, env, next.Name, JSON(next)); err != nil {
			return Deployment{}, err
		}
	} else if err != nil {
		return Deployment{}, err
	}
	if err = validatePreviewAcceptance(ctx, tx, a, next, initialPreview); err != nil {
		return Deployment{}, err
	}
	if a.Revision != expected {
		return Deployment{}, fmt.Errorf("%w: expected %d, current revision is %d; plan again", ErrConflict, expected, a.Revision)
	}
	if s.ValidateDeployment == nil && (spec.HasDeliveryCapabilities(next) || seed != nil && spec.HasDeliveryCapabilities(*seed)) {
		return Deployment{}, errors.New("public TCP, backend certificates and AWS identities require a configured runtime validator")
	}
	if s.ValidateDeployment != nil {
		if err = s.ValidateDeployment(ctx, a, next); err != nil {
			return Deployment{}, err
		}
		if seed != nil {
			if err = s.ValidateDeployment(ctx, a, *seed); err != nil {
				return Deployment{}, err
			}
		}
	}
	if err = s.reserveDomains(ctx, tx, a.ID, next); err != nil {
		return Deployment{}, err
	}
	var pending int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE application_id=$1 AND status IN ('queued','running')", a.ID).Scan(&pending); err != nil {
		return Deployment{}, err
	}
	if pending >= 20 {
		return Deployment{}, errors.New("application queue is full (20 pending releases); wait or cancel a queued deployment")
	}
	id := NewID()
	revision := a.Revision + 1
	if _, err = tx.Exec(ctx, "INSERT INTO deployments(id,application_id,identity_id,key_id,idempotency_key,request_hash,revision,spec,resolved_spec) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, a.ID, p.ID, p.KeyID, idem, hash[:], revision, JSON(next), resolvedJSON); err != nil {
		return Deployment{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE applications SET revision=$2,spec=$3,observed='{}'::jsonb,updated_at=now(),status='queued' WHERE id=$1", a.ID, revision, JSON(next)); err != nil {
		return Deployment{}, err
	}
	if initialPreview != nil {
		_, err = tx.Exec(ctx, "INSERT INTO previews(id,parent_id,application_id,project,environment,name,branch,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()+make_interval(hours=>$8))", initialPreview.ID, initialPreview.ParentID, a.ID, a.Project, a.Environment, initialPreview.Name, initialPreview.Branch, initialPreview.TTLHours)
		if err != nil {
			return Deployment{}, err
		}
	}
	if initialSource != nil {
		if err = s.bindInitialSource(ctx, tx, p, a, id, *initialSource); err != nil {
			return Deployment{}, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'deployment.accept',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"revision": revision, "application_id": a.ID})); err != nil {
		return Deployment{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message) VALUES($1,'queued','Release accepted into the durable application queue')", id); err != nil {
		return Deployment{}, err
	}
	if initialShowcase != nil {
		if err = s.recordShowcaseAcceptance(ctx, tx, showcaseID, a.ID, id); err != nil {
			return Deployment{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Deployment{}, err
	}
	return s.Deployment(ctx, id)
}

// Claim holds a session advisory lock until Finish/Release. Process loss closes
// the connection; the next worker can resume the same durable running operation.
// No transaction is held while waiting for Kubernetes.
type Claim struct {
	mu         sync.Mutex
	Conn       *pgxpool.Conn
	Deployment Deployment
	App        Application
}

var ErrClaimLost = errors.New("durable application claim is no longer held")

func (s *Store) Claim(ctx context.Context) (*Claim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT d.id,d.application_id FROM deployments d WHERE d.status IN ('queued','running') AND NOT EXISTS(SELECT 1 FROM previews p WHERE p.application_id=d.application_id AND (p.state<>'active' OR p.expires_at<=now())) AND NOT EXISTS (SELECT 1 FROM deployments older WHERE older.application_id=d.application_id AND older.revision<d.revision AND older.status IN ('queued','running')) ORDER BY d.created_at LIMIT 16`)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, app string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.app); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range candidates {
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", v.app).Scan(&locked)
		if err != nil || !locked {
			conn.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		release := func() { releaseClaimConnection(conn) }
		d, err := scanDep(conn.QueryRow(ctx, "UPDATE deployments SET status='running',started_at=COALESCE(started_at,now()),attempts=attempts+1 WHERE id=$1 AND status IN ('queued','running') RETURNING "+depCols, v.id))
		if err != nil {
			release()
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		a, err := s.Application(ctx, v.app)
		if err != nil {
			release()
			return nil, err
		}
		return &Claim{Conn: conn, Deployment: d, App: a}, nil
	}
	return nil, nil
}
func (c *Claim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil {
		return
	}
	releaseClaimConnection(c.Conn)
	c.Conn = nil
}

func releaseClaimConnection(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock_all()"); err != nil {
		// Never return a possibly locked live connection to the shared pool.
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

func (c *Claim) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil || c.Conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Conn.Ping(ctx); err != nil {
		return err
	}
	var expired bool
	if err := c.Conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM previews WHERE application_id=$1 AND (state<>'active' OR expires_at<=now()))", c.App.ID).Scan(&expired); err != nil {
		return err
	}
	if expired {
		return ErrClaimLost
	}
	return nil
}

// SetResolved returns the immutable winner, including when a previous attempt
// already persisted it. The worker must deploy this returned value, never its
// uncommitted local tag-resolution result.
func (c *Claim) SetResolved(ctx context.Context, app spec.Application) (spec.Application, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil || c.Conn.Conn().IsClosed() {
		return spec.Application{}, ErrClaimLost
	}
	var persisted spec.Application
	err := c.Conn.QueryRow(ctx, "UPDATE deployments SET resolved_spec=COALESCE(resolved_spec,$2) WHERE id=$1 AND application_id=$3 AND status='running' RETURNING resolved_spec", c.Deployment.ID, JSON(app), c.Deployment.ApplicationID).Scan(&persisted)
	if errors.Is(err, pgx.ErrNoRows) {
		return spec.Application{}, fmt.Errorf("%w: operation is no longer running", ErrClaimLost)
	}
	return persisted, err
}

// Finish commits using the exact PostgreSQL session that owns the application
// lock. A lost backend cannot fall back to an unrelated pooled connection and
// finalize a result after a replacement worker has claimed the operation.
func (c *Claim) Finish(ctx context.Context, status, message string, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn == nil || c.Conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	tx, err := c.Conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := finishTransaction(ctx, tx, c.Deployment, status, message, result); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) LastHealthy(ctx context.Context, app string, before int64) (*spec.Application, error) {
	var a spec.Application
	err := s.Pool.QueryRow(ctx, "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision<$2 AND status='succeeded' AND resolved_spec IS NOT NULL ORDER BY revision DESC LIMIT 1", app, before).Scan(&a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func finishTransaction(ctx context.Context, tx pgx.Tx, d Deployment, status, message string, result any) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return errors.New("operation can only finish succeeded, failed or cancelled")
	}
	query := "UPDATE deployments SET status=$2,error=$3,result=$4,finished_at=now() WHERE id=$1 AND application_id=$5 AND status='running'"
	tag, err := tx.Exec(ctx, query, d.ID, status, message, JSON(result), d.ApplicationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: operation is no longer running or available", ErrClaimLost)
	}
	appStatus := status
	if status == "failed" && d.RecoveryState == "succeeded" {
		appStatus = "recovered"
	}
	if status == "succeeded" {
		appStatus = "healthy"
		if len(d.Spec.Services) == 0 {
			appStatus = "empty"
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE applications SET observed=CASE WHEN revision=$3 THEN $2 ELSE observed END,status=CASE WHEN revision=$3 THEN $4 ELSE 'queued' END,updated_at=now() WHERE id=$1", d.ApplicationID, JSON(result), d.Revision, appStatus); err != nil {
		return err
	}
	return nil
}
