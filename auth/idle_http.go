package auth

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"time"
)

type IdleScope struct{ Project, Environment string }
type IdleHTTPService struct {
	ApplicationID, Project, Environment, Service string
	Revision                                     int64
	Hosts                                        []string
}

var ErrIdleIneligible = cluster.ErrIdleIneligible

// IdleHTTPServices accepts trusted scopes from the embedding product only.
// No workload credentials or configuration values are exposed to the gateway.
func (s *Service) IdleHTTPServices(ctx context.Context, scopes []IdleScope) ([]IdleHTTPService, error) {
	if s.runtime == nil || len(scopes) > 32 {
		return nil, ErrIdleIneligible
	}
	result := []IdleHTTPService{}
	for _, scope := range scopes {
		apps, cursor, err := s.store.ApplicationPage(ctx, scope.Project, scope.Environment, "")
		if err != nil {
			return nil, err
		}
		if cursor != "" {
			return nil, errors.New("idle scope exceeds application bound")
		}
		for _, app := range apps {
			for name := range app.Spec.Services {
				hosts, err := s.runtime.IdleHTTPHosts(ctx, cluster.Target{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Revision: app.Revision, Spec: app.Spec}, name)
				if errors.Is(err, ErrIdleIneligible) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if len(result) >= 128 || len(hosts) > 64 {
					return nil, errors.New("idle HTTP route bound exceeded")
				}
				result = append(result, IdleHTTPService{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Service: name, Revision: app.Revision, Hosts: hosts})
			}
		}
	}
	return result, nil
}
func idleTarget(app store.Application, target IdleHTTPService) (cluster.Target, error) {
	if app.ID != target.ApplicationID || app.Project != target.Project || app.Environment != target.Environment || app.Revision != target.Revision || app.Status != "healthy" {
		return cluster.Target{}, ErrIdleIneligible
	}
	normalized, err := spec.Normalize(app.Spec)
	if err != nil {
		return cluster.Target{}, err
	}
	return cluster.Target{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Revision: app.Revision, Spec: normalized}, nil
}
func (s *Service) changeHTTPIdle(ctx context.Context, target IdleHTTPService, sleep bool) error {
	if s.runtime == nil {
		return ErrIdleIneligible
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.store.WithRuntimeApplication(bounded, target.ApplicationID, func(app store.Application) error {
		current, err := idleTarget(app, target)
		if err != nil {
			return err
		}
		return s.runtime.SetHTTPIdle(bounded, current, target.Service, sleep)
	})
}
func (s *Service) SleepHTTPService(ctx context.Context, target IdleHTTPService) error {
	return s.changeHTTPIdle(ctx, target, true)
}
func (s *Service) WakeHTTPService(ctx context.Context, target IdleHTTPService) error {
	if err := s.changeHTTPIdle(ctx, target, false); err != nil {
		return err
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		app, err := s.store.Application(ctx, target.ApplicationID)
		if err != nil {
			return err
		}
		current, err := idleTarget(app, target)
		if err != nil {
			return err
		}
		ready, err := s.runtime.HTTPAwake(ctx, current, target.Service)
		if err != nil {
			return err
		}
		if ready {
			return s.changeHTTPIdle(ctx, target, false)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
