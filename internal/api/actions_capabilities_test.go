package api

import (
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestActionsCapabilitiesDoNotEnableUnqualifiedNativeProviders(t *testing.T) {
	providers := managedActionsProviderCapabilities()
	if len(providers) != 3 || providers[0].Provider != actions.ProviderGitHub || !providers[0].Available || providers[0].Image != spec.ActionsRunnerImage || !providers[0].Cache.Persistent {
		t.Fatal("qualified GitHub behavior changed")
	}
	for _, provider := range providers[1:] {
		if provider.Available || provider.Image != "" || provider.Reason == "" || provider.Isolation.SingleJob || provider.Cache.Persistent {
			t.Fatal("native configuration was presented as runtime qualification")
		}
	}
}
