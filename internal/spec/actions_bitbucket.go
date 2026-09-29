package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
)

var bitbucketActionsLabel = regexp.MustCompile(`^[a-z0-9.]{1,64}$`)

// Native platform labels are derived from compute selection so a pool cannot
// advertise an architecture that its runner image and node do not provide.
func normalizeBitbucketActionsLabels(s *Service) error {
	if s.Actions == nil || s.Actions.Provider.Effective() != actions.ProviderBitbucket {
		return nil
	}
	platform := "linux"
	if s.Architecture == "arm64" {
		platform = "linux.arm64"
	} else if s.Architecture != "amd64" {
		return fmt.Errorf("actions.labels: select an explicit amd64 or arm64 Bitbucket runner architecture")
	}
	input := s.Actions.Labels
	if len(input) == 0 {
		input = []string{"hakopod"}
	}
	labels, seen := []string{"self.hosted", platform}, map[string]bool{}
	for _, label := range input {
		if !bitbucketActionsLabel.MatchString(label) || seen[label] || strings.HasPrefix(label, "hakopod.owner.") || label == "windows" || label == "macos" || label == "linux.shell" {
			return fmt.Errorf("actions.labels: Bitbucket labels must be unique lowercase letters, numbers and dots")
		}
		seen[label] = true
		if label == "self.hosted" || label == platform {
			continue
		}
		if label == "linux" || label == "linux.arm64" {
			return fmt.Errorf("actions.labels: Bitbucket platform labels must match the selected runner architecture")
		}
		labels = append(labels, label)
	}
	// The provider allows ten custom labels. Reserve one for the durable
	// ownership marker added by the control-plane registration adapter.
	if len(labels) > 11 {
		return fmt.Errorf("actions.labels: Bitbucket supports at most nine custom labels")
	}
	s.Actions.Labels = labels
	return nil
}
