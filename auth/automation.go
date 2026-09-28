package auth

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

type AutomationKey = store.Key
type AutomationKeyInput = store.KeyInput
type AutomationGrant struct {
	Workspace   string
	Binding     string
	Project     string
	Environment string
	Permissions []string
}

// Automation keys are issued by a trusted embedding after it authorizes the
// browser and workspace. They are not browser or legacy CLI sessions. The
// embedding must recheck the binding, current role and policy on every request.
func (s *Service) VerifyAutomation(ctx context.Context, token string) (User, AutomationGrant, error) {
	p, err := s.store.Authenticate(ctx, token)
	if err != nil || p.CredentialType != "machine" || p.Application != "" || p.Project == "" || p.Environment == "" || slices.Contains(p.Permissions, "admin") {
		return User{}, AutomationGrant{}, ErrUnauthorized
	}
	g := AutomationGrant{Project: p.Project, Environment: p.Environment, Permissions: slices.Clone(p.Permissions)}
	if err = s.store.Pool.QueryRow(ctx, "SELECT scope_id,binding FROM automation_key_scopes WHERE key_id=$1", p.KeyID).Scan(&g.Workspace, &g.Binding); err != nil {
		return User{}, AutomationGrant{}, ErrUnauthorized
	}
	u, err := s.deviceUser(ctx, p)
	return u, g, err
}

// CreateAutomationKey cannot issue installation administrators or application
// credentials. Only a live browser identity can call the embedding issuer.
func (s *Service) CreateAutomationKey(ctx context.Context, account User, grant AutomationGrant, in AutomationKeyInput, rotate string) (AutomationKey, string, error) {
	p, err := s.store.KeyPrincipal(ctx, account.KeyID)
	if err != nil || p.ID != account.ID || p.CredentialType != "browser" || p.MFARequired {
		return AutomationKey{}, "", ErrUnauthorized
	}
	if grant.Workspace == "" || grant.Binding == "" || in.Project != grant.Project || in.Environment != grant.Environment || in.Application != "" || !slices.Contains(in.Permissions, "deployments:read") {
		return AutomationKey{}, "", fmt.Errorf("%w: select one workspace and include deployments:read", store.ErrInput)
	}
	for _, permission := range in.Permissions {
		if permission == "admin" || !slices.Contains(grant.Permissions, permission) {
			return AutomationKey{}, "", store.ErrForbidden
		}
	}
	return s.store.CreateBoundKey(ctx, p, in, grant.Workspace, grant.Binding, rotate)
}

func (s *Service) AutomationKeys(ctx context.Context, workspace string) ([]AutomationKey, error) {
	return s.store.BoundKeys(ctx, workspace)
}

func (s *Service) RevokeAutomationKey(ctx context.Context, account User, workspace, id string) error {
	p, err := s.store.KeyPrincipal(ctx, account.KeyID)
	if err != nil || p.ID != account.ID || p.CredentialType != "browser" || p.MFARequired {
		return ErrUnauthorized
	}
	return s.store.RevokeBoundKey(ctx, p, workspace, id)
}

// Used by embedding tests and callers without duplicating key expiry limits.
const MaxAutomationKeyLifetime = 90 * 24 * time.Hour
