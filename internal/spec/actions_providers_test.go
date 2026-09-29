package spec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/pelletier/go-toml/v2"
)

func TestActionsExplicitGitHubPreservesExistingRevisionShape(t *testing.T) {
	legacy, err := Normalize(actionsFixture())
	if err != nil {
		t.Fatal(err)
	}
	explicit := actionsFixture()
	explicit.Services["runner"].Actions.Provider = actions.ProviderGitHub
	normalized, err := Normalize(explicit)
	if err != nil || !reflect.DeepEqual(legacy, normalized) {
		t.Fatal("an explicit default changed the saved GitHub configuration", err)
	}
	encoded, err := toml.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "provider =") || strings.Contains(string(encoded), "gitlab") || strings.Contains(string(encoded), "bitbucket") {
		t.Fatal("legacy GitHub revision acquired new provider fields")
	}
	if got := normalized.Services["runner"].Actions.ProviderTarget(); got.Provider.Effective() != actions.ProviderGitHub || got.GitHub.Repository != "team/repository" {
		t.Fatal("legacy provider target changed", got)
	}
}

func TestActionsRejectProvidersUntilRuntimeQualification(t *testing.T) {
	for _, provider := range []actions.Provider{actions.ProviderGitLab, actions.ProviderBitbucket} {
		t.Run(string(provider), func(t *testing.T) {
			app := actionsFixture()
			config := app.Services["runner"].Actions
			config.Repository = ""
			config.Provider = provider
			if provider == actions.ProviderGitLab {
				config.GitLab = &GitLabTarget{ProjectID: 12}
			} else {
				config.Bitbucket = &BitbucketTarget{Workspace: "{12345678-abcd-4234-8234-123456789abc}"}
			}
			if err := config.ProviderTarget().Validate(); err != nil {
				t.Fatal("valid future provider contract rejected", err)
			}
			for _, suspended := range []bool{false, true} {
				svc := app.Services["runner"]
				svc.Suspended = suspended
				app.Services["runner"] = svc
				_, err := Normalize(app)
				var unsupported *actions.UnsupportedProviderError
				if !errors.As(err, &unsupported) || unsupported.Provider != provider || !strings.Contains(err.Error(), "not yet qualified") {
					t.Fatalf("unqualified provider did not fail explicitly: %v", err)
				}
			}
		})
	}
}

func TestActionsRejectMixedProviderTargets(t *testing.T) {
	for name, mutate := range map[string]func(*Actions){
		"implicit github and gitlab": func(a *Actions) { a.GitLab = &GitLabTarget{ProjectID: 12} },
		"explicit github and bitbucket": func(a *Actions) {
			a.Provider = actions.ProviderGitHub
			a.Bitbucket = &BitbucketTarget{Workspace: "{12345678-abcd-4234-8234-123456789abc}"}
		},
		"gitlab and github repository": func(a *Actions) {
			a.Provider = actions.ProviderGitLab
			a.GitLab = &GitLabTarget{ProjectID: 12}
		},
		"unknown provider": func(a *Actions) { a.Provider = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			app := actionsFixture()
			mutate(app.Services["runner"].Actions)
			if _, err := Normalize(app); err == nil {
				t.Fatal("mixed or unknown provider accepted")
			}
		})
	}
}

func TestActionsJobsCredentialReferenceAndFallback(t *testing.T) {
	app := actionsFixture()
	if app.Services["runner"].Actions.EffectiveJobsCredential() != "github-token" {
		t.Fatal("legacy jobs lost their management credential fallback")
	}
	app.Services["runner"].Actions.JobsCredential = "github-jobs"
	normalized, err := Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	config := normalized.Services["runner"].Actions
	if config.Credential != "github-token" || config.EffectiveJobsCredential() != "github-jobs" {
		t.Fatal("separate job permission reference was not preserved")
	}
	if len(SecretReferences(normalized.Services["runner"])) != 0 {
		t.Fatal("control-plane credentials may reach a job")
	}
	encoded, err := toml.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(normalized, decoded) {
		t.Fatal("jobs credential did not round trip", err)
	}
	for _, ref := range []string{"../token", "has spaces", "provider:path", strings.Repeat("a", 128)} {
		bad := actionsFixture()
		bad.Services["runner"].Actions.JobsCredential = ref
		if _, err := Normalize(bad); err == nil || !strings.Contains(err.Error(), "jobs_credential") {
			t.Fatalf("invalid jobs credential reference %q accepted: %v", ref, err)
		}
	}
}

func TestActionsProviderContractsRoundTripWithoutRuntimeAcceptance(t *testing.T) {
	for _, config := range []Actions{
		{Provider: actions.ProviderGitLab, Credential: "gitlab-management", JobsCredential: "gitlab-jobs", Labels: []string{"hakopod"}, GitLab: &GitLabTarget{URL: "https://git.example.test/team/gitlab", ProjectID: 12, TrustPolicy: "company-gitlab"}},
		{Provider: actions.ProviderBitbucket, Credential: "bitbucket-management", Labels: []string{"hakopod"}, Bitbucket: &BitbucketTarget{Workspace: "{12345678-abcd-4234-8234-123456789abc}", Repository: "{aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee}"}},
	} {
		encoded, err := toml.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Actions
		if err = toml.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(config, decoded) {
			t.Fatal("provider TOML contract did not round trip", err)
		}
		encoded, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		decoded = Actions{}
		if err = json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(config, decoded) {
			t.Fatal("provider JSON contract did not round trip", err)
		}
	}
}
