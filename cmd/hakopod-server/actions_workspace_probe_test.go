package main

import (
	"os"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
)

func TestActionsWorkspaceProbeArgumentsAreExplicitAndStrict(t *testing.T) {
	valid := []string{"--kubeconfig", "/etc/hakopod/admin-kubeconfig", "--context", "development", "--node", "dev-node", "--installation-id", "0123456789abcdef0123456789abcdef", "--workspace-profile", "shared-overlay2-v1"}
	options, err := parseActionsWorkspaceProbeArgs(valid)
	if err != nil || options.Profile != cluster.ActionsWorkspaceSharedOverlay2V1 || options.Context != "development" {
		t.Fatal("valid probe flags rejected", err)
	}
	for name, args := range map[string][]string{
		"missing":             {},
		"positional":          append(append([]string{}, valid...), "extra"),
		"unknown":             append(append([]string{}, valid...), "--force"),
		"relative kubeconfig": {"--kubeconfig", "relative", "--context", "dev", "--node", "dev", "--installation-id", "0123456789abcdef0123456789abcdef", "--workspace-profile", "shared-overlay2-v1"},
		"profile fallback":    {"--kubeconfig", "/operator/config", "--context", "dev", "--node", "dev", "--installation-id", "0123456789abcdef0123456789abcdef", "--workspace-profile", "vfs"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseActionsWorkspaceProbeArgs(args); err == nil {
				t.Fatal("unsafe or ambiguous flags accepted")
			}
		})
	}
}

func TestActionsWorkspaceProbeDispatchPrecedesOperatorConfigAndDatabase(t *testing.T) {
	previous := os.Args
	defer func() { os.Args = previous }()
	os.Args = []string{"hakopod-server", "actions-workspace-probe"}
	t.Setenv("HAKOPOD_CONFIG_FILE", "/nonexistent/operator-config")
	t.Setenv("HAKOPOD_DATABASE_URL", "")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "kubeconfig") {
		t.Fatal("probe read normal configuration or database before validating its own flags", err)
	}
}
