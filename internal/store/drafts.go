package store

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// draftEntryLimit bounds the attributed history kept with one staged draft.
const draftEntryLimit = 50

// Draft is the one shared set of staged, not yet deployed changes for an
// application. It records the revision it started from so a newer deployment
// can be merged in three ways instead of being overwritten.
type Draft struct {
	ApplicationID string                 `json:"application_id"`
	BaseRevision  int64                  `json:"base_revision"`
	Revision      int64                  `json:"revision"`
	BaseSpec      spec.Application       `json:"-"`
	Spec          spec.Application       `json:"spec"`
	Provenance    map[string]SourceBuild `json:"-"`
	Entries       []DraftEntry           `json:"entries"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// DraftEntry attributes one staged edit to the person who made it.
type DraftEntry struct {
	IdentityID string        `json:"identity_id"`
	Author     string        `json:"author"`
	At         time.Time     `json:"at"`
	Changes    []spec.Change `json:"changes"`
}

func (s *Store) Draft(ctx context.Context, applicationID string) (Draft, error) {
	var d Draft
	err := s.Pool.QueryRow(ctx, `SELECT application_id,base_revision,revision,base_spec,spec,provenance,entries,created_at,updated_at
		FROM application_drafts WHERE application_id=$1`, applicationID).
		Scan(&d.ApplicationID, &d.BaseRevision, &d.Revision, &d.BaseSpec, &d.Spec, &d.Provenance, &d.Entries, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// StageDraft records next as the draft, measured against base (the revision
// and specification next was edited from). expected is the draft revision the
// caller saw, 0 when it saw none; a mismatch is ErrConflict.
func (s *Store) StageDraft(ctx context.Context, p Principal, app Application, baseRevision int64, base spec.Application, next spec.Application, provenance map[string]SourceBuild, expected int64, changes []spec.Change) (Draft, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	current, err := lockedDraft(ctx, tx, app.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, err
	}
	if (exists && current.Revision != expected) || (!exists && expected != 0) {
		return Draft{}, ErrConflict
	}
	if !exists {
		current = Draft{ApplicationID: app.ID, Provenance: map[string]SourceBuild{}}
	}
	current.BaseRevision, current.BaseSpec = baseRevision, base
	if current.Provenance == nil {
		current.Provenance = map[string]SourceBuild{}
	}
	maps.Copy(current.Provenance, provenance)
	for name := range current.Provenance {
		if _, ok := next.Services[name]; !ok {
			delete(current.Provenance, name)
		}
	}
	current.Entries = append(current.Entries, DraftEntry{IdentityID: p.ID, Author: p.Name, At: time.Now().UTC(), Changes: changes})
	if len(current.Entries) > draftEntryLimit {
		current.Entries = current.Entries[len(current.Entries)-draftEntryLimit:]
	}
	err = tx.QueryRow(ctx, `INSERT INTO application_drafts(application_id,base_revision,revision,base_spec,spec,provenance,entries)
		VALUES($1,$2,1,$3,$4,$5,$6)
		ON CONFLICT(application_id) DO UPDATE SET revision=application_drafts.revision+1,spec=EXCLUDED.spec,
			base_revision=EXCLUDED.base_revision,base_spec=EXCLUDED.base_spec,
			provenance=EXCLUDED.provenance,entries=EXCLUDED.entries,updated_at=now()
		RETURNING revision,created_at,updated_at`,
		app.ID, current.BaseRevision, JSON(current.BaseSpec), JSON(next), JSON(current.Provenance), JSON(current.Entries)).
		Scan(&current.Revision, &current.CreatedAt, &current.UpdatedAt)
	if err != nil {
		return Draft{}, err
	}
	current.Spec = next
	return current, tx.Commit(ctx)
}

// DiscardDraft removes the draft when its revision still matches expected.
// Passing 0 removes whatever draft exists; callers use it after a deploy.
func (s *Store) DiscardDraft(ctx context.Context, applicationID string, expected int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM application_drafts WHERE application_id=$1 AND ($2=0 OR revision=$2)`, applicationID, expected)
	if err != nil {
		return err
	}
	if expected != 0 && tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func lockedDraft(ctx context.Context, tx pgx.Tx, applicationID string) (Draft, error) {
	var d Draft
	err := tx.QueryRow(ctx, `SELECT application_id,base_revision,revision,base_spec,spec,provenance,entries,created_at,updated_at
		FROM application_drafts WHERE application_id=$1 FOR UPDATE`, applicationID).
		Scan(&d.ApplicationID, &d.BaseRevision, &d.Revision, &d.BaseSpec, &d.Spec, &d.Provenance, &d.Entries, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}
