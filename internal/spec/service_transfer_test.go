package spec

import (
	"reflect"
	"testing"
)

func transferFixture(name string) Application {
	a, _ := Normalize(Application{Name: name, Services: map[string]Service{"api": {Image: "nginx:alpine", Port: 8080, Env: map[string]string{"MODE": "service"}}}})
	return a
}

func TestMovePreservesRuntimeOverridesAndInputs(t *testing.T) {
	source, destination := transferFixture("source"), transferFixture("destination")
	source.InjectEnv = true
	source.Env = map[string]string{"MODE": "app", "SOURCE": "yes"}
	source.Secrets = map[string]SecretRef{"AUTH": {Ref: "auth"}}
	before, _ := Normalize(source)
	left, right, err := MoveService(source, destination, "api", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Services) != 0 || len(right.Services) != 2 || right.Services["worker"].Env["MODE"] != "service" || right.Services["worker"].Env["SOURCE"] != "yes" || right.Services["worker"].Secrets["AUTH"].Ref != "auth" {
		t.Fatal("lost service settings")
	}
	if !reflect.DeepEqual(source, before) || len(destination.Services) != 1 {
		t.Fatal("mutated inputs")
	}
}

func TestMoveRejectsDataAndApplicationBoundResources(t *testing.T) {
	for _, kind := range []string{"collision", "volume", "domain", "job", "dependency", "binding", "external-secret", "network-rule"} {
		t.Run(kind, func(t *testing.T) {
			a, b := transferFixture("source"), transferFixture("destination")
			svc := a.Services["api"]
			name := "new"
			switch kind {
			case "collision":
				name = "api"
			case "volume":
				svc.Volume = &Volume{SizeGiB: 1, MountPath: "/data"}
			case "domain":
				svc.Public = true
				a.Domains = map[string]string{"app.example.com": "api"}
			case "job":
				svc.Port = 0
				svc.Job = &Job{}
			case "dependency":
				a.Services["other"] = Service{Image: "nginx:alpine", DependsOn: []string{"api"}}
			case "binding":
				a.Services["other"] = Service{Image: "nginx:alpine", Bindings: map[string]Binding{"URL": {Service: "api", Protocol: "http"}}}
			case "external-secret":
				svc.Secrets = map[string]SecretRef{"AUTH": {Provider: "vault", Path: "test", Key: "key"}}
			case "network-rule":
				svc.NetworkAccess = &NetworkAccess{From: []string{}}
			}
			a.Services["api"] = svc
			if _, _, err := MoveService(a, b, "api", name); err == nil {
				t.Fatal("unsafe move accepted")
			}
		})
	}
}

func TestCatalogMergePreservesSharedConfiguration(t *testing.T) {
	base, addition := transferFixture("source"), transferFixture("template")
	addition.Services["new"] = addition.Services["api"]
	delete(addition.Services, "api")
	addition.InjectEnv = true
	addition.Env = map[string]string{"TEMPLATE": "yes"}
	next, err := AddServices(base, addition)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Services["api"], base.Services["api"]) || next.Services["new"].Env["TEMPLATE"] != "yes" || len(next.Env) != 0 {
		t.Fatal("template leaked defaults to existing services")
	}
	if _, err = AddServices(next, addition); err == nil {
		t.Fatal("collision silently replaced a service")
	}
	addition.Networks["default"] = Network{Internal: true}
	if _, err = AddServices(base, addition); err == nil {
		t.Fatal("network conflict accepted")
	}
}

func TestSingleServiceTemplateCanUseUniqueName(t *testing.T) {
	base, addition := transferFixture("existing"), transferFixture("template")
	renamed, err := NameTemplateService(addition, "second-api")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := addition.Services["api"]; !ok {
		t.Fatal("mutated template")
	}
	next, err := AddServices(base, renamed)
	if err != nil || len(next.Services) != 2 {
		t.Fatal("cannot add another single-service template", err)
	}
	if _, err = NameTemplateService(next, "one"); err == nil {
		t.Fatal("renamed a multi-service template unsafely")
	}
}
