package worker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type actionsDrainSource interface {
	RuntimeBase(context.Context, string, int64) (store.Deployment, error)
	ActionsSlots(context.Context, string, string) ([]store.ActionsSlot, error)
}

const actionsDrainPreparationTimeout = 5 * time.Second

func actionsDrainCandidate(d store.Deployment) bool {
	if d.CancelRequested || d.Revision < 2 || d.ResolvedSpec == nil || d.RecoveryState != "" {
		return false
	}
	for _, service := range d.ResolvedSpec.Services {
		if service.Actions != nil && service.Suspended {
			return true
		}
	}
	return false
}

// Only a pure suspension gets time to finish jobs already using a bounded
// runner lifetime. Other changes retain the ordinary deployment deadline.
func actionsDrainAllowance(ctx context.Context, source actionsDrainSource, d store.Deployment) (time.Duration, error) {
	if !actionsDrainCandidate(d) {
		return 0, nil
	}
	base, err := source.RuntimeBase(ctx, d.ApplicationID, d.Revision-1)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	services := actionsSuspensionServices(d, base)
	var allowance time.Duration
	for _, name := range spec.Names(spec.Application{Services: services}) {
		lifetime, ok := actionsDrainLifetime(services[name])
		if !ok {
			return 0, nil
		}
		allowance = max(allowance, lifetime)
		slots, err := source.ActionsSlots(ctx, d.ApplicationID, name)
		if err != nil {
			return 0, err
		}
		if len(slots) > 20 {
			return 0, fmt.Errorf("Managed Actions slot inventory exceeds its bound")
		}
		for _, slot := range slots {
			if slot.ApplicationID != d.ApplicationID || slot.Service != name {
				return 0, fmt.Errorf("Managed Actions slot belongs to another pool")
			}
			// Older busy slots keep their original timeout after a configuration
			// change. The application's advisory lock prevents new slot creation.
			lifetime, ok = actionsDrainLifetime(slot.Config)
			if !ok {
				return 0, nil
			}
			allowance = max(allowance, lifetime)
		}
	}
	return allowance, nil
}

func actionsSuspensionServices(d, base store.Deployment) map[string]spec.Service {
	if !actionsDrainCandidate(d) || base.ApplicationID != d.ApplicationID || base.Revision != d.Revision-1 {
		return nil
	}
	// Match the runtime-action API's immutable base after successful recovery.
	if base.RecoveryState == "succeeded" && base.RecoverySpec != nil {
		base.Spec = *base.RecoverySpec
		base.ResolvedSpec = base.RecoverySpec
		base.Status = "succeeded"
	}
	if (base.Status != "succeeded" && base.Status != "failed") || base.ResolvedSpec == nil || base.RecoveryState == "running" {
		return nil
	}
	desiredChanges, _ := actionsSuspensionChanges(base.Spec, d.Spec)
	resolvedChanges, services := actionsSuspensionChanges(*base.ResolvedSpec, *d.ResolvedSpec)
	if len(desiredChanges) == 0 || !reflect.DeepEqual(desiredChanges, resolvedChanges) {
		return nil
	}
	return services
}

func actionsSuspensionChanges(before, after spec.Application) ([]string, map[string]spec.Service) {
	previous, err := spec.Normalize(before)
	if err != nil {
		return nil, nil
	}
	next, err := spec.Normalize(after)
	if err != nil {
		return nil, nil
	}
	var changed []string
	suspended := make(map[string]spec.Service)
	for _, name := range spec.Names(next) {
		service := next.Services[name]
		prior, exists := previous.Services[name]
		if service.Actions == nil || !service.Suspended {
			continue
		}
		suspended[name] = service
		if exists && prior.Actions != nil && !prior.Suspended {
			changed = append(changed, name)
			service.Suspended = false
			next.Services[name] = service
		}
	}
	if len(changed) == 0 || !reflect.DeepEqual(previous, next) {
		return nil, nil
	}
	return changed, suspended
}

func actionsDrainLifetime(service spec.Service) (time.Duration, bool) {
	if service.Actions == nil {
		return 0, false
	}
	minutes := service.Actions.TimeoutMinutes
	if minutes == 0 {
		minutes = 60
	}
	if minutes < 5 || minutes > 360 {
		return 0, false
	}
	var grace time.Duration
	switch service.Actions.Provider.Effective() {
	case actions.ProviderGitHub:
		grace = 30 * time.Second
	case actions.ProviderGitLab:
		grace = 4 * time.Minute // Existing manager lifetime and pod termination grace.
	default:
		// A reusable Bitbucket runner has no bounded pod lifetime. Its pool
		// timeout cannot establish a safe deadline for an assigned step.
		return 0, false
	}
	return time.Duration(minutes)*time.Minute + grace, true
}
