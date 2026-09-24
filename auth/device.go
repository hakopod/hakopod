package auth

import (
	"context"

	"github.com/hakopod/hakopod/internal/store"
)

// DeviceScope is an embedding-authorized deployment destination. ID is a durable
// product scope, not a browser preference; the session can never switch scopes.
type DeviceScope = store.DeviceScope

func (s *Service) ConfigureDeviceScopes(scopes func(context.Context, User) ([]DeviceScope, error)) {
	s.store.DeviceScopes = func(ctx context.Context, p store.Principal) ([]store.DeviceScope, error) {
		u, err := s.deviceUser(ctx, p)
		if err != nil || !u.Verified {
			return nil, ErrUnauthorized
		}
		return scopes(ctx, u)
	}
}

func (s *Service) deviceUser(ctx context.Context, p store.Principal) (User, error) {
	u := User{KeyID: p.KeyID, ID: p.ID, Email: p.Email, Name: p.Name, MFAVerified: p.MFAVerified, MFARequired: p.MFARequired}
	if err := s.store.Pool.QueryRow(ctx, "SELECT email_verified FROM identities WHERE id=$1 AND NOT disabled", p.ID).Scan(&u.Verified); err != nil {
		return User{}, ErrUnauthorized
	}
	return u, nil
}

// VerifyCLI is deliberately separate from browser verification. Only sessions
// issued through the configured product consent flow have a durable binding.
func (s *Service) VerifyCLI(ctx context.Context, token string) (User, DeviceScope, error) {
	p, err := s.store.Authenticate(ctx, token)
	if err != nil || p.CredentialType != "cli" || s.store.DeviceScopes == nil {
		return User{}, DeviceScope{}, ErrUnauthorized
	}
	var id string
	if err = s.store.Pool.QueryRow(ctx, "SELECT scope_id FROM device_session_scopes WHERE key_id=$1", p.KeyID).Scan(&id); err != nil {
		return User{}, DeviceScope{}, ErrUnauthorized
	}
	scopes, err := s.store.DeviceScopes(ctx, p)
	if err != nil {
		return User{}, DeviceScope{}, ErrUnauthorized
	}
	for _, scope := range scopes {
		if scope.ID == id && scope.Project == p.Project && scope.Environment == p.Environment {
			u, err := s.deviceUser(ctx, p)
			return u, scope, err
		}
	}
	return User{}, DeviceScope{}, ErrUnauthorized
}
