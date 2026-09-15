package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InitialPreview struct {
	ID             string `json:"id"`
	ParentID       string `json:"parent_id"`
	ParentRevision int64  `json:"parent_revision"`
	Name           string `json:"name"`
	Branch         string `json:"branch"`
	TTLHours       int    `json:"ttl_hours"`
}
type Preview struct {
	ID            string     `json:"id"`
	ParentID      string     `json:"parent_id"`
	ApplicationID *string    `json:"application_id"`
	Project       string     `json:"project"`
	Environment   string     `json:"environment"`
	Name          string     `json:"name"`
	Branch        string     `json:"branch"`
	State         string     `json:"state"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	DeletedAt     *time.Time `json:"deleted_at"`
	CleanupError  string     `json:"cleanup_error,omitempty"`
}

const previewCols = "id,parent_id,application_id,project,environment,name,branch,state,created_at,expires_at,deleted_at,cleanup_error"

func scanPreview(row scanner) (Preview, error) {
	var p Preview
	err := row.Scan(&p.ID, &p.ParentID, &p.ApplicationID, &p.Project, &p.Environment, &p.Name, &p.Branch, &p.State, &p.CreatedAt, &p.ExpiresAt, &p.DeletedAt, &p.CleanupError)
	return p, err
}

var previewName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (s *Store) AcceptPreview(ctx context.Context, p Principal, parent Application, next spec.Application, in InitialPreview, idem string) (Deployment, error) {
	if !canManagePreviews(p, parent.Project, parent.Environment) {
		return Deployment{}, ErrForbidden
	}
	if !previewName.MatchString(in.Name) || len(in.Branch) > 200 || in.TTLHours < 1 || in.TTLHours > 72 {
		return Deployment{}, fmt.Errorf("%w: preview name, branch or lifetime is invalid; use 1–72 hours", ErrInput)
	}
	in.ParentID = parent.ID
	digest := sha256.Sum256([]byte(p.ID + "\x00" + idem))
	in.ID = hex.EncodeToString(digest[:16])
	next.Name = "preview-" + in.ID
	normalized, err := spec.Normalize(next)
	if err != nil {
		return Deployment{}, err
	}
	if err = spec.ValidatePreview(normalized); err != nil {
		return Deployment{}, err
	}
	return s.accept(ctx, p, parent.Project, parent.Environment, normalized, 0, idem, nil, nil, &in)
}
func (s *Store) Preview(ctx context.Context, id string) (Preview, error) {
	return scanPreview(s.Pool.QueryRow(ctx, "SELECT "+previewCols+" FROM previews WHERE id=$1", id))
}
func (s *Store) ApplicationPreviews(ctx context.Context, parent, cursor string) ([]Preview, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+previewCols+" FROM previews WHERE parent_id=$1 AND id>$2 ORDER BY id LIMIT 51", parent, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Preview{}
	for rows.Next() {
		p, err := scanPreview(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
func (s *Store) RequestPreviewDeletion(ctx context.Context, p Principal, id, confirmation string) error {
	preview, err := s.Preview(ctx, id)
	if err != nil {
		return err
	}
	if !canManagePreviews(p, preview.Project, preview.Environment) {
		return ErrForbidden
	}
	if confirmation != preview.Name {
		return fmt.Errorf("%w: confirm the preview name", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "UPDATE previews SET state='deleting' WHERE id=$1 AND state='active'", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE deployments SET cancel_requested=true WHERE application_id=$1 AND status IN ('queued','running')", preview.ApplicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'preview.delete-request',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func validatePreviewAcceptance(ctx context.Context, tx pgx.Tx, a Application, next spec.Application, initial *InitialPreview) error {
	if initial != nil {
		parent, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR SHARE", initial.ParentID))
		if err != nil {
			return err
		}
		if parent.Project != a.Project || parent.Environment != a.Environment || parent.Revision != initial.ParentRevision {
			return fmt.Errorf("%w: parent application changed; review the preview again", ErrConflict)
		}
		var nested bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM previews WHERE application_id=$1)", parent.ID).Scan(&nested); err != nil {
			return err
		}
		if nested {
			return fmt.Errorf("%w: previews cannot have previews", ErrInput)
		}
		// Serialize preview capacity across different names in the same project.
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,44))", a.Project); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM previews WHERE project=$1 AND state<>'deleted'", a.Project).Scan(&count); err != nil {
			return err
		}
		if count >= 3 {
			return fmt.Errorf("%w: at most three active previews per project; delete a preview first", ErrConflict)
		}
		return spec.ValidatePreview(next)
	}
	var state string
	var expires time.Time
	err := tx.QueryRow(ctx, "SELECT state,expires_at FROM previews WHERE application_id=$1 FOR SHARE", a.ID).Scan(&state, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "active" || !expires.After(time.Now()) {
		return fmt.Errorf("%w: preview expired or is being deleted", ErrConflict)
	}
	return spec.ValidatePreview(next)
}

type PreviewClaim struct {
	mu      sync.Mutex
	conn    *pgxpool.Conn
	Preview Preview
	App     Application
}

func (c *PreviewClaim) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	return c.conn.Ping(ctx)
}
func (c *PreviewClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		releaseClaimConnection(c.conn)
		c.conn = nil
	}
}
func (s *Store) ClaimPreviewCleanup(ctx context.Context) (*PreviewClaim, error) {
	rows, err := s.Pool.Query(ctx, "SELECT application_id FROM previews WHERE application_id IS NOT NULL AND (state='deleting' OR state='active' AND expires_at<=now()) ORDER BY cleanup_attempted_at NULLS FIRST,expires_at LIMIT 16")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", id).Scan(&locked); err != nil || !locked {
			conn.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		p, err := scanPreview(conn.QueryRow(ctx, "UPDATE previews SET state='deleting',cleanup_attempted_at=now() WHERE application_id=$1 AND (state='deleting' OR state='active' AND expires_at<=now()) RETURNING "+previewCols, id))
		if err != nil {
			releaseClaimConnection(conn)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		a, err := s.Application(ctx, id)
		if err != nil {
			releaseClaimConnection(conn)
			return nil, err
		}
		if _, err = conn.Exec(ctx, "UPDATE deployments SET status='cancelled',cancel_requested=true,error='Preview expired or was deleted',finished_at=now() WHERE application_id=$1 AND status IN ('queued','running')", id); err != nil {
			releaseClaimConnection(conn)
			return nil, err
		}
		return &PreviewClaim{conn: conn, Preview: p, App: a}, nil
	}
	return nil, nil
}
func (c *PreviewClaim) Finish(ctx context.Context, cleanupError string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if cleanupError != "" {
		_, err := c.conn.Exec(ctx, "UPDATE previews SET cleanup_error=$2 WHERE id=$1", c.Preview.ID, cleanupError)
		return err
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = deleteApplicationMetadata(ctx, tx, c.App); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE previews SET state='deleted',deleted_at=now(),cleanup_error='' WHERE id=$1", c.Preview.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource,metadata) VALUES('system','preview.cleaned',$1,$2)", c.Preview.ID, JSON(map[string]any{"application_id": c.App.ID, "project": c.App.Project})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func canManagePreviews(p Principal, project, environment string) bool {
	return p.Allows("deployments:write", project, environment, "") && (p.CanManageProject(project) || p.CredentialType == "machine" && p.Project == project && p.Environment == environment && p.Application == "")
}
