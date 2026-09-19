package auth

import (
	"context"
	"github.com/hakopod/hakopod/internal/idlehttp"
)

type IdleScope = idlehttp.IdleScope
type IdleHTTPService = idlehttp.IdleHTTPService

var ErrIdleIneligible = idlehttp.ErrIdleIneligible

func (s *Service) IdleHTTPServices(ctx context.Context, scopes []IdleScope) ([]IdleHTTPService, error) {
	return (&idlehttp.Service{Store: s.store, Cluster: s.runtime}).IdleHTTPServices(ctx, scopes)
}
func (s *Service) SleepHTTPService(ctx context.Context, target IdleHTTPService) error {
	return (&idlehttp.Service{Store: s.store, Cluster: s.runtime}).SleepHTTPService(ctx, target)
}
func (s *Service) WakeHTTPService(ctx context.Context, target IdleHTTPService) error {
	return (&idlehttp.Service{Store: s.store, Cluster: s.runtime}).WakeHTTPService(ctx, target)
}
