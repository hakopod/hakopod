package api

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// This snapshot deliberately contains no provider token. It has its own
// authenticated domain and survives slot cleanup for historical job transport.
type gitlabIntentSnapshot struct {
	Version     int                          `json:"version"`
	Project     string                       `json:"project"`
	Environment string                       `json:"environment"`
	Application string                       `json:"application"`
	Config      *spec.Actions                `json:"config"`
	Runtime     cluster.GitLabActionsRuntime `json:"runtime"`
}

func gitlabIntentBinding(slot store.ActionsSlot) (actions.RegistrationBinding, error) {
	binding, err := slot.RegistrationBinding()
	if err != nil {
		return binding, err
	}
	binding.RunnerID = ""
	return binding, nil
}

func (s *Server) sealGitLabIntent(t cluster.Target, slot store.ActionsSlot, runtime cluster.GitLabActionsRuntime) ([]byte, error) {
	if err := cluster.ValidateGitLabActionsRuntime(runtime); err != nil {
		return nil, err
	}
	binding, err := gitlabIntentBinding(slot)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(gitlabIntentSnapshot{Version: 1, Project: t.Project, Environment: t.Environment, Application: t.Spec.Name, Config: slot.Config.Actions, Runtime: runtime})
	if err != nil {
		return nil, errors.New("GitLab original trust cannot be encoded")
	}
	defer clear(payload)
	key := s.authEncryptionKey()
	defer clear(key)
	return actions.SealProviderIntent(key, binding, payload)
}

func (s *Server) openGitLabIntent(t cluster.Target, slot store.ActionsSlot) (cluster.GitLabActionsRuntime, error) {
	var saved gitlabIntentSnapshot
	binding, err := gitlabIntentBinding(slot)
	if err != nil {
		return saved.Runtime, err
	}
	key := s.authEncryptionKey()
	defer clear(key)
	data, err := actions.OpenProviderIntent(key, binding, slot.EncryptedProviderIntent)
	if err != nil {
		return saved.Runtime, err
	}
	defer clear(data)
	target, err := binding.Target.Canonical()
	if err != nil || decodeGitLabPrivate(data, &saved) != nil || saved.Version != 1 || saved.Project != t.Project || saved.Environment != t.Environment || saved.Application != t.Spec.Name || !reflect.DeepEqual(saved.Config, slot.Config.Actions) || saved.Runtime.TransportPolicy.CoordinatorURL != target.GitLab.URL {
		return saved.Runtime, errors.New("GitLab original trust does not match its recorded scope")
	}
	if err := cluster.ValidateGitLabActionsRuntime(saved.Runtime); err != nil {
		return saved.Runtime, err
	}
	return saved.Runtime, nil
}

func (s *Server) gitlabOriginalRuntime(t cluster.Target, slot store.ActionsSlot) (*cluster.GitLabActionsRuntime, error) {
	if len(slot.EncryptedProviderIntent) == 0 && s.actionsGitLabClient != nil {
		// Old in-process fixtures exercise lifecycle failures independently of
		// installation trust. This exception is unreachable through HTTP/startup.
		return nil, nil
	}
	runtime, err := s.openGitLabIntent(t, slot)
	return &runtime, err
}

func (s *Server) gitlabHistoryRuntime(p store.ActionsPool, job store.ActionsJob) (*cluster.GitLabActionsRuntime, error) {
	if job.ProviderConfig == nil || !reflect.DeepEqual(job.ProviderConfig, p.Config.Actions) {
		return nil, errors.New("GitLab history configuration changed")
	}
	slot := store.ActionsSlot{ID: job.SlotID, ApplicationID: p.ApplicationID, Service: p.Service, Config: p.Config, EncryptedProviderIntent: job.EncryptedProviderIntent}
	return s.gitlabOriginalRuntime(actionsTarget(p), slot)
}

func (s *Server) installationGitLabClient(ctx context.Context, t cluster.Target, service string, config spec.Service, token string, original *cluster.GitLabActionsRuntime) (*actions.GitLabClient, error) {
	if !s.actionsNativeConfigured || s.Cluster == nil {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	var runtime cluster.GitLabActionsRuntime
	var err error
	if original == nil {
		runtime, err = s.Cluster.ResolveGitLabActions(ctx, t, service, config)
	} else {
		runtime = *original
		err = cluster.ValidateGitLabActionsRuntime(runtime)
	}
	if err != nil {
		return nil, err
	}
	if runtime.ControlPlaneTrust == nil {
		return nil, errors.New("GitLab original control-plane trust is unavailable")
	}
	return actions.NewGitLabClient(config.Actions.ProviderTarget(), token, actions.GitLabClientOptions{TrustPolicy: runtime.ControlPlaneTrust, Budget: &s.actionsBudget, TimeoutMinutes: config.Actions.TimeoutMinutes, FreshDeniedNetworks: func(ctx context.Context) ([]string, error) {
		return s.Cluster.GitLabActionsDeniedNetworks(ctx, runtime)
	}})
}
