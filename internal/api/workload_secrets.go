package api

import (
	"net/http"
	"strings"
)

func (s *Server) registerWorkloadSecretRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/secrets", s.listWorkloadSecrets)
	routes.HandleFunc("POST /api/v1/deployment-secret-requirements", s.secretRequirements)
	routes.HandleFunc("POST /api/v1/secrets/{name}", s.createWorkloadSecret)
	routes.HandleFunc("PUT /api/v1/secrets/{name}", s.putWorkloadSecret)
	routes.HandleFunc("DELETE /api/v1/secrets/{name}", s.deleteWorkloadSecret)
}
func (s *Server) secretScope(w http.ResponseWriter, r *http.Request, permission string) (string, string, string, bool) {
	project, environment := scope(r)
	application := r.URL.Query().Get("application")
	if !validScope(project, environment) || !slug.MatchString(application) {
		problem(w, 400, "invalid_scope", "project, environment and application name are required")
		return "", "", "", false
	}
	if !who(r).Allows(permission, project, environment, application) {
		problem(w, 403, "forbidden", "secret scope is not permitted")
		return "", "", "", false
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes secret storage is unavailable")
		return "", "", "", false
	}
	return project, environment, application, true
}
func (s *Server) listWorkloadSecrets(w http.ResponseWriter, r *http.Request) {
	p, e, a, ok := s.secretScope(w, r, "deployments:read")
	if !ok {
		return
	}
	items, err := s.Cluster.ListWorkloadSecrets(r.Context(), p, e, a)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) putWorkloadSecret(w http.ResponseWriter, r *http.Request) {
	p, e, a, ok := s.secretScope(w, r, "deployments:write")
	if !ok {
		return
	}
	name := r.PathValue("name")
	var in struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slug.MatchString(name) || len(in.Value) == 0 || len(in.Value) > 64<<10 || strings.ContainsRune(in.Value, 0) {
		problem(w, 400, "invalid_secret", "use a valid reference name and a nonempty value up to 64 KiB without NUL")
		return
	}
	if err := s.Cluster.PutWorkloadSecret(r.Context(), p, e, a, name, in.Value); err != nil {
		failure(w, err)
		return
	}
	_, err := s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.write',$3)", who(r).ID, who(r).KeyID, p+"/"+e+"/"+a+"/"+name)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"name": name, "saved": true, "restart_required": true})
}
func (s *Server) deleteWorkloadSecret(w http.ResponseWriter, r *http.Request) {
	p, e, a, ok := s.secretScope(w, r, "deployments:write")
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !slug.MatchString(name) {
		problem(w, 400, "invalid_secret", "invalid reference name")
		return
	}
	required, err := s.Store.ActionsCredentialRequired(r.Context(), p, e, a, name)
	if err != nil {
		failure(w, err)
		return
	}
	if required {
		problem(w, 409, "conflict", "Remove the runner pool and wait for GitHub registration cleanup before deleting its credential.")
		return
	}
	if err := s.Cluster.DeleteWorkloadSecret(r.Context(), p, e, a, name); err != nil {
		failure(w, err)
		return
	}
	_, err = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.delete',$3)", who(r).ID, who(r).KeyID, p+"/"+e+"/"+a+"/"+name)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
