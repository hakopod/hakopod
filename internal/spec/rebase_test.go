package spec

import (
	"reflect"
	"testing"
)

func app(services map[string]Service, env map[string]string) Application {
	return Application{SchemaVersion: 1, Name: "billing", Services: services, Env: env}
}

func TestRebaseCarriesIndependentChangesForward(t *testing.T) {
	base := app(map[string]Service{"api": {Image: "a@sha256:1", Replicas: 1}, "web": {Image: "w@sha256:1"}}, nil)
	// Someone else deployed a new web image; the draft only scaled api and added reports.
	current := app(map[string]Service{"api": {Image: "a@sha256:1", Replicas: 1}, "web": {Image: "w@sha256:2"}}, nil)
	draft := app(map[string]Service{"api": {Image: "a@sha256:1", Replicas: 3}, "web": {Image: "w@sha256:1"}, "reports": {Image: "r@sha256:1"}}, nil)
	got, conflicts, err := Rebase(base, current, draft)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("rebase = %v, %v", conflicts, err)
	}
	if got.Services["web"].Image != "w@sha256:2" || got.Services["api"].Replicas != 3 || got.Services["reports"].Image != "r@sha256:1" {
		t.Fatalf("unexpected rebase result: %+v", got.Services)
	}
}

func TestRebaseNamesConflictsAndKeepsCurrent(t *testing.T) {
	base := app(map[string]Service{"api": {Image: "a@sha256:1"}}, map[string]string{"MODE": "a"})
	current := app(map[string]Service{"api": {Image: "a@sha256:2"}}, map[string]string{"MODE": "b"})
	draft := app(map[string]Service{"api": {Image: "a@sha256:3"}}, map[string]string{"MODE": "c"})
	got, conflicts, err := Rebase(base, current, draft)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"env", "services.api"}) {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if got.Services["api"].Image != "a@sha256:2" || got.Env["MODE"] != "b" {
		t.Fatalf("conflicting parts must keep the current revision: %+v %v", got.Services, got.Env)
	}
}

func TestRebaseHonoursRemovals(t *testing.T) {
	base := app(map[string]Service{"api": {Image: "a"}, "old": {Image: "o"}}, nil)
	current := app(map[string]Service{"api": {Image: "a"}, "old": {Image: "o"}}, nil)
	draft := app(map[string]Service{"api": {Image: "a"}}, nil)
	got, conflicts, err := Rebase(base, current, draft)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("rebase = %v, %v", conflicts, err)
	}
	if _, ok := got.Services["old"]; ok {
		t.Fatal("a service removed in the draft must stay removed")
	}
}
