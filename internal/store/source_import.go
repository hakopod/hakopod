package store

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type InitialSource struct {
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id,omitempty"`
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
	AutoDeploy   bool   `json:"auto_deploy"`
	CommitSHA    string `json:"commit_sha"`
}

func (s *Store) AcceptSourceImport(ctx context.Context, p Principal, project, environment string, next spec.Application, source InitialSource, idem string) (Deployment, error) {
	if !p.IsAdmin() {
		return Deployment{}, ErrForbidden
	}
	if (source.Provider != "github" && source.Provider != "gitlab") || source.Repository == "" || source.Branch == "" || source.Path == "" || source.CommitSHA == "" {
		return Deployment{}, fmt.Errorf("%w: initial source is incomplete", ErrInput)
	}
	return s.accept(ctx, p, project, environment, next, 0, idem, &source, nil)
}
func (s *Store) bindInitialSource(ctx context.Context, tx pgx.Tx, p Principal, a Application, deployment string, source InitialSource) error {
	if a.Revision != 0 || !p.IsAdmin() {
		return ErrForbidden
	}
	grant, err := s.NewSourceGrant(ctx, tx, p, a.Project, a.Environment, a.Name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO application_sources(application_id,provider,repository,branch,path,auto_deploy,grant_id,last_commit,last_deployment,connection_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))", a.ID, source.Provider, source.Repository, source.Branch, source.Path, source.AutoDeploy, grant, source.CommitSHA, deployment, source.ConnectionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'source.import',$3,$4)", p.ID, p.KeyID, a.ID, JSON(source))
	return err
}
