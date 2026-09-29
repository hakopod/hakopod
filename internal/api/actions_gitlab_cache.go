package api

import (
	"context"
	"errors"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
)

func (s *Server) gitlabCacheCredentials(ctx context.Context, t cluster.Target, config spec.Service, runtime cluster.GitLabActionsRuntime) (*actions.GitLabCacheCredentials, error) {
	if config.Actions == nil || config.Actions.Cache == nil {
		return nil, nil
	}
	reference := config.Actions.Cache.Credential
	if runtime.Cache == nil || reference == "" || !(spec.SecretRef{Ref: reference}).Valid() {
		return nil, errors.New("GitLab cache needs installation-approved storage and an application secret")
	}
	if err := actionsFence(ctx, t); err != nil {
		return nil, err
	}
	value, err := s.actionRuntime().ActionsCredential(ctx, t, reference)
	if err != nil {
		return nil, errors.New("GitLab cache credential is unavailable")
	}
	var credentials actions.GitLabCacheCredentials
	if len(value) > 16<<10 || decodeGitLabPrivate([]byte(value), &credentials) != nil || credentials.Validate() != nil {
		return nil, errors.New("GitLab cache secret must contain access_key, secret_key and optional session_token JSON fields")
	}
	return &credentials, nil
}
