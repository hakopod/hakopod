package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

type managedPlatformTrustProvider interface {
	ManagedPlatformTrust(context.Context, store.ManagedPlatform) (database.PublicTrust, error)
}

func (s *Server) managedPlatformTrust(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.ManagedPlatform(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	provider, ok := s.ManagedPlatformRuntime.(managedPlatformTrustProvider)
	if !ok {
		problem(w, http.StatusServiceUnavailable, "managed_platform_trust_unavailable", "Platform certificate trust is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	trust, err := provider.ManagedPlatformTrust(ctx, item)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "managed_platform_trust_unavailable", "The platform's current certificate authority could not be verified.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, trust)
}
