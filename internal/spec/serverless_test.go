package spec

import (
	"maps"
	"os"
	"strings"
	"testing"
)

func TestServerlessAndNodeRoundTrip(t *testing.T) {
	raw := []byte(`schema_version=1
name="function"
[services.api]
image="node:24-alpine"
port=8080
public=true
node_name="worker.example"
[services.api.serverless]
min_replicas=0
`)
	app, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	svc := app.Services["api"]
	if svc.Serverless.IdleSeconds != 300 || svc.Serverless.MaxConcurrency != 16 || svc.NodeName != "worker.example" {
		t.Fatalf("missing defaults: %+v", svc)
	}
	before := app
	before.Services = maps.Clone(app.Services)
	s := before.Services["api"]
	s.NodeName = ""
	s.Serverless = nil
	before.Services["api"] = s
	fields := map[string]bool{}
	for _, c := range Diff(&before, app) {
		fields[c.Field] = true
	}
	if !fields["node_name"] || !fields["serverless"] {
		t.Fatal(fields)
	}
}
func TestServerlessRejectsIncompatibleWorkloads(t *testing.T) {
	for _, kind := range []string{"private", "replicas", "storage", "job", "autoscale", "port", "idle", "concurrency", "node"} {
		t.Run(kind, func(t *testing.T) {
			svc := Service{Image: "node:24-alpine", Port: 8080, Public: true, Replicas: 1, Serverless: &Serverless{}}
			switch kind {
			case "private":
				svc.Public = false
			case "replicas":
				svc.Replicas = 2
			case "storage":
				svc.Volume = &Volume{SizeGiB: 1, MountPath: "/data"}
			case "job":
				svc.Job = &Job{}
			case "autoscale":
				svc.Autoscaling = &Autoscaling{MinReplicas: 1, MaxReplicas: 2}
			case "port":
				svc.Ports = []Port{{Name: "extra", Port: 8081}}
			case "idle":
				svc.Serverless.IdleSeconds = 29
			case "concurrency":
				svc.Serverless.MaxConcurrency = 65
			case "node":
				svc.NodeName = "arbitrary/node"
			}
			_, err := Normalize(Application{Name: "function", Services: map[string]Service{"api": svc}})
			if err == nil || !strings.Contains(err.Error(), "services.api") {
				t.Fatal("invalid settings accepted", err)
			}
		})
	}
}

func TestHTTPFunctionExamples(t *testing.T) {
	for _, name := range []string{"javascript", "python"} {
		data, err := os.ReadFile("../../examples/http-functions/" + name + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		app, err := Parse(data)
		if err != nil {
			t.Fatal(name, err)
		}
		if app.Services["hello"].Serverless == nil || len(app.Services["hello"].Files) != 1 {
			t.Fatal("function example incomplete")
		}
	}
}
