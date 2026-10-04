package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type HostActionsGrant = store.HostActionsGrant

// EnsureHostActionsKey is reserved for a trusted host-local Cloud embedding.
// Authorize the operating-system peer and current internal workspace before
// calling it. Persist the returned raw key privately before serving requests.
// Never publish this issuer as a browser or HTTP administration endpoint.
func (s *Service) EnsureHostActionsKey(ctx context.Context, grant HostActionsGrant, existingRaw string) (AutomationKey, string, error) {
	if s.config.DeploymentMode != cluster.DeploymentManagedCloud {
		return AutomationKey{}, "", store.ErrForbidden
	}
	return s.store.EnsureHostActionsKey(ctx, grant, existingRaw)
}

func (s *Service) VerifyHostActionsKey(ctx context.Context, grant HostActionsGrant, raw string) (User, error) {
	if s.config.DeploymentMode != cluster.DeploymentManagedCloud {
		return User{}, store.ErrForbidden
	}
	p, err := s.store.VerifyHostActionsKey(ctx, grant, raw)
	if err != nil {
		return User{}, err
	}
	// This is a machine identity, never an operator browser session.
	return User{ID: p.ID, KeyID: p.KeyID, Email: p.Email, Name: p.Name, Verified: true}, nil
}

var hostActionsOperationKey = regexp.MustCompile(`^recovery:[a-f0-9]{32}:[0-9]{1,2}:(stop|resume|cancel|rollback)(:[1-3])?$`)
var hostActionsDeployment = regexp.MustCompile(`^/deployments/([a-f0-9]{32})(/cancel)?$`)

// ServeHostActionsRuntime dispatches only recovery lifecycle requests using a
// real durable application-scoped key. The caller owns peer-UID verification,
// read-only grants, and current Cloud workspace ownership checks. The canonical
// API still authenticates the key and persists it for worker reauthorization.
func (s *Service) ServeHostActionsRuntime(handler http.Handler, w http.ResponseWriter, r *http.Request, grant HostActionsGrant, raw string) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func(status int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "host_recovery_unavailable", "message": "Host Actions recovery is unavailable for this request"}})
	}
	if handler == nil || r.Header.Get("X-Hakopod-Workspace") != grant.Workspace ||
		r.Header.Get("X-Hakopod-Recovery-Application") != grant.ApplicationID || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		deny(http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.VerifyHostActionsKey(ctx, grant, raw); err != nil {
		deny(http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == r.URL.Path {
		deny(http.StatusForbidden)
		return
	}
	app := "/applications/" + grant.ApplicationID
	allowed := r.Method == "GET" && (path == app || path == app+"/actions")
	if r.Method == "POST" && hostActionsOperationKey.MatchString(r.Header.Get("Idempotency-Key")) {
		allowed = path == app+"/rollback" || path == app+"/services/"+grant.Service+"/stop" || path == app+"/services/"+grant.Service+"/resume"
	}
	var application string
	var lookup error
	if match := hostActionsDeployment.FindStringSubmatch(path); match != nil &&
		(r.Method == "GET" && match[2] == "" || r.Method == "POST" && match[2] == "/cancel" && hostActionsOperationKey.MatchString(r.Header.Get("Idempotency-Key"))) {
		lookup = s.store.Pool.QueryRow(ctx, "SELECT application_id FROM deployments WHERE id=$1", match[1]).Scan(&application)
		allowed = lookup == nil && application == grant.ApplicationID
	} else if key, ok := strings.CutPrefix(path, "/idempotency/"); ok && r.Method == "GET" && hostActionsOperationKey.MatchString(key) {
		lookup = s.store.Pool.QueryRow(ctx, "SELECT application_id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", grant.OwnerIdentity, key).Scan(&application)
		allowed = lookup == nil && application == grant.ApplicationID
	}
	if errors.Is(lookup, pgx.ErrNoRows) {
		deny(http.StatusNotFound)
		return
	}
	if lookup != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		deny(http.StatusForbidden)
		return
	}
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	request.Header.Del("Cookie")
	request.Header.Set("Authorization", "Bearer "+raw)
	handler.ServeHTTP(w, request)
}
