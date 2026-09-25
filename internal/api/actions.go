package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type actionsRuntime interface {
	ActionsAvailable(context.Context) error
	ActionsCredential(context.Context, cluster.Target, string) (string, error)
	ActionsPodPhase(context.Context, cluster.Target, string) (string, error)
	DeleteActionsPod(context.Context, cluster.Target, string) (bool, error)
	DeleteActionsConfig(context.Context, cluster.Target, string) error
	SaveActionsConfig(context.Context, cluster.Target, string, string, string) error
	StartActionsPod(context.Context, cluster.Target, string, string, spec.Service) error
	HasActionsConfig(context.Context, cluster.Target, string) (bool, error)
}

func (s *Server) actionRuntime() actionsRuntime {
	if s.actionsTestRuntime != nil {
		return s.actionsTestRuntime
	}
	return s.Cluster
}

func (s *Server) ConfigureActions() {
	s.Cluster.ConfigureActions(func(ctx context.Context, t cluster.Target) error {
		app := store.Application{ID: t.ApplicationID, Project: t.Project, Environment: t.Environment, Name: t.Spec.Name}
		if err := s.Store.SyncActions(ctx, app, t.Spec, t.Revision); err != nil {
			return err
		}
		pools, err := s.Store.ActionsPools(ctx, t.ApplicationID)
		if err != nil {
			return err
		}
		for _, p := range pools {
			if err = s.reconcileActionsPool(ctx, t, p); err != nil {
				_ = s.Store.ActionsMessage(ctx, p, safeActionsError(err))
				return fmt.Errorf("%s", safeActionsError(err))
			}
		}
		return nil
	}, s.observeActions)
}

func actionsTarget(p store.ActionsPool) cluster.Target {
	return cluster.Target{ApplicationID: p.ApplicationID, Project: p.Project, Environment: p.Environment, Revision: p.Revision, Spec: spec.Application{SchemaVersion: 1, Name: p.ApplicationName, Services: map[string]spec.Service{p.Service: p.Config}, Networks: map[string]spec.Network{"default": {}}}}
}

type runnerProvider interface {
	Register(context.Context, actions.Target, string, []string) (actions.Registration, error)
	Get(context.Context, actions.Target, int64) (actions.Runner, error)
	Find(context.Context, actions.Target, string) (*actions.Runner, error)
	Delete(context.Context, actions.Target, int64) error
}

func actionsFence(ctx context.Context, t cluster.Target) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.BeforeStep != nil {
		return t.BeforeStep(ctx)
	}
	return nil
}

func (s *Server) RunActions(ctx context.Context) {
	afterApp, afterService := "", ""
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		pools, err := s.Store.ActionsPoolsPage(ctx, "", afterApp, afterService)
		if err != nil {
			continue
		}
		if len(pools) < 200 {
			afterApp, afterService = "", ""
		} else {
			last := pools[len(pools)-1]
			afterApp, afterService = last.ApplicationID, last.Service
		}
		for _, p := range pools {
			if ctx.Err() != nil {
				return
			}
			step, cancel := context.WithTimeout(ctx, 45*time.Second)
			claim, err := s.Store.ClaimActions(step, p.ApplicationID)
			if err != nil || claim == nil {
				cancel()
				continue
			}
			// Refresh after acquiring the application lock; a deployment may have
			// changed the pool since the inventory page was read.
			current, readErr := s.Store.ActionsPools(step, p.ApplicationID)
			err = readErr
			if err == nil {
				for _, fresh := range current {
					if fresh.Service == p.Service {
						p = fresh
						t := actionsTarget(p)
						t.BeforeStep = claim.Check
						err = s.reconcileActionsPool(step, t, p)
						break
					}
				}
			}
			if err != nil {
				_ = s.Store.ActionsMessage(step, p, safeActionsError(err))
			}
			claim.Release()
			cancel()
		}
	}
}

// Provider and Kubernetes errors may contain a request body. Persist only
// bounded public diagnostics, never the single-job config or provider token.
func safeActionsError(err error) string {
	var status *actions.StatusError
	if errors.As(err, &status) {
		return status.Error() + "; verify the selected scope and runner group, and organization Self-hosted runners or repository Administration read/write permission"
	}
	return "Runner reconciliation is waiting for GitHub, its scoped credential, or the sandbox. Cleanup will retry automatically."
}
func actionsNotFound(err error) bool {
	var status *actions.StatusError
	return errors.As(err, &status) && status.Status == 404
}

func (s *Server) runnerClient(ctx context.Context, t cluster.Target, config spec.Service) (runnerProvider, error) {
	if config.Actions == nil {
		return nil, fmt.Errorf("runner configuration is missing")
	}
	token, err := s.actionRuntime().ActionsCredential(ctx, t, config.Actions.Credential)
	if err != nil {
		return nil, err
	}
	if s.actionsClient != nil {
		return s.actionsClient(token)
	}
	return actions.New(token)
}
func sameActionsConfig(a, b spec.Service) bool {
	a.Replicas = 1
	b.Replicas = 1
	a.Suspended = false
	b.Suspended = false
	return reflect.DeepEqual(a, b)
}

func (s *Server) cleanupActionsSlot(ctx context.Context, t cluster.Target, p store.ActionsPool, v store.ActionsSlot, c runnerProvider, force bool) (bool, error) {
	if err := actionsFence(ctx, t); err != nil {
		return false, err
	}
	if err := s.Store.UpdateActionsSlot(ctx, v.ID, v.RunnerID, "cleanup"); err != nil {
		return false, err
	}
	if force {
		if _, err := s.actionRuntime().DeleteActionsPod(ctx, t, v.ID); err != nil {
			return false, err
		}
	}
	id := v.RunnerID
	if id == 0 {
		found, err := c.Find(ctx, v.Config.Actions.Target(), "hakopod-"+v.ID)
		if err != nil {
			return false, err
		}
		if found != nil {
			id = found.ID
		}
	}
	if id > 0 {
		if err := actionsFence(ctx, t); err != nil {
			return false, err
		}
		if err := c.Delete(ctx, v.Config.Actions.Target(), id); err != nil {
			return false, err
		}
	}
	gone, err := s.actionRuntime().DeleteActionsPod(ctx, t, v.ID)
	if err != nil || !gone {
		return false, err
	}
	if err = s.actionRuntime().DeleteActionsConfig(ctx, t, v.ID); err != nil {
		return false, err
	}
	return true, s.Store.DeleteActionsSlot(ctx, v.ID)
}

func (s *Server) reconcileActionsPool(ctx context.Context, t cluster.Target, p store.ActionsPool) error {
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if err != nil {
		return err
	}
	accessErr := s.Store.RequireActions(ctx, p.Project, p.Environment)
	if accessErr != nil && !errors.Is(accessErr, store.ErrLicenseRequired) {
		return accessErr
	}
	allowed := accessErr == nil
	desired := int(p.Config.Replicas)
	if p.Removed || p.Config.Suspended || !allowed {
		desired = 0
	}
	remaining := len(slots)
	retained := 0
	for _, v := range slots {
		if err := actionsFence(ctx, t); err != nil {
			return err
		}
		if p.Removed {
			if _, err := s.actionRuntime().DeleteActionsPod(ctx, t, v.ID); err != nil {
				return err
			}
		}
		client, err := s.runnerClient(ctx, t, v.Config)
		if err != nil {
			return err
		}
		phase, err := s.actionRuntime().ActionsPodPhase(ctx, t, v.ID)
		if err != nil {
			return err
		}
		retire := p.Removed || p.Config.Suspended || !allowed || !sameActionsConfig(v.Config, p.Config) || retained >= desired
		// An intent survives a lost registration response. Recover and remove it
		// by its persisted unique name before creating another registration.
		if v.Phase == "intent" || v.Phase == "cleanup" || phase == "Succeeded" || phase == "Failed" || phase == "deleting" || (phase == "missing" && v.Phase != "starting") {
			gone, err := s.cleanupActionsSlot(ctx, t, p, v, client, p.Removed)
			if err != nil {
				return err
			}
			if gone {
				remaining--
			}
			continue
		}
		runner, err := client.Get(ctx, v.Config.Actions.Target(), v.RunnerID)
		if actionsNotFound(err) {
			gone, e := s.cleanupActionsSlot(ctx, t, p, v, client, true)
			if e != nil {
				return e
			}
			if gone {
				remaining--
			}
			continue
		}
		if err != nil {
			return err
		}
		if retire && !runner.Busy {
			gone, err := s.cleanupActionsSlot(ctx, t, p, v, client, false)
			if err != nil {
				return err
			}
			if gone {
				remaining--
			}
			continue
		}
		if p.Removed {
			gone, err := s.cleanupActionsSlot(ctx, t, p, v, client, true)
			if err != nil {
				return err
			}
			if gone {
				remaining--
			}
			continue
		}
		if phase == "missing" {
			exists, e := s.actionRuntime().HasActionsConfig(ctx, t, v.ID)
			if e != nil {
				return e
			}
			if !exists {
				_, e = s.cleanupActionsSlot(ctx, t, p, v, client, true)
				return e
			}
			if retire {
				continue
			}
			if err = s.actionRuntime().StartActionsPod(ctx, t, p.Service, v.ID, v.Config); err != nil {
				return err
			}
		}
		state := "starting"
		if runner.Status == "online" {
			state = "online"
		}
		if runner.Busy {
			state = "busy"
		}
		if err = s.Store.UpdateActionsSlot(ctx, v.ID, v.RunnerID, state); err != nil {
			return err
		}
		if !retire {
			retained++
		}
	}
	if p.Removed {
		if remaining == 0 {
			return s.Store.DeleteRetiredActionsPool(ctx, p)
		}
		return s.Store.ActionsMessage(ctx, p, "Removing runners and GitHub registrations")
	}
	if !allowed {
		return s.Store.ActionsMessage(ctx, p, "Pro access is required. Busy jobs finish; no new runners are started.")
	}
	if p.Config.Suspended {
		return s.Store.ActionsMessage(ctx, p, "Paused. Busy jobs finish before their runners are removed.")
	}
	if err = s.actionRuntime().ActionsAvailable(ctx); err != nil {
		return err
	}
	// At most one provider registration per pool per pass. Draining and
	// cleanup-pending slots continue to count against the replica budget.
	if remaining < desired {
		if err = s.Store.RequireActions(ctx, p.Project, p.Environment); err != nil {
			return err
		}
		client, err := s.runnerClient(ctx, t, p.Config)
		if err != nil {
			return err
		}
		if err = actionsFence(ctx, t); err != nil {
			return err
		}
		v, err := s.Store.NewActionsSlot(ctx, p)
		if err != nil {
			return err
		}
		if err = actionsFence(ctx, t); err != nil {
			return err
		}
		registered, err := client.Register(ctx, p.Config.Actions.Target(), "hakopod-"+v.ID, p.Config.Actions.Labels)
		if err != nil {
			return err
		}
		if err = actionsFence(ctx, t); err != nil {
			return err
		}
		if err = s.Store.UpdateActionsSlot(ctx, v.ID, registered.Runner.ID, "starting"); err != nil {
			return err
		}
		if err = s.actionRuntime().SaveActionsConfig(ctx, t, p.Service, v.ID, registered.EncodedConfig); err != nil {
			return err
		}
		if err = s.actionRuntime().StartActionsPod(ctx, t, p.Service, v.ID, p.Config); err != nil {
			return err
		}
	}
	return s.Store.ActionsMessage(ctx, p, "")
}

func (s *Server) observeActions(ctx context.Context, t cluster.Target, name string, config spec.Service) (cluster.ServiceStatus, error) {
	result := cluster.ServiceStatus{Name: name, Status: "deploying", Desired: config.Replicas, Image: config.Image}
	if config.Suspended {
		result.Desired = 0
	}
	pools, err := s.Store.ActionsPools(ctx, t.ApplicationID)
	if err != nil {
		return result, err
	}
	found := false
	for _, p := range pools {
		if p.Service != name {
			continue
		}
		found = true
		result.Message = p.Message
		slots, e := s.Store.ActionsSlots(ctx, t.ApplicationID, name)
		if e != nil {
			return result, e
		}
		for _, v := range slots {
			if sameActionsConfig(v.Config, config) && (v.Phase == "online" || v.Phase == "busy") && time.Since(v.UpdatedAt) < 2*time.Minute {
				result.Ready++
			}
		}
		if config.Suspended && len(slots) == 0 {
			result.Status = "stopped"
		}
		if !config.Suspended && result.Ready >= result.Desired && p.Message == "" {
			result.Status = "ready"
		}
	}
	if !found {
		result.Status = "missing"
	}
	return result, nil
}

func (s *Server) actionsStatus(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	pools, err := s.Store.ActionsPools(r.Context(), a.ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	type item struct {
		Pool  store.ActionsPool   `json:"pool"`
		Slots []store.ActionsSlot `json:"slots"`
	}
	out := []item{}
	for _, p := range pools {
		slots, e := s.Store.ActionsSlots(r.Context(), a.ID, p.Service)
		if e != nil {
			authFailure(w, e)
			return
		}
		out = append(out, item{p, slots})
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) actionsCapabilities(w http.ResponseWriter, r *http.Request) {
	project, env := r.URL.Query().Get("project"), r.URL.Query().Get("environment")
	if !validScope(project, env) || !who(r).Allows("deployments:read", project, env, "") {
		authFailure(w, store.ErrForbidden)
		return
	}
	err := s.Store.RequireActions(r.Context(), project, env)
	if err != nil && !errors.Is(err, store.ErrLicenseRequired) {
		authFailure(w, err)
		return
	}
	runtime := s.actionRuntime().ActionsAvailable(r.Context())
	message := ""
	if runtime != nil {
		message = "The Managed Actions sandbox is not ready on this installation."
	}
	write(w, 200, map[string]any{"licensed": err == nil, "runtime_ready": runtime == nil, "message": message, "runner_image": spec.ActionsRunnerImage})
}
