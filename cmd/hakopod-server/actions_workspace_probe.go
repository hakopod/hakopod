package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/hakopod/hakopod/internal/cluster"
)

func parseActionsWorkspaceProbeArgs(args []string) (cluster.ActionsWorkspaceProbeOptions, error) {
	var options cluster.ActionsWorkspaceProbeOptions
	var profile string
	flags := flag.NewFlagSet("actions-workspace-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.Kubeconfig, "kubeconfig", "", "absolute operator kubeconfig path")
	flags.StringVar(&options.Context, "context", "", "exact Kubernetes context")
	flags.StringVar(&options.NodeName, "node", "", "exact local Kubernetes node")
	flags.StringVar(&options.InstallationID, "installation-id", "", "owning installation ID")
	flags.StringVar(&profile, "workspace-profile", "", "shared-overlay2-v1")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return options, fmt.Errorf("actions-workspace-probe requires --kubeconfig, --context, --node, --installation-id and --workspace-profile")
	}
	options.Profile = cluster.ActionsWorkspaceProfile(profile)
	return options, options.Validate()
}

func runActionsWorkspaceProbe(args []string) error {
	options, err := parseActionsWorkspaceProbeArgs(args)
	if err != nil {
		return err
	}
	debug.SetMemoryLimit(192 << 20)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cluster.ProbeActionsWorkspace(ctx, options); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Actions shared workspace qualified; the probe resources and disk workspace were removed.")
	return nil
}
