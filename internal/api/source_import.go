package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type sourceImportInput struct {
	Project      string `json:"project"`
	Environment  string `json:"environment"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
	AutoDeploy   bool   `json:"auto_deploy"`
}

func (in sourceImportInput) binding() sourceBinding {
	return sourceBinding{ConnectionID: selectedGitConnection(in.Provider, in.ConnectionID), Provider: in.Provider, Repository: in.Repository, Branch: in.Branch, Path: in.Path, AutoDeploy: in.AutoDeploy}
}

type sourceImportReview struct {
	ConnectionRevision int64             `json:"connection_revision,omitempty"`
	Input              sourceImportInput `json:"source"`
	Commit             string            `json:"commit"`
	Hash               string            `json:"hash"`
	Key                string            `json:"key"`
	Expires            int64             `json:"expires"`
}

func sourceContentHash(a spec.Application) string {
	sum := sha256.Sum256(store.JSON(a))
	return hex.EncodeToString(sum[:])
}

func (review sourceImportReview) initialSource() store.InitialSource {
	return store.InitialSource{ExpectedConnectionRevision: review.ConnectionRevision, ConnectionID: review.Input.ConnectionID, Provider: review.Input.Provider, Repository: review.Input.Repository, Branch: review.Input.Branch, Path: review.Input.Path, AutoDeploy: review.Input.AutoDeploy, CommitSHA: review.Commit}
}
func (s *Server) signSourceReview(body []byte) string {
	mac := hmac.New(sha256.New, s.authEncryptionKey())
	mac.Write([]byte("hakopod-source-import-v1\x00"))
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) registerSourceImportRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/sources/plan", s.planSourceImport)
	m.HandleFunc("POST /api/v1/sources/deploy", s.deploySourceImport)
}
func (s *Server) sourceImportConfigured(w http.ResponseWriter, r *http.Request, in sourceImportInput) bool {
	if !gitManager(w, r) {
		return false
	}
	if len(s.authEncryptionKey()) != 32 {
		problem(w, 503, "review_key_unavailable", "Configure the installation authentication encryption key before importing sources")
		return false
	}
	if !validScope(in.Project, in.Environment) || !validSource(in.binding()) {
		problem(w, 400, "invalid_source", "Choose a project, environment, provider, repository, branch and relative TOML path")
		return false
	}
	if err := s.validateGitConnection(r.Context(), in.Provider, in.ConnectionID); err != nil {
		authFailure(w, err)
		return false
	}
	if in.AutoDeploy {
		data, err := s.gitWebhookCredentials(r.Context(), in.Provider, in.ConnectionID)
		if err != nil || len(data["webhook-secret"]) < 32 {
			problem(w, 400, "source_not_configured", "Configure this provider's signed webhook before enabling automatic deployment")
			return false
		}
	}
	return true
}
func (s *Server) planSourceImport(w http.ResponseWriter, r *http.Request) {
	var in sourceImportInput
	if !decode(w, r, &in) {
		return
	}
	in.Repository = normalizeSourceRepository(in.Repository)
	in.ConnectionID = selectedGitConnection(in.Provider, in.ConnectionID)
	if !s.sourceImportConfigured(w, r, in) {
		return
	}
	next, commit, err := s.sourceSpec(r.Context(), in.binding(), "")
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	if _, err = s.Store.FindApplication(r.Context(), in.Project, in.Environment, next.Name); err == nil {
		problem(w, 409, "application_exists", "This application already exists. Connect its repository from the application's source page.")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}

	if !s.validateDeliveryPlan(w, r, in.Project, in.Environment, next, nil) {
		return
	}
	expires := time.Now().Add(15 * time.Minute)
	connection, err := s.readGitConnection(r.Context(), in.ConnectionID)
	if err != nil {
		authFailure(w, err)
		return
	}
	review := sourceImportReview{ConnectionRevision: connection.Revision, Input: in, Commit: commit, Hash: sourceContentHash(next), Key: who(r).KeyID, Expires: expires.Unix()}
	warnings := deliveryWarnings(r, next)
	if warnings == nil {
		warnings = []string{}
	}
	write(w, 200, map[string]any{"application_id": "", "expected_revision": 0, "spec": next, "changes": spec.Diff(nil, next), "warnings": warnings, "resource_profiles": spec.Profiles, "commit_sha": commit, "review_token": s.signSourceReview(store.JSON(review)), "expires_at": expires, "source": in})
}
func (s *Server) deploySourceImport(w http.ResponseWriter, r *http.Request) {
	if !gitManager(w, r) {
		return
	}
	var in struct {
		ReviewToken string `json:"review_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.ReviewToken) > 8192 || len(s.authEncryptionKey()) != 32 {
		problem(w, 400, "review_required", "Review the repository configuration before importing")
		return
	}
	parts := strings.Split(in.ReviewToken, ".")
	if len(parts) != 2 {
		problem(w, 400, "review_required", "Review the repository configuration before importing")
		return
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !hmac.Equal([]byte(in.ReviewToken), []byte(s.signSourceReview(body))) {
		problem(w, 400, "review_required", "Source review is invalid; review the repository again")
		return
	}
	var review sourceImportReview
	if json.Unmarshal(body, &review) != nil || review.Key != who(r).KeyID {
		problem(w, 409, "review_expired", "Source review expired or belongs to another session; review again")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		problem(w, 400, "idempotency_key_required", "Idempotency-Key must contain 8–128 characters")
		return
	}
	p, err := s.freshRuntimePrincipal(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !p.CanBindGit(review.Input.Project, review.Input.Environment) {
		failure(w, store.ErrForbidden)
		return
	}
	// A committed request remains recoverable after its review expires or the
	// provider becomes unavailable. Accept still verifies the exact request hash.
	var acceptedID string
	err = s.Store.Pool.QueryRow(r.Context(), "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", p.ID, key).Scan(&acceptedID)
	if err == nil {
		existing, readErr := s.Store.Deployment(r.Context(), acceptedID)
		if readErr != nil {
			failure(w, readErr)
			return
		}
		if sourceContentHash(existing.Spec) != review.Hash {
			failure(w, store.ErrConflict)
			return
		}
		d, replayErr := s.Store.AcceptSourceImport(r.Context(), p, review.Input.Project, review.Input.Environment, existing.Spec, review.initialSource(), key)
		if replayErr != nil {
			failure(w, replayErr)
			return
		}
		write(w, 202, d)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	if review.Expires < time.Now().Unix() || review.Expires > time.Now().Add(16*time.Minute).Unix() {
		problem(w, 409, "review_expired", "Source review expired; review again")
		return
	}
	if !s.sourceImportConfigured(w, r, review.Input) {
		return
	}
	if review.ConnectionRevision > 0 {
		connection, e := s.readGitConnection(r.Context(), selectedGitConnection(review.Input.Provider, review.Input.ConnectionID))
		if e != nil {
			authFailure(w, e)
			return
		}
		if connection.Revision != review.ConnectionRevision {
			problem(w, 409, "git_connection_changed", "Git connection changed; review the repository again")
			return
		}
	}
	next, _, err := s.sourceSpec(r.Context(), review.Input.binding(), review.Commit)
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	if sourceContentHash(next) != review.Hash {
		problem(w, 409, "source_changed", "Repository content no longer matches the reviewed commit")
		return
	}
	p, err = s.freshRuntimePrincipal(r)
	if err != nil {
		failure(w, err)
		return
	}
	connection, err := s.readGitConnection(r.Context(), selectedGitConnection(review.Input.Provider, review.Input.ConnectionID))
	if err != nil {
		authFailure(w, err)
		return
	}
	if !connection.Enabled {
		problem(w, 409, "git_connection_disabled", "Git connection was disabled; enable it and review the repository again")
		return
	}
	if review.ConnectionRevision > 0 && connection.Revision != review.ConnectionRevision {
		problem(w, 409, "git_connection_changed", "Git connection changed; review the repository again")
		return
	}
	d, err := s.Store.AcceptSourceImport(r.Context(), p, review.Input.Project, review.Input.Environment, next, review.initialSource(), key)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, d)
}
