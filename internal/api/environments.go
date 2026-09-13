package api

import (
	"net/http"

	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("project")
	if !validScope(project, in.Name) {
		problem(w, 400, "invalid_environment", "environment names use 1–40 lowercase letters, digits and hyphens")
		return
	}
	p := who(r)
	if !p.CanManageProject(project) || !p.Allows("deployments:write", project, in.Name, "") {
		failure(w, store.ErrForbidden)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var exists string
	if err = tx.QueryRow(r.Context(), "SELECT name FROM projects WHERE name=$1 FOR UPDATE", project).Scan(&exists); err != nil {
		failure(w, err)
		return
	}
	var count int
	var duplicate bool
	if err = tx.QueryRow(r.Context(), "SELECT count(*),COALESCE(bool_or(name=$2),false) FROM environments WHERE project=$1", project, in.Name).Scan(&count, &duplicate); err != nil {
		failure(w, err)
		return
	}
	if duplicate {
		problem(w, 409, "environment_exists", "This project already has an environment with that name.")
		return
	}
	if count >= 32 {
		problem(w, 400, "environment_limit", "A project supports at most 32 environments.")
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO environments(project,name) VALUES($1,$2)", project, in.Name); err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'environment.create',$3)", p.ID, p.KeyID, project+"/"+in.Name)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, map[string]string{"project": project, "name": in.Name})
}
