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
