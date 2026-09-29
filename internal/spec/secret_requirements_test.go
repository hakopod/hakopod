package spec

import (
	"reflect"
	"testing"
)

func TestLocalSecretNamesCoversEffectiveRuntime(t *testing.T) {
	app := Application{Secrets: map[string]SecretRef{"DEFAULT": {Ref: "shared"}, "OVERRIDE": {Ref: "unused"}}, Services: map[string]Service{
		"worker": {Secrets: map[string]SecretRef{"OVERRIDE": {Ref: "worker"}, "EXTERNAL": {Provider: "vault", Path: "app", Key: "token"}}, Files: map[string]File{"cert": {Secret: &SecretRef{Ref: "certificate"}}}, Bindings: map[string]Binding{"URL": {Password: &SecretRef{Ref: "database"}}}},
	}}
	if got := LocalSecretNames(app); !reflect.DeepEqual(got, []string{"certificate", "database", "shared", "worker"}) {
		t.Fatalf("references = %v", got)
	}
}

func TestRunnerSecretsIncludeSeparateJobAccess(t *testing.T) {
	for _, jobs := range []string{"", "runner-management", "workflow-read"} {
		t.Run(jobs, func(t *testing.T) {
			app := Application{Services: map[string]Service{
				"runner": {Actions: &Actions{Repository: "team/repo", Credential: "runner-management", JobsCredential: jobs}},
			}}
			want := []string{"runner-management"}
			if jobs == "workflow-read" {
				want = append(want, jobs)
			}
			for name, discover := range map[string]func(Application) []string{"runtime": LocalSecretNames, "template": TemplateSecretNames} {
				if got := discover(app); !reflect.DeepEqual(got, want) {
					t.Errorf("%s references = %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestRunnerSecretsIncludeCacheWithoutExposingItsValue(t *testing.T) {
	app := Application{Services: map[string]Service{"runner": {Actions: &Actions{Credential: "management", Cache: &ActionsCache{Credential: "cache"}}}}}
	for _, discover := range []func(Application) []string{LocalSecretNames, TemplateSecretNames} {
		if got := discover(app); !reflect.DeepEqual(got, []string{"cache", "management"}) {
			t.Fatalf("cache credential requirement = %v", got)
		}
	}
}
