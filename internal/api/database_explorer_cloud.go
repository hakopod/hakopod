package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/hakopod/hakopod/internal/store"
)

type explorerCloudIdentity struct {
	Version     int      `json:"version"`
	Workspace   string   `json:"workspace"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Actor       string   `json:"actor"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Session     string   `json:"session"`
	Permissions []string `json:"permissions"`
}

func (s *Server) explorerCloudPrincipal(ctx context.Context, p store.Principal, t *explorerTarget, ticket string) (store.Principal, explorerCloudIdentity, error) {
	var identity explorerCloudIdentity
	if p.CredentialType != "machine" || p.RuntimeScoped || t.CloudWorkspace == "" || len(ticket) != 64 {
		return p, identity, store.ErrForbidden
	}
	url, err := explorerURL(os.Getenv("HAKOPOD_EXPLORER_CLOUD_AUTHORITY_URL"))
	if err != nil {
		return p, identity, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url.String(), nil)
	if err != nil {
		return p, identity, err
	}
	req.Header.Set("Authorization", "Bearer "+ticket)
	resp, err := s.explorerRuntime.client.Do(req)
	if err != nil {
		return p, identity, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(data) > 8192 || resp.StatusCode != 200 {
		return p, identity, store.ErrForbidden
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil || identity.Version != 1 || identity.Workspace != t.CloudWorkspace || identity.Project != t.Project || identity.Environment != t.Environment || identity.Actor == "" || len(identity.Actor) > 256 || len(identity.Session) != 64 {
		return p, identity, errors.New("invalid Cloud explorer authority")
	}
	p, err = scopedRuntimePrincipal(p, RuntimeScope{AllowMachine: true, Identity: p.ID, Project: t.Project, Environment: t.Environment, Permissions: identity.Permissions, Authorize: func(context.Context) error { return nil }})
	return p, identity, err
}
