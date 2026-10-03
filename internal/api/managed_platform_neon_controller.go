package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type neonControllerAuthority interface {
	ValidNeonControllerToken(string, int64, string) bool
	NotifyNeonController(context.Context, *store.Store, string, int64, *managedplatform.NeonAttachNotification, *managedplatform.NeonSafekeeperNotification) (bool, error)
}

func (s *Server) registerNeonController(mux *http.ServeMux) {
	authority, ok := s.ManagedPlatformRuntime.(neonControllerAuthority)
	if !ok {
		return
	}
	concurrent := make(chan struct{}, 4)
	handle := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		platformID := r.PathValue("platform")
		revision, err := managedplatform.NeonControllerRevision(r.PathValue("revision"))
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err != nil || token == r.Header.Get("Authorization") || !authority.ValidNeonControllerToken(platformID, revision, token) {
			neonProxyProblem(w, http.StatusUnauthorized, "controller authorization failed")
			return
		}
		select {
		case concurrent <- struct{}{}:
			defer func() { <-concurrent }()
		default:
			neonProxyProblem(w, http.StatusTooManyRequests, "controller notifications are busy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
		if r.URL.RawQuery != "" || r.ContentLength > 16384 {
			neonProxyProblem(w, http.StatusBadRequest, "invalid controller notification")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var attach *managedplatform.NeonAttachNotification
		var safekeepers *managedplatform.NeonSafekeeperNotification
		if r.PathValue("notification") == "notify-attach" {
			attach = &managedplatform.NeonAttachNotification{}
			err = decoder.Decode(attach)
		} else if r.PathValue("notification") == "notify-safekeepers" {
			safekeepers = &managedplatform.NeonSafekeeperNotification{}
			err = decoder.Decode(safekeepers)
		} else {
			http.NotFound(w, r)
			return
		}
		if err != nil || decoder.Decode(new(any)) != io.EOF {
			neonProxyProblem(w, http.StatusBadRequest, "invalid controller notification")
			return
		}
		applied, err := authority.NotifyNeonController(ctx, s.Store, platformID, revision, attach, safekeepers)
		if errors.Is(err, store.ErrInput) {
			neonProxyProblem(w, http.StatusBadRequest, "invalid controller notification")
			return
		}
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrConflict) {
			neonProxyProblem(w, http.StatusForbidden, "controller binding changed")
			return
		}
		if err != nil || !applied {
			// The pinned safekeeper hook does not resend a same-generation 423.
			// A 503 keeps it retrying until every running compute is observed.
			neonProxyProblem(w, http.StatusServiceUnavailable, "controller configuration is pending")
			return
		}
		write(w, http.StatusOK, map[string]bool{"applied": true})
	}
	mux.HandleFunc("PUT /api/v1/internal/neon/storage-controller/{platform}/{revision}/{notification}", handle)
}
