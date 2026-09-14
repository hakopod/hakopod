package secretprovider

import (
	"context"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

type Repository interface {
	SecretProvider(context.Context, string) (Provider, error)
}

type Service struct {
	Repo          Repository
	CredentialKey []byte
	slots         chan struct{}
	// Only package tests can override transport creation.
	client func(Provider) (*providerClient, error)
}

func NewService(repo Repository, key []byte) *Service {
	return &Service{Repo: repo, CredentialKey: key, slots: make(chan struct{}, 4), client: newProviderClient}
}

// Resolve returns a per-deployment snapshot. Each distinct reference is fetched
// once, with credentials and access checked afresh. No values or tokens are cached
// across deployments or included in returned errors.
func (s *Service) Resolve(parent context.Context, project, environment string, app spec.Application) (map[string]map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ErrUnavailable
	}
	clients := map[string]*providerClient{}
	defer func() {
		for _, c := range clients {
			c.http.CloseIdleConnections()
		}
	}()
	cache := map[spec.SecretRef][]byte{}
	result := map[string]map[string][]byte{}
	total := 0
	for _, name := range spec.Names(app) {
		for key, ref := range spec.SecretReferences(app.Services[name]) {
			if ref.Provider == "" {
				continue
			}
			if !ref.Valid() {
				return nil, ErrUnavailable
			}
			client := clients[ref.Provider]
			if client == nil {
				p, err := s.Repo.SecretProvider(ctx, ref.Provider)
				if err != nil || p.Validate() != nil || !p.Allows(project, environment) {
					return nil, ErrUnavailable
				}
				credentials, err := OpenCredentials(s.CredentialKey, p)
				if err != nil {
					return nil, ErrUnavailable
				}
				client, err = s.client(p)
				if err != nil {
					return nil, ErrUnavailable
				}
				clients[ref.Provider] = client
				if err = client.authenticate(ctx, credentials); err != nil {
					return nil, ErrUnavailable
				}
			}
			value, exists := cache[ref]
			if !exists {
				var err error
				value, err = client.read(ctx, ref.Path, ref.Key)
				if err != nil {
					return nil, ErrUnavailable
				}
				cache[ref] = value
			}
			total += len(value) + len(key)
			if total > 2<<20 {
				return nil, ErrUnavailable
			}
			if result[name] == nil {
				result[name] = map[string][]byte{}
			}
			result[name][key] = value
		}
	}
	return result, nil
}
