package spec

import (
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
)

func TestBitbucketLabelsFollowTheSelectedNativeArchitecture(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		platform := "linux"
		if architecture == "arm64" {
			platform = "linux.arm64"
		}
		for _, input := range [][]string{nil, {"hakopod"}, {platform, "self.hosted", "hakopod"}} {
			s := Service{Architecture: architecture, Actions: &Actions{Provider: actions.ProviderBitbucket, Labels: input}}
			if err := normalizeBitbucketActionsLabels(&s); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.Actions.Labels, []string{"self.hosted", platform, "hakopod"}) {
				t.Fatal("native routing lost the selected architecture or custom label", s.Actions.Labels)
			}
			if err := normalizeBitbucketActionsLabels(&s); err != nil {
				t.Fatal("normalization was not idempotent", err)
			}
		}
	}
}

func TestBitbucketLabelsRejectInvalidRoutingBeforeRegistration(t *testing.T) {
	for _, input := range [][]string{
		{"linux.arm64"}, {"linux", "linux.arm64"}, {"linux.shell"}, {"windows"}, {"macos"},
		{"UPPER"}, {"hyphen-name"}, {"underscore_name"}, {"a", "a"}, {"hakopod.owner.spoof"},
		{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
	} {
		s := Service{Architecture: "amd64", Actions: &Actions{Provider: actions.ProviderBitbucket, Labels: input}}
		if err := normalizeBitbucketActionsLabels(&s); err == nil {
			t.Fatal("invalid provider labels reached registration", input)
		}
		if !reflect.DeepEqual(s.Actions.Labels, input) {
			t.Fatal("failed normalization discarded entered labels")
		}
	}
	s := Service{Actions: &Actions{Provider: actions.ProviderBitbucket}}
	if err := normalizeBitbucketActionsLabels(&s); err == nil {
		t.Fatal("missing architecture advertised a default native platform")
	}
}
