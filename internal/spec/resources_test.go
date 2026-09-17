package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestExplicitResourceValidationAndRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"inherit", `cpu_limit="1"`, ""},
		{"all", "cpu_request='0.125'\ncpu_limit='2'\nmemory_request='512Mi'\nmemory_limit='2Gi'", ""},
		{"bytes", "memory_request='256000000'\nmemory_limit='512000000'", ""},
		{"zero", `cpu_limit="0"`, "cpu_limit"},
		{"negative", `memory_limit="-1Gi"`, "memory_limit"},
		{"fractional cpu", `cpu_request="0.0001"`, "cpu_request"},
		{"cpu unit", `cpu_limit="500Mi"`, "cpu_limit"},
		{"fractional byte", `memory_limit="512m"`, "memory_limit"},
		{"fractional memory", `memory_limit="0.5Gi"`, "memory_limit"},
		{"too large", `memory_limit="999999999999999999999Gi"`, "memory_limit"},
		{"cpu bound", `cpu_limit="65"`, "cpu_limit"},
		{"memory bound", `memory_limit="257Gi"`, "memory_limit"},
		{"tiny memory", `memory_limit="512"`, "memory_limit"},
		{"cpu inversion", "cpu_request='1'\ncpu_limit='500m'", "cpu_request"},
		{"inherited inversion", `memory_request="1Gi"`, "memory_request"},
		{"lower limit inversion", `memory_limit="64Mi"`, "memory_request"},
		{"numeric type", `cpu_limit=1`, "invalid TOML"},
		{"unknown field", `cpu="1"`, "unknown TOML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, err := Parse([]byte("name='test'\n[services.api]\nimage='nginx:alpine'\n[services.api.resources]\n" + tc.fields))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %s, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			again, err := Normalize(app)
			if err != nil || !reflect.DeepEqual(app, again) {
				t.Fatalf("not idempotent: %v", err)
			}
			data, _ := toml.Marshal(app)
			parsed, err := Parse(data)
			if err != nil || !reflect.DeepEqual(app, parsed) {
				t.Fatalf("TOML round-trip: %v", err)
			}
			data, _ = json.Marshal(app)
			var jsonApp Application
			if err := json.Unmarshal(data, &jsonApp); err != nil {
				t.Fatal(err)
			}
			normalized, err := Normalize(jsonApp)
			if err != nil || !reflect.DeepEqual(app, normalized) {
				t.Fatalf("JSON round-trip: %v", err)
			}
		})
	}
	s := Service{Size: "medium", Resources: &Resources{CPULimit: "2"}}
	if p := EffectiveResources(s); p != (Profile{"300m", "2", "308Mi", "615Mi"}) {
		t.Fatal(p)
	}
	if err := ValidateResourceCeiling(Service{Resources: &Resources{CPULimit: "2401m"}}, Profiles["large"]); err == nil {
		t.Fatal("cloud ceiling bypass")
	}
}

func TestComposeResourceUnitsAndConflicts(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"deploy", "deploy: {resources: {limits: {cpus: '1.5', memory: 512m}, reservations: {cpus: 0.2, memory: 128m}}}", ""},
		{"service fields", "cpus: 1.5\n    mem_limit: 512mb\n    mem_reservation: 128m", ""},
		{"consistent aliases", "cpus: 1.5\n    mem_limit: 536870912\n    deploy: {resources: {limits: {cpus: 1.5, memory: 0.5g}, reservations: {cpus: 0.2, memory: 128m}}}", ""},
		{"CPU conflict", "cpus: 2\n    deploy: {resources: {limits: {cpus: 1.5}}}", "conflicts"},
		{"memory conflict", "mem_limit: 1g\n    deploy: {resources: {limits: {memory: 512m}}}", "conflicts"},
		{"unknown limit", "deploy: {resources: {limits: {pids: 100}}}", "pids"},
		{"CPU invalid", "cpus: false", "cpus"},
		{"no limit", "mem_limit: 0", "mem_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft, err := ImportCompose([]byte("name: test\nservices:\n  api:\n    image: nginx:alpine\n    "+tc.fields+"\n"), "", nil, nil)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %s, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := EffectiveResources(draft.Spec.Services["api"])
			if q := resource.MustParse(p.MemoryLimit); q.Cmp(resource.MustParse("512Mi")) != 0 {
				t.Fatal(p)
			}
			if q := resource.MustParse(p.CPULimit); q.Cmp(resource.MustParse("1500m")) != 0 {
				t.Fatal(p)
			}
			if _, err := Parse([]byte(draft.TOML)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResourceChangesAppearInPlanAndDoNotMutateRevision(t *testing.T) {
	old, err := Parse([]byte("name='test'\n[services.api]\nimage='nginx:alpine'\n[services.api.resources]\ncpu_limit='1'"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := Normalize(old)
	if err != nil {
		t.Fatal(err)
	}
	s := next.Services["api"]
	s.Resources.CPULimit = "2"
	next.Services["api"] = s
	if old.Services["api"].Resources.CPULimit != "1" {
		t.Fatal("stored revision mutated")
	}
	changes := Diff(&old, next)
	if len(changes) != 1 || changes[0].Field != "resources" || changes[0].Sensitive {
		t.Fatal(changes)
	}
}
