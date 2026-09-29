package api

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestActionsConfigMatchesApprovedResolvedImage(t *testing.T) {
	saved := spec.Service{Image: spec.ActionsRunnerImage, Replicas: 1, Actions: &spec.Actions{Repository: "team/repo", Credential: "token"}}
	resolved := saved
	resolved.Image = strings.Split(saved.Image, ":")[0] + "@" + strings.SplitN(saved.Image, "@", 2)[1]
	if !sameActionsConfig(saved, resolved) || !sameActionsConfig(resolved, saved) {
		t.Fatal("approved image resolution changed runner identity")
	}
	if saved.Image != spec.ActionsRunnerImage {
		t.Fatal("comparison changed the saved specification")
	}
	for name, change := range map[string]func(*spec.Service){
		"digest": func(s *spec.Service) { s.Image = strings.Split(s.Image, "@")[0] + "@sha256:" + strings.Repeat("a", 64) },
		"repository": func(s *spec.Service) {
			s.Image = strings.Replace(s.Image, "ghcr.io/hakopod/", "ghcr.io/other/", 1)
		},
		"unapproved tag": func(s *spec.Service) { s.Image = strings.Replace(s.Image, "@", ":unapproved@", 1) },
		"restart":        func(s *spec.Service) { s.RestartNonce = "new-revision" },
		"architecture":   func(s *spec.Service) { s.Architecture = "arm64" },
		"credential":     func(s *spec.Service) { s.Actions = &spec.Actions{Repository: "team/repo", Credential: "replacement"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := resolved
			change(&changed)
			if sameActionsConfig(saved, changed) || sameActionsConfig(changed, saved) {
				t.Fatal("different runner configuration was treated as unchanged")
			}
		})
	}
}

func TestActionsSlotImages(t *testing.T) {
	old := "ghcr.io/actions/actions-runner@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4"
	slot := func(image string) store.ActionsSlot { return store.ActionsSlot{Config: spec.Service{Image: image}} }
	if got := actionsSlotImages(nil); got != spec.ActionsRunnerImage {
		t.Fatalf("empty pool: %s", got)
	}
	if got := actionsSlotImages([]store.ActionsSlot{slot(spec.ActionsRunnerImage), slot(spec.ActionsRunnerImage)}); got != spec.ActionsRunnerImage {
		t.Fatalf("replacement pool: %s", got)
	}
	if got := actionsSlotImages([]store.ActionsSlot{slot(spec.ActionsRunnerImage), slot(old)}); got != old+", "+spec.ActionsRunnerImage {
		t.Fatalf("mixed draining pool: %s", got)
	}
}
