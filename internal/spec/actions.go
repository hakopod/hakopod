package spec

import (
	"fmt"
	"github.com/hakopod/hakopod/internal/actions"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	actionsRunnerRepository    = "ghcr.io/hakopod/actions-runner"
	actionsRunnerDigest        = "sha256:537bb6ad98ed0f4ebb3cd6d595d5c956af5887c93d148b4b6d6e9f1301069dff"
	ActionsRunnerImage         = actionsRunnerRepository + ":2.337.0-hakopod-10386e7c1943aeee52923e2b82e7f0bd1b4e7012@" + actionsRunnerDigest
	actionsRunnerResolvedImage = actionsRunnerRepository + "@" + actionsRunnerDigest
)

// Actions describes an organization or repository pool of single-job runners. Credential
// is a control-plane secret reference, never a job environment variable.
type Actions struct {
	Repository       string   `json:"repository,omitempty" toml:"repository,omitempty"`
	Organization     string   `json:"organization,omitempty" toml:"organization,omitempty"`
	RunnerGroupID    int64    `json:"runner_group_id,omitempty" toml:"runner_group_id,omitempty"`
	Credential       string   `json:"credential" toml:"credential"`
	Labels           []string `json:"labels" toml:"labels"`
	TimeoutMinutes   int64    `json:"timeout_minutes,omitempty" toml:"timeout_minutes"`
	WorkspaceSizeGiB int64    `json:"workspace_size_gib,omitempty" toml:"workspace_size_gib,omitempty"`
}

func (a Actions) Target() actions.Target {
	return actions.Target{Repository: a.Repository, Organization: a.Organization, RunnerGroupID: a.RunnerGroupID}
}

var actionsLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// IsActionsRunnerImage accepts only the approved tagged or resolved reference.
func IsActionsRunnerImage(image string) bool {
	return IsCurrentActionsRunnerImage(image) || image == "ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4" || image == "ghcr.io/actions/actions-runner@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4"
}

// Saved upstream references remain valid; new slots always use the current pin.
func IsCurrentActionsRunnerImage(image string) bool {
	return image == ActionsRunnerImage || image == actionsRunnerResolvedImage
}

func normalizeActions(s *Service) error {
	if s.Actions == nil {
		return nil
	}
	a := s.Actions
	if !a.Target().Valid() {
		return fmt.Errorf("actions: select either a GitHub.com organization or owner/repository; a nonnegative runner_group_id is optional for organizations only")
	}
	if !(SecretRef{Ref: a.Credential}).Valid() || a.Credential == "" {
		return fmt.Errorf("actions.credential: select an application secret")
	}
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
	if a.TimeoutMinutes == 0 {
		a.TimeoutMinutes = 60
	}
	if a.TimeoutMinutes < 5 || a.TimeoutMinutes > 360 {
		return fmt.Errorf("actions.timeout_minutes: choose 5–360 minutes")
	}
	if a.WorkspaceSizeGiB < 0 || a.WorkspaceSizeGiB == 1 || a.WorkspaceSizeGiB > 16 {
		return fmt.Errorf("actions.workspace_size_gib: choose 2–16 GiB of temporary workspace storage")
	}
	if s.Image == "" {
		s.Image = ActionsRunnerImage
	}
	// Registry resolution removes the tag while preserving the approved
	// repository and digest. Both forms must survive deployment validation.
	if !IsActionsRunnerImage(s.Image) {
		return fmt.Errorf("actions runners use the verified, digest-pinned runner image")
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
