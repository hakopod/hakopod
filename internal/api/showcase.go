package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerShowcaseRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/showcase", s.showcase)
	routes.HandleFunc("POST /api/v1/showcase/remove", s.removeShowcase)
}
func (s *Server) showcase(w http.ResponseWriter, r *http.Request) {
	if !who(r).Allows("deployments:read", "demo", "development", "shop") {
		authFailure(w, store.ErrForbidden)
		return
	}
	item, err := s.Store.Showcase(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	item.Removable = item.Removable && who(r).IsAdmin() && who(r).CredentialType == "browser"
	write(w, 200, item)
}
func (s *Server) removeShowcase(w http.ResponseWriter, r *http.Request) {
	if !who(r).IsAdmin() || who(r).CredentialType != "browser" {
		authFailure(w, store.ErrForbidden)
		return
	}
	var in struct {
		ExpectedRevision            *int64  `json:"expected_revision"`
		ExpectedApplicationID       *string `json:"expected_application_id"`
		ExpectedApplicationRevision *int64  `json:"expected_application_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.ExpectedApplicationRevision == nil || in.ExpectedApplicationID == nil {
		authFailure(w, store.ErrInput)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Store.RequestShowcaseRemoval(ctx, who(r), *in.ExpectedRevision, *in.ExpectedApplicationID, *in.ExpectedApplicationRevision); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, map[string]string{"state": "removing"})
}

// RunShowcase shares normal durable deployment work. Its idle path is one
// bounded row read every ten seconds; it never starts builds or a demo database.
func (s *Server) RunShowcase(ctx context.Context) {
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, 40*time.Second)
		_ = s.processShowcase(attempt)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Server) processShowcase(ctx context.Context) error {
	item, err := s.Store.Showcase(ctx)
	if err != nil || (item.State != "queued" && item.State != "removing") || time.Now().Before(item.NextAttempt) {
		return err
	}
	finish := func(state, message string) error {
		_, err := s.Store.Pool.Exec(ctx, "UPDATE showcase SET state=$1,revision=revision+1,message=$2,attempts=attempts+1,next_attempt_at=now()+interval '30 seconds',updated_at=now() WHERE id=$3 AND revision=$4 AND state=$5", state, message, item.ID, item.Revision, item.State)
		return err
	}
	retry := func(message string) error {
		state := item.State
		if item.Attempts >= 4 {
			state = "blocked"
		}
		return finish(state, message)
	}

	if item.State == "queued" {
		_, err := s.Store.AcceptShowcase(ctx, item)
		switch {
		case err == nil, errors.Is(err, store.ErrShowcaseInactive):
			return nil
		case errors.Is(err, store.ErrUnauthorized), errors.Is(err, store.ErrForbidden):
			return finish("blocked", "The sample owner's deployment permission is unavailable; remove the sample task or review administrator access.")
		case errors.Is(err, store.ErrConflict):
			return finish("skipped", "An application named shop already exists. The bootstrap sample did not change it.")
		default:
			return retry("The sample deployment could not be accepted; check platform database availability.")
		}
	}
	connection, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	var locked bool
	if err = connection.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", store.ShowcaseLock).Scan(&locked); err != nil || !locked {
		connection.Release()
		return err
	}
	appLocked := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlockErr error
		if appLocked {
			_, unlockErr = connection.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,3))", item.ApplicationID)
		}
		if unlockErr == nil {
			_, unlockErr = connection.Exec(cleanup, "SELECT pg_advisory_unlock($1)", store.ShowcaseLock)
		}
		if unlockErr != nil {
			_ = connection.Hijack().Close(cleanup)
		} else {
			connection.Release()
		}
	}()
	item, err = s.Store.Showcase(ctx)
	if err != nil || item.State != "removing" {
		return err
	}
	if item.ApplicationID == "" {
		return finish("removed", "The sample was cancelled before deployment and will not be recreated.")
	}
	if s.Cluster == nil {
		return retry("Sample removal is waiting for Kubernetes connectivity.")
	}
	if err = connection.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", item.ApplicationID).Scan(&appLocked); err != nil || !appLocked {
		return err
	}
	if err = s.Cluster.CheckShowcaseRemoval(ctx, item.ApplicationID); err != nil {
		if errors.Is(err, cluster.ErrSampleHasData) {
			return finish("blocked", err.Error())
		}
		return retry("Sample ownership or Kubernetes availability could not be verified; no namespace was removed.")
	}
	if err = s.Store.DetachShowcase(ctx, item); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return finish("blocked", err.Error())
		}
		return retry("Sample deployment metadata could not be removed; no namespace was removed.")
	}
	if err = s.Cluster.RemoveShowcase(ctx, item.ApplicationID); err != nil {
		return retry("Sample namespace cleanup is pending; inspect connectivity, ownership and namespace finalizers.")
	}
	return finish("removed", "The sample shop was removed and will not be recreated by bootstrap.")
}
