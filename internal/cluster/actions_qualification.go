package cluster

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
)

// GitLabActionsExecutionQualification records real native job acceptance for
// the exact image pair in the enclosing report. Transport sentinel tests alone
// cannot authorize execution, another architecture or another coordinator.
type GitLabActionsExecutionQualification struct {
	Passed              bool     `json:"passed"`
	Architecture        string   `json:"architecture"`
	Coordinators        []string `json:"coordinators"`
	RunnerScopes        []string `json:"runner_scopes"`
	Checkout            bool     `json:"checkout"`
	Script              bool     `json:"script"`
	Artifacts           bool     `json:"artifacts"`
	Services            bool     `json:"services"`
	JobIsolation        bool     `json:"job_isolation"`
	CredentialIsolation bool     `json:"credential_isolation"`
	Drain               bool     `json:"drain"`
	Cleanup             bool     `json:"cleanup"`
	CustomCA            bool     `json:"custom_ca"`
	PrivateCoordinator  bool     `json:"private_coordinator"`
	CrossArchitecture   bool     `json:"cross_architecture"`
	CacheServerAddress  string   `json:"cache_server_address,omitempty"`
	CacheSaveRestore    bool     `json:"cache_save_restore"`
	CacheSizeLimit      bool     `json:"cache_size_limit"`
	CacheScopeIsolation bool     `json:"cache_scope_isolation"`
}

func validateGitLabExecutionReport(images GitLabActionsImages) error {
	proof := images.Execution
	invalid := errors.New("GitLab image pair has no complete native execution qualification")
	if proof == nil || !proof.Passed || proof.Architecture != images.Architecture || !gitlabActionsSHA256.MatchString(images.ExecutionReportSHA256) || images.ExecutionReportSHA256 == strings.Repeat("0", 64) || len(proof.Coordinators) < 1 || len(proof.Coordinators) > 16 || !proof.Checkout || !proof.Script || !proof.Artifacts || !proof.Services || !proof.JobIsolation || !proof.CredentialIsolation || !proof.Drain || !proof.Cleanup {
		return invalid
	}
	if len(proof.RunnerScopes) < 1 || len(proof.RunnerScopes) > 2 {
		return invalid
	}
	for i, scope := range proof.RunnerScopes {
		if scope != "project" && scope != "group" || slices.Contains(proof.RunnerScopes[:i], scope) {
			return invalid
		}
	}
	seen := map[string]bool{}
	for _, coordinator := range proof.Coordinators {
		canonical, err := actions.CanonicalGitLabURL(coordinator)
		if err != nil || canonical != coordinator || seen[coordinator] {
			return invalid
		}
		seen[coordinator] = true
	}
	return nil
}

// ValidateGitLabActionsExecution is the admission boundary. Image transport
// qualification can prepare a smoke test but cannot create a managed pool.
func ValidateGitLabActionsExecution(runtime GitLabActionsRuntime, cache bool) error {
	if err := ValidateGitLabActionsRuntime(runtime); err != nil {
		return err
	}
	if err := validateGitLabExecutionReport(runtime.Images); err != nil {
		return err
	}
	proof := runtime.Images.Execution
	if !slices.Contains(proof.Coordinators, runtime.TransportPolicy.CoordinatorURL) {
		return errors.New("GitLab native execution is not qualified for this coordinator and image pair")
	}
	if len(runtime.CAPEM) > 0 && !proof.CustomCA {
		return errors.New("GitLab native execution has not qualified this custom CA configuration")
	}
	for _, destination := range runtime.PrivateDestinations {
		if runtime.ControlPlaneTrust != nil && len(runtime.ControlPlaneTrust.AllowedPrivateCIDRs) > 0 && destination.Origin != "" && !proof.PrivateCoordinator {
			return errors.New("GitLab native execution has not qualified a private coordinator")
		}
	}
	if cache && (runtime.Cache == nil || runtime.Images.CacheProtocolVersion != 1 || proof.CacheServerAddress != runtime.Cache.ServerAddress || !proof.CacheSaveRestore || !proof.CacheSizeLimit || !proof.CacheScopeIsolation) {
		return errors.New("GitLab cache has no complete save, restore, size and scope qualification for this storage endpoint")
	}
	return nil
}

// ManagedGitLabCapabilities exposes only exact qualified bindings in the caller's
// environment. It does not substitute for admission or fresh network inventory.
func (c *Client) ManagedGitLabCapabilities(project, environment string) actions.ProviderCapabilities {
	capability := (&actions.GitLabClient{}).Capabilities()
	if c.options.ManagedActions == nil {
		return capability
	}
	for _, binding := range c.options.ManagedActions.GitLab {
		if binding.Project != project || binding.Environment != environment || ValidateGitLabActionsExecution(binding.Runtime, false) != nil || validateGitLabExecutionScope(binding.Runtime, binding.Target.GitLab) != nil {
			continue
		}
		runtime := binding.Runtime
		gitlab := *binding.Target.GitLab
		cache := runtime.Cache != nil && ValidateGitLabActionsExecution(runtime, true) == nil
		capability.Bindings = append(capability.Bindings, actions.ProviderBindingCapabilities{
			Application: binding.Application, Service: binding.Service, Image: runtime.Images.Manager,
			Architecture: runtime.Images.Architecture, GitLab: &gitlab, Cache: cache,
			CrossArchitecture: runtime.Images.Execution.CrossArchitecture,
		})
		if !slices.Contains(capability.Build.NativeArchitectures, runtime.Images.Architecture) {
			capability.Build.NativeArchitectures = append(capability.Build.NativeArchitectures, runtime.Images.Architecture)
		}
		capability.Available = true
		capability.Cache.Persistent = capability.Cache.Persistent || cache
	}
	if capability.Available {
		capability.Reason = "Choose an installation-approved pool. Availability depends on its coordinator, image and architecture."
		// A global image or cross-build flag would misrepresent mixed bindings.
		capability.Isolation = actions.ProviderIsolationCapabilities{SingleJob: true, ManagerCredentials: true}
		capability.Build.Reason = "Native and cross-architecture qualification is recorded for each approved pool."
		if capability.Cache.Persistent {
			capability.Cache.Backend, capability.Cache.Reason = "s3", "Persistent cache is available for the approved pools marked with cache support."
		}
	}
	return capability
}

func (c *Client) validateActionsDelivery(ctx context.Context, t Target) error {
	for service, svc := range t.Spec.Services {
		if svc.Actions != nil && !svc.Suspended && svc.Replicas > 0 {
			switch svc.Actions.Provider.Effective() {
			case actions.ProviderGitLab:
				if _, err := c.ResolveGitLabActions(ctx, t, service, svc); err != nil {
					return err
				}
			case actions.ProviderBitbucket:
				return &actions.UnsupportedProviderError{Provider: actions.ProviderBitbucket, Reason: "native execution is not qualified for this installation"}
			}
		}
		if svc.Actions != nil && !svc.Suspended && svc.Replicas > 0 {
			if err := c.ActionsPoolAvailable(ctx, t, svc); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateGitLabExecutionScope(runtime GitLabActionsRuntime, target *actions.GitLabTarget) error {
	scope := "project"
	if target == nil {
		return errors.New("GitLab runner scope is unavailable")
	}
	if target.GroupID > 0 {
		scope = "group"
	}
	if runtime.Images.Execution == nil || !slices.Contains(runtime.Images.Execution.RunnerScopes, scope) {
		return errors.New("GitLab native execution has not qualified this runner scope")
	}
	return nil
}
