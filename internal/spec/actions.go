package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	actionsRunnerRepository     = "ghcr.io/hakopod/actions-runner"
	actionsRunnerDigest         = "sha256:1e05326c75ff1412c68e7e2aed6541fa2e3dd07a8ce6389c9246fc253d7eb3b2"
	ActionsRunnerImage          = actionsRunnerRepository + ":2.337.0-hakopod-6d9a8152b7f5dafa1bfc5bbf1a24207710c746a7@" + actionsRunnerDigest
	actionsRunnerResolvedImage  = actionsRunnerRepository + "@" + actionsRunnerDigest
	previousActionsRunnerDigest = "sha256:63768a269f453a8e386588ae77b3ff9bdf911244c0276aad28b324d629be1489"
	previousActionsRunnerImage  = actionsRunnerRepository + ":2.337.0-hakopod-37ab7a0390da5202ab3ac7cb12983d6d08f146bf@" + previousActionsRunnerDigest
)

type GitLabTarget = actions.GitLabTarget
type BitbucketTarget = actions.BitbucketTarget

// Actions describes a provider runner pool. Credentials name application secrets
// used by the control plane, never job environment variables.
type Actions struct {
	Provider         actions.Provider `json:"provider,omitempty" toml:"provider,omitempty"`
	Repository       string           `json:"repository,omitempty" toml:"repository,omitempty"`
	Organization     string           `json:"organization,omitempty" toml:"organization,omitempty"`
	RunnerGroupID    int64            `json:"runner_group_id,omitempty" toml:"runner_group_id,omitempty"`
	GitLab           *GitLabTarget    `json:"gitlab,omitempty" toml:"gitlab,omitempty"`
	Bitbucket        *BitbucketTarget `json:"bitbucket,omitempty" toml:"bitbucket,omitempty"`
	Credential       string           `json:"credential" toml:"credential"`
	JobsCredential   string           `json:"jobs_credential,omitempty" toml:"jobs_credential,omitempty"`
	Labels           []string         `json:"labels" toml:"labels"`
	TimeoutMinutes   int64            `json:"timeout_minutes,omitempty" toml:"timeout_minutes"`
	WorkspaceSizeGiB int64            `json:"workspace_size_gib,omitempty" toml:"workspace_size_gib,omitempty"`
	Cache            *ActionsCache    `json:"cache,omitempty" toml:"cache,omitempty"`
}

// Cache opts into installation-approved storage. Its credential is a local
// secret reference; bucket, endpoint and limits remain installation-owned.
type ActionsCache struct {
	Credential string `json:"credential" toml:"credential"`
}

func (a Actions) Target() actions.Target {
	return actions.Target{Repository: a.Repository, Organization: a.Organization, RunnerGroupID: a.RunnerGroupID}
}

func (a Actions) ProviderTarget() actions.ProviderTarget {
	return actions.ProviderTarget{Provider: a.Provider, GitHub: a.Target(), GitLab: a.GitLab, Bitbucket: a.Bitbucket}
}

func (a Actions) EffectiveJobsCredential() string {
	if a.JobsCredential != "" {
		return a.JobsCredential
	}
	return a.Credential
}

var actionsLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// IsActionsRunnerImage retains exact approved references for saved revisions.
func IsActionsRunnerImage(image string) bool {
	switch image {
	case ActionsRunnerImage, actionsRunnerResolvedImage,
		previousActionsRunnerImage, actionsRunnerRepository + "@" + previousActionsRunnerDigest,
		"ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4",
		"ghcr.io/actions/actions-runner@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4":
		return true
	default:
		return false
	}
}

// New slots use the current pin; prior approved images drain after their job.
func IsCurrentActionsRunnerImage(image string) bool {
	return image == ActionsRunnerImage || image == actionsRunnerResolvedImage
}

func normalizeActions(s *Service) error {
	if s.Actions == nil {
		return nil
	}
	a := s.Actions
	target, err := a.ProviderTarget().Canonical()
	if err != nil {
		return fmt.Errorf("actions: %w", err)
	}
	// Syntax is independent of installation qualification. The cluster validates
	// the exact native binding during planning, acceptance and every new slot.
	a.Provider, a.GitLab, a.Bitbucket = target.Provider, target.GitLab, target.Bitbucket
	if target.Provider == actions.ProviderGitHub {
		// Preserve the omitted default in existing immutable GitHub revisions.
		a.Provider = ""
	}
	if a.Cache != nil && (target.Provider != actions.ProviderGitLab || a.Cache.Credential == "" || !(SecretRef{Ref: a.Cache.Credential}).Valid()) {
		return fmt.Errorf("actions.cache: select a cache secret for an installation-approved GitLab pool")
	}
	if target.Provider == actions.ProviderBitbucket && target.Bitbucket.Repository == "" {
		return fmt.Errorf("actions.bitbucket.repository: select the repository assigned to this runner")
	}
	if !(SecretRef{Ref: a.Credential}).Valid() || a.Credential == "" {
		return fmt.Errorf("actions.credential: select an application secret")
	}
	if a.JobsCredential != "" && !(SecretRef{Ref: a.JobsCredential}).Valid() {
		return fmt.Errorf("actions.jobs_credential: select an application secret")
	}
	if target.Provider == actions.ProviderBitbucket {
		if err := normalizeBitbucketActionsLabels(s); err != nil {
			return err
		}
	} else {
		if len(a.Labels) == 0 {
			a.Labels = []string{"hakopod"}
		}
		if len(a.Labels) > 8 {
			return fmt.Errorf("actions.labels: at most eight labels")
		}
		seen := map[string]bool{}
		for _, label := range a.Labels {
			key := strings.ToLower(label)
			if !actionsLabel.MatchString(label) || seen[key] {
				return fmt.Errorf("actions.labels: use distinct letters, numbers, dots, underscores or hyphens")
			}
			seen[key] = true
		}
	}
	if a.TimeoutMinutes == 0 {
		a.TimeoutMinutes = 60
	}
	if a.TimeoutMinutes < 5 || a.TimeoutMinutes > 360 {
		return fmt.Errorf("actions.timeout_minutes: choose 5–360 minutes")
	}
	if a.WorkspaceSizeGiB < 0 || a.WorkspaceSizeGiB == 1 || a.WorkspaceSizeGiB > 16 {
		return fmt.Errorf("actions.workspace_size_gib: choose 2–16 GiB of temporary workspace storage")
	}
	if target.Provider == actions.ProviderGitHub {
		if s.Image == "" {
			s.Image = ActionsRunnerImage
		}
		if !IsActionsRunnerImage(s.Image) {
			return fmt.Errorf("actions runners use the verified, digest-pinned runner image")
		}
	} else {
		if validateImage(s.Image) != nil || !strings.Contains(s.Image, "@sha256:") {
			return fmt.Errorf("native actions runners require an installation-approved digest-pinned image")
		}
		if s.Architecture != "amd64" && s.Architecture != "arm64" {
			return fmt.Errorf("native actions runners require an explicit amd64 or arm64 architecture")
		}
	}
	if s.Size == "" {
		s.Size = "compute"
	}
	if s.Replicas > 10 {
		return fmt.Errorf("actions pools support at most ten concurrent job slots")
	}
	// An actions pool manages its own sandbox, so it never names a daemon.
	if s.Job != nil || s.Serverless != nil || s.Autoscaling != nil || s.Public || s.BackendHTTP2 || s.Port != 0 || len(s.Ports) > 0 || len(s.PublicTCP) > 0 || len(s.HTTP) > 0 || s.Volume != nil || len(s.Mounts) > 0 || len(s.TemporaryMounts) > 0 || s.GPU != nil || len(s.Env) > 0 || len(s.Secrets) > 0 || len(s.Files) > 0 || len(s.Bindings) > 0 || len(s.Command) > 0 || len(s.Args) > 0 || len(s.DependsOn) > 0 || s.AWSIdentity != "" || s.ContainerDaemon != "" || s.RegistryCredential != "" || len(s.CertificateMounts) > 0 || s.TLS != nil || s.RunAsUser != 0 || s.RunAsGroup != 0 || s.FSGroup != 0 || s.WorkingDir != "" || s.ReadOnlyRootFilesystem || len(s.PrivateEgress) > 0 {
		return fmt.Errorf("actions pools manage their own sandbox, image, environment, storage and networking; remove other workload controls")
	}
	if s.Readiness != nil || s.Healthcheck != "" || s.NetworkAccess != nil || s.TerminationGraceSeconds != 0 {
		return fmt.Errorf("actions pools manage their own probes and network access")
	}
	p := EffectiveResources(*s)
	if target.Provider != actions.ProviderGitHub {
		minimumCPU, minimumMemory, minimumLimit := "200m", "2Gi", "4Gi"
		if target.Provider == actions.ProviderBitbucket {
			minimumCPU, minimumMemory, minimumLimit = "1", "8Gi", "8Gi"
			if ActionsWorkspaceGiB(a) < 4 {
				return fmt.Errorf("Bitbucket runners require at least 4 GiB of temporary storage")
			}
		}
		for _, bound := range []struct{ value, minimum, label string }{
			{p.CPURequest, minimumCPU, "requested CPU"}, {p.CPULimit, "1", "CPU limit"},
			{p.MemoryRequest, minimumMemory, "requested memory"}, {p.MemoryLimit, minimumLimit, "memory limit"},
		} {
			value, err := resource.ParseQuantity(bound.value)
			if err != nil || value.Cmp(resource.MustParse(bound.minimum)) < 0 {
				return fmt.Errorf("%s runners require at least %s %s", target.Provider, bound.minimum, bound.label)
			}
		}
	}
	request, e := resource.ParseQuantity(p.MemoryRequest)
	if e != nil || request.Cmp(resource.MustParse("768Mi")) < 0 {
		return fmt.Errorf("actions pools require at least 768Mi requested memory including sandbox overhead")
	}
	cpu, e := resource.ParseQuantity(p.CPURequest)
	if e != nil || cpu.Cmp(resource.MustParse("200m")) < 0 {
		return fmt.Errorf("actions pools require at least 200m requested CPU including sandbox overhead")
	}
	memory, err := resource.ParseQuantity(p.MemoryLimit)
	if err != nil || memory.Cmp(resource.MustParse("4Gi")) < 0 {
		return fmt.Errorf("actions pools require at least 4Gi memory per job slot, including Docker and temporary storage")
	}
	return nil
}

// ActionsWorkspaceGiB retains the existing default for saved pool revisions.
func ActionsWorkspaceGiB(a *Actions) int64 {
	if a == nil || a.WorkspaceSizeGiB == 0 {
		return 2
	}
	return a.WorkspaceSizeGiB
}

func HasActiveActions(a Application) bool {
	for _, s := range a.Services {
		if s.Actions != nil && !s.Suspended {
			return true
		}
	}
	return false
}
