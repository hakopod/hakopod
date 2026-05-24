package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type proxyChange struct {
	Settings        map[string]string `json:"settings"`
	ResourceVersion string            `json:"resource_version"`
	KeyID           string            `json:"key_id,omitempty"`
	Status          string            `json:"status"`
	Error           string            `json:"error"`
	AppliedVersion  string            `json:"applied_version"`
	Attempts        int               `json:"attempts"`
	RetryAt         time.Time         `json:"retry_at"`
}

func (s *Server) registerProxyRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/settings/haproxy", s.getProxy)
	routes.HandleFunc("PATCH /api/v1/settings/haproxy", s.setProxy)
}
func (s *Server) getProxy(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is unavailable")
		return
	}
	observed, err := s.Cluster.ProxyConfiguration(r.Context())
	if err != nil {
		problem(w, 503, "proxy_unavailable", err.Error())
		return
	}
	row, err := s.Store.RuntimeResource(r.Context(), "proxy", "", "", "haproxy")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	var change proxyChange
	if row.Revision > 0 && json.Unmarshal(row.Metadata, &change) != nil {
		problem(w, 503, "unavailable", "stored HAProxy configuration is invalid")
		return
	}
	change.KeyID = ""
	drift := false
	if change.Status == "applied" {
		for k, v := range change.Settings {
			drift = drift || observed.Settings[k] != v
		}
	}
	write(w, 200, map[string]any{"observed": observed, "revision": row.Revision, "change": change, "drift": drift})
}
func (s *Server) setProxy(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Settings                map[string]string `json:"settings"`
		ExpectedRevision        *int64            `json:"expected_revision"`
		ExpectedResourceVersion string            `json:"expected_resource_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.ExpectedResourceVersion == "" {
		problem(w, 400, "review_required", "provide the reviewed database revision and Kubernetes resource version")
		return
	}
	previous, previousErr := s.Store.RuntimeResource(r.Context(), "proxy", "", "", "haproxy")
	if previousErr == nil {
		var pending proxyChange
		if json.Unmarshal(previous.Metadata, &pending) != nil {
			problem(w, 503, "unavailable", "stored HAProxy configuration is invalid")
			return
		}
		if pending.Status == "queued" {
			problem(w, 409, "proxy_pending", "wait for the accepted HAProxy change to finish")
			return
		}
	} else if !errors.Is(previousErr, pgx.ErrNoRows) {
		failure(w, previousErr)
		return
	}
	if err := cluster.ValidateProxySettings(in.Settings); err != nil {
		problem(w, 400, "invalid_proxy", err.Error())
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is unavailable")
		return
	}
	current, err := s.Cluster.ProxyConfiguration(r.Context())
	if err != nil {
		problem(w, 503, "proxy_unavailable", err.Error())
		return
	}
	if current.ResourceVersion != in.ExpectedResourceVersion {
		problem(w, 409, "proxy_conflict", "HAProxy configuration changed; refresh before saving")
		return
	}
	change := proxyChange{Settings: in.Settings, ResourceVersion: in.ExpectedResourceVersion, KeyID: who(r).KeyID, Status: "queued"}
	row, err := s.Store.PutRuntimeResource(r.Context(), who(r), "proxy", "", "", "haproxy", *in.ExpectedRevision, change)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]any{"revision": row.Revision, "status": "queued"})
}

// RunPlatform reconciles small installation configuration records, with bounded
// retries. Existing application traffic never passes through this worker.
func (s *Server) RunPlatform(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileProxy(ctx)
		}
	}
}
func (s *Server) reconcileProxy(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if s.Cluster == nil {
		return
	}
	row, err := s.Store.RuntimeResource(ctx, "proxy", "", "", "haproxy")
	if err != nil {
		return
	}
	var change proxyChange
	if json.Unmarshal(row.Metadata, &change) != nil || change.Status != "queued" || time.Now().Before(change.RetryAt) {
		return
	}
	principal, err := s.Store.KeyPrincipal(ctx, change.KeyID)
	if err == nil && !principal.IsAdmin() {
		err = store.ErrForbidden
	}
	if err == nil {
		change.AppliedVersion, err = s.Cluster.ApplyProxyConfiguration(ctx, change.Settings, change.ResourceVersion, row.Revision)
	}
	if parent.Err() != nil {
		return
	}
	change.Attempts++
	change.Status = "applied"
	change.Error = ""
	if err != nil {
		change.Status = "failed"
		change.Error = err.Error()
		if len(change.Error) > 512 {
			change.Error = "HAProxy configuration could not be applied"
		}
		if change.Attempts < 5 && !errors.Is(err, store.ErrForbidden) && !errors.Is(err, store.ErrUnauthorized) && !errors.Is(err, cluster.ErrProxyConflict) {
			change.Status = "queued"
			change.RetryAt = time.Now().Add(time.Duration(5*(1<<change.Attempts)) * time.Second)
		}
	}
	finish, done := context.WithTimeout(parent, 5*time.Second)
	defer done()
	_, _ = s.Store.Pool.Exec(finish, "UPDATE runtime_resources SET metadata=$1,updated_at=now() WHERE kind='proxy' AND project='' AND environment='' AND name='haproxy' AND revision=$2", store.JSON(change), row.Revision)
}
