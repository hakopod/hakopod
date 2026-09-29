package actions

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProviderTargetsPreserveGitHubDefaults(t *testing.T) {
	legacy := Target{Organization: "team", RunnerGroupID: 42}
	for _, provider := range []Provider{"", ProviderGitHub} {
		canonical, err := (ProviderTarget{Provider: provider, GitHub: legacy}).Canonical()
		if err != nil || canonical.Provider != ProviderGitHub || canonical.GitHub != legacy {
			t.Fatalf("GitHub target changed: %#v, %v", canonical, err)
		}
	}
	// Keep the original comparable representation used by the GitHub budget.
	keys := map[Target]bool{legacy: true}
	if !keys[legacy] {
		t.Fatal("legacy scope is not a stable inventory key")
	}
	for _, provider := range []Provider{"", ProviderGitHub, ProviderGitLab, ProviderBitbucket} {
		if !provider.Valid() {
			t.Fatalf("known provider %q rejected", provider)
		}
	}
	if Provider("GITHUB").Valid() || Provider("other").Valid() {
		t.Fatal("unknown provider accepted")
	}
}

func TestProviderTargetsRejectMixedOrMissingScopes(t *testing.T) {
	gitlab := &GitLabTarget{ProjectID: 12}
	bitbucket := &BitbucketTarget{Workspace: "{12345678-1234-4234-8234-123456789abc}"}
	for name, target := range map[string]ProviderTarget{
		"missing github":       {},
		"github and gitlab":    {GitHub: Target{Repository: "team/repo"}, GitLab: gitlab},
		"github and bitbucket": {GitHub: Target{Repository: "team/repo"}, Bitbucket: bitbucket},
		"missing gitlab":       {Provider: ProviderGitLab},
		"gitlab and github":    {Provider: ProviderGitLab, GitHub: Target{Organization: "team"}, GitLab: gitlab},
		"gitlab and bitbucket": {Provider: ProviderGitLab, GitLab: gitlab, Bitbucket: bitbucket},
		"missing bitbucket":    {Provider: ProviderBitbucket},
		"bitbucket and github": {Provider: ProviderBitbucket, GitHub: Target{Repository: "team/repo"}, Bitbucket: bitbucket},
		"bitbucket and gitlab": {Provider: ProviderBitbucket, GitLab: gitlab, Bitbucket: bitbucket},
		"unknown":              {Provider: "other", GitLab: gitlab},
	} {
		t.Run(name, func(t *testing.T) {
			if target.Validate() == nil {
				t.Fatal("ambiguous provider target accepted")
			}
		})
	}
}

func TestGitLabTargetCanonicalizationAndTrustPolicy(t *testing.T) {
	for _, scope := range []GitLabTarget{{ProjectID: 12}, {GroupID: 13}} {
		canonical, err := (ProviderTarget{Provider: ProviderGitLab, GitLab: &scope}).Canonical()
		if err != nil || canonical.GitLab.URL != "https://gitlab.com" || canonical.GitLab.ProjectID != scope.ProjectID || canonical.GitLab.GroupID != scope.GroupID {
			t.Fatalf("valid GitLab scope rejected: %#v, %v", canonical, err)
		}
		if scope.URL != "" {
			t.Fatal("canonicalization mutated the original target")
		}
	}
	for _, raw := range []string{
		"https://git.example.test:8443/team/gitlab/",
		"https://10.2.3.4/gitlab",
		"https://[fd00::1234]:8443/gitlab",
	} {
		target := ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: raw, ProjectID: 12}}
		if target.Validate() == nil {
			t.Fatal("custom destination without an installation policy accepted")
		}
		target.GitLab.TrustPolicy = "company-gitlab"
		if target.Validate() != nil {
			t.Fatal("explicit policy reference rejected")
		}
	}
	for name, scope := range map[string]GitLabTarget{
		"missing scope":     {},
		"both scopes":       {ProjectID: 12, GroupID: 13},
		"negative project":  {ProjectID: -1, GroupID: 13},
		"negative group":    {ProjectID: 12, GroupID: -1},
		"large project":     {ProjectID: 9007199254740992},
		"large group":       {GroupID: 9007199254740992},
		"policy path":       {ProjectID: 12, TrustPolicy: "../other"},
		"policy whitespace": {ProjectID: 12, TrustPolicy: "company gitlab"},
	} {
		t.Run(name, func(t *testing.T) {
			if (ProviderTarget{Provider: ProviderGitLab, GitLab: &scope}).Validate() == nil {
				t.Fatal("invalid GitLab scope accepted")
			}
		})
	}
}

func TestGitLabURLCanonicalizationPreservesInstancePrefix(t *testing.T) {
	for raw, wanted := range map[string]string{
		"":                        "https://gitlab.com",
		"https://GitLab.COM:443/": "https://gitlab.com",
		"https://gitlab.com./":    "https://gitlab.com",
		"https://git.example.test:8443/team/gitlab/": "https://git.example.test:8443/team/gitlab",
		"https://git.example.test/team/gitlab":       "https://git.example.test/team/gitlab",
		"https://[2001:4860:4860::8888]:443/gitlab":  "https://[2001:4860:4860::8888]/gitlab",
		"https://[::ffff:8.8.8.8]:8443/gitlab":       "https://8.8.8.8:8443/gitlab",
		"https://b\u00fccher.example/gitlab":         "https://xn--bcher-kva.example/gitlab",
	} {
		t.Run(raw, func(t *testing.T) {
			actual, err := CanonicalGitLabURL(raw)
			if err != nil || actual != wanted {
				t.Fatalf("canonical URL = %q, %v; want %q", actual, err, wanted)
			}
		})
	}
}

func TestGitLabURLRejectsAmbiguityAndUnsafeLiteralDestinations(t *testing.T) {
	for _, raw := range []string{
		"http://git.example.test", "https://user:secret@git.example.test", "https://git.example.test?token=secret", "https://git.example.test?", "https://git.example.test#", "https://git.example.test/#fragment",
		"https://git.example.test\\evil", "https://git.example.test/a%2fb", "https://git.example.test/a%252fb", "https://git.example.test/%2e%2e/", "https://git.example.test/../gitlab", "https://git.example.test/./gitlab", "https://git.example.test//gitlab", "https://git.example.test/a b",
		"https://git.example.test/api/v4", "https://git.example.test/gitlab/api/v4/", "https://git.example.test/team/ci", "https://git.example.test/ci/",
		"https://localhost", "https://127.0.0.1", "https://[::1]", "https://[::ffff:127.0.0.1]", "https://169.254.169.254", "https://[fe80::1]", "https://[fe80::1%25eth0]", "https://0.0.0.0", "https://[::]", "https://224.0.0.1", "https://[ff02::1]",
		"https://git.example.test:", "https://git.example.test:0", "https://git.example.test:0443", "https://git.example.test:65536", "https://git.example.test:-1", "https://-git.example.test", "https://git..example.test", "https://" + strings.Repeat("a", 64) + ".example",
		"https://git.example.test/\n", "https://git.example.test/\x00", "https://git.example.test/" + strings.Repeat("a", 2048),
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := CanonicalGitLabURL(raw); err == nil {
				t.Fatal("unsafe or ambiguous URL accepted")
			}
		})
	}
}

func TestBitbucketTargetPreservesUUIDIdentity(t *testing.T) {
	original := &BitbucketTarget{Workspace: "12345678-ABCD-4234-8234-123456789ABC", Repository: "{aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee}"}
	copy := *original
	canonical, err := (ProviderTarget{Provider: ProviderBitbucket, Bitbucket: original}).Canonical()
	if err != nil || canonical.Bitbucket.Workspace != "{12345678-abcd-4234-8234-123456789abc}" || canonical.Bitbucket.Repository != original.Repository {
		t.Fatalf("UUID target changed: %#v, %v", canonical, err)
	}
	if !reflect.DeepEqual(*original, copy) {
		t.Fatal("UUID canonicalization mutated the original target")
	}
	if err := (ProviderTarget{Provider: ProviderBitbucket, Bitbucket: &BitbucketTarget{Workspace: original.Workspace}}).Validate(); err != nil {
		t.Fatal("workspace scope rejected", err)
	}
	for _, value := range []string{"", "workspace-slug", "123", "{00000000-0000-0000-0000-000000000000}", "{12345678-abcd-4234-8234-123456789abc", "12345678-abcd-4234-8234-123456789abc}", "../other", "{12345678-abcd-4234-8234-123456789abc}/other"} {
		if (ProviderTarget{Provider: ProviderBitbucket, Bitbucket: &BitbucketTarget{Workspace: value}}).Validate() == nil {
			t.Fatalf("invalid UUID %q accepted", value)
		}
		if value != "" && (ProviderTarget{Provider: ProviderBitbucket, Bitbucket: &BitbucketTarget{Workspace: original.Workspace, Repository: value}}).Validate() == nil {
			t.Fatalf("invalid repository UUID %q accepted", value)
		}
	}
}

func TestProviderRegistrationNeverSerializesPrivatePayload(t *testing.T) {
	registration := ProviderRegistration{
		Runner:            ProviderRunner{ID: "{12345678-abcd-4234-8234-123456789abc}", Name: "hakopod-fixture"},
		ManagerConfig:     []byte("private-manager-fixture"),
		CleanupCredential: []byte("private-cleanup-fixture"),
	}
	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields["runner"] == nil || strings.Contains(string(encoded), "private-") {
		t.Fatal("registration JSON includes private fields")
	}
	var restored ProviderRegistration
	if err = json.Unmarshal(encoded, &restored); err != nil || restored.Runner.ID != registration.Runner.ID {
		t.Fatal("opaque runner ID did not round trip", err)
	}
}

func TestProviderRegistrationRejectsIncompleteOrUnboundedResults(t *testing.T) {
	fixture := func() ProviderRegistration {
		return ProviderRegistration{Runner: ProviderRunner{ID: "{12345678-abcd-4234-8234-123456789abc}", Name: "hakopod-fixture"}, ManagerConfig: []byte("private-manager-fixture")}
	}
	for _, id := range []string{"123", "{12345678-abcd-4234-8234-123456789abc}"} {
		registration := fixture()
		registration.Runner.ID = id
		if err := registration.Validate("hakopod-fixture"); err != nil {
			t.Fatal("valid opaque registration rejected", err)
		}
	}
	for name, mutate := range map[string]func(*ProviderRegistration){
		"missing id":          func(r *ProviderRegistration) { r.Runner.ID = "" },
		"large id":            func(r *ProviderRegistration) { r.Runner.ID = strings.Repeat("a", 129) },
		"id path":             func(r *ProviderRegistration) { r.Runner.ID = "id/other" },
		"id encoded path":     func(r *ProviderRegistration) { r.Runner.ID = "id%2fother" },
		"id newline":          func(r *ProviderRegistration) { r.Runner.ID = "id\nother" },
		"wrong name":          func(r *ProviderRegistration) { r.Runner.Name = "other" },
		"large status":        func(r *ProviderRegistration) { r.Runner.Status = strings.Repeat("a", 65) },
		"missing config":      func(r *ProviderRegistration) { r.ManagerConfig = nil },
		"large config":        func(r *ProviderRegistration) { r.ManagerConfig = make([]byte, (128<<10)+1) },
		"large cleanup token": func(r *ProviderRegistration) { r.CleanupCredential = make([]byte, (8<<10)+1) },
	} {
		t.Run(name, func(t *testing.T) {
			registration := fixture()
			mutate(&registration)
			if registration.Validate("hakopod-fixture") == nil {
				t.Fatal("unsafe registration accepted")
			}
		})
	}
}
