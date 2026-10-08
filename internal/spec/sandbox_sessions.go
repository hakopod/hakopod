package spec

import (
	"fmt"
	"regexp"
	"strings"
)

const MaxSandboxSessions = 8

// SandboxSession configures a fixed worker and helper for isolated stateful execution.
type SandboxSession struct {
	AllowedIdentities []string `json:"allowed_identities" toml:"allowed_identities"`
	HelperCommand     []string `json:"helper_command" toml:"helper_command"`
	IdleSeconds       int64    `json:"idle_seconds,omitempty" toml:"idle_seconds"`
	LifetimeSeconds   int64    `json:"lifetime_seconds,omitempty" toml:"lifetime_seconds"`
}

func normalizeSandboxSession(app Application, svc *Service) error {
	if svc.Session == nil {
		return nil
	}
	cfg := svc.Session
	if len(cfg.AllowedIdentities) < 1 || len(cfg.AllowedIdentities) > 32 {
		return fmt.Errorf("session.allowed_identities requires 1-32 exact identity IDs")
	}
	seen := map[string]bool{}
	for _, id := range cfg.AllowedIdentities {
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) || seen[id] {
			return fmt.Errorf("session.allowed_identities must contain unique identity IDs")
		}
		seen[id] = true
	}
	if len(cfg.HelperCommand) == 0 || len(svc.Command) == 0 {
		return fmt.Errorf("session requires a fixed worker command and helper_command")
	}
	if err := ValidateCommand(cfg.HelperCommand, nil); err != nil {
		return fmt.Errorf("session.helper_command: %w", err)
	}
	if cfg.IdleSeconds == 0 {
		cfg.IdleSeconds = 900
	}
	if cfg.LifetimeSeconds == 0 {
		cfg.LifetimeSeconds = 3600
	}
	if cfg.IdleSeconds < 60 || cfg.IdleSeconds > 900 || cfg.LifetimeSeconds < cfg.IdleSeconds || cfg.LifetimeSeconds > 3600 {
		return fmt.Errorf("session idle_seconds must be 60-900 and lifetime_seconds must be at least idle_seconds and at most 3600")
	}
	if svc.Job != nil || svc.Actions != nil || svc.Serverless != nil || svc.Autoscaling != nil || svc.GPU != nil || svc.Volume != nil || svc.ContainerDaemon != "" || svc.AWSIdentity != "" {
		return fmt.Errorf("session cannot use another workload mode, autoscaling, GPU, persistent volume, container daemon or AWS identity")
	}
	if svc.Port != 0 || len(svc.Ports) != 0 || svc.Public || len(svc.PublicTCP) != 0 || len(svc.HTTP) != 0 || svc.TLS != nil || svc.BackendHTTP2 || svc.NetworkAccess != nil || len(svc.Networks) != 0 || len(svc.PrivateEgress) != 0 {
		return fmt.Errorf("session cannot expose ports or join application networks")
	}
	if len(svc.Secrets) != 0 || len(app.Secrets) != 0 || len(svc.Bindings) != 0 || len(svc.Files) != 0 || len(svc.Mounts) != 0 || len(svc.CertificateMounts) != 0 || len(svc.DependsOn) != 0 || app.InjectEnv {
		return fmt.Errorf("session cannot receive secrets, bindings, files, persistent mounts, certificates, dependencies or injected environment")
	}
	if len(app.Env) != 0 {
		return fmt.Errorf("session applications cannot define shared environment variables")
	}
	if svc.Replicas > 1 || svc.Healthcheck != "" || svc.Readiness != nil || svc.UpdateStrategy != "" {
		return fmt.Errorf("session cannot configure replicas, readiness, healthcheck or update strategy")
	}
	if svc.RuntimeProfile == "" {
		return fmt.Errorf("session requires an administrator-approved sandbox runtime_profile")
	}
	if svc.RunAsUser < 1 || svc.RunAsGroup < 1 || svc.FSGroup < 1 || !svc.ReadOnlyRootFilesystem {
		return fmt.Errorf("session requires explicit nonroot user/group/fs_group and a read-only root filesystem")
	}
	if !strings.Contains(svc.Image, "@sha256:") {
		return fmt.Errorf("session requires a digest-pinned worker image")
	}
	return nil
}
