package api

import (
	"context"
	"errors"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// The read credential is required by retained history. Management and cache
// credentials may already have been removed after cleanup; mask those while
// available without retaining additional provider access just for old logs.
func (s *Server) actionsLogCredentials(ctx context.Context, pool store.ActionsPool, original *spec.Actions) ([]string, error) {
	if original == nil || original.EffectiveJobsCredential() == "" {
		return nil, errors.New("job log configuration is unavailable")
	}
	pool.Config.Actions = original
	target := actionsTarget(pool)
	read := original.EffectiveJobsCredential()
	value, err := s.actionRuntime().ActionsCredential(ctx, target, read)
	if err != nil || value == "" {
		return nil, errors.New("job log read credential is unavailable")
	}
	values := []string{value}
	references := []string{original.Credential}
	if original.Cache != nil {
		references = append(references, original.Cache.Credential)
	}
	seen := map[string]bool{read: true}
	for _, reference := range references {
		if reference == "" || seen[reference] {
			continue
		}
		seen[reference] = true
		step, cancel := context.WithTimeout(ctx, 2*time.Second)
		value, err := s.actionRuntime().ActionsCredential(step, target, reference)
		cancel()
		if err == nil && value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}
