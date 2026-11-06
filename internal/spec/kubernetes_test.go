package spec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func kubernetesFixture(t *testing.T) Application {
	t.Helper()
	a, err := Parse([]byte(`schema_version = 1
name = "shop"
[services.api]
image = "nginx:1.27"
port = 8080
public = true
[services.api.kubernetes]
deployment = '''
spec:
  template:
    spec:
      tolerations:
        - key: dedicated
          operator: Equal
          value: shop
          effect: NoSchedule
'''
ingress = '''
metadata:
  annotations:
    haproxy.org/timeout-server: 120s
'''
`))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestKubernetesPatchesParseNormalizeAndRoundTrip(t *testing.T) {
	a := kubernetesFixture(t)
	k := a.Services["api"].Kubernetes
	if k == nil || !strings.Contains(k.Deployment, "tolerations") || !strings.Contains(k.Ingress, "haproxy.org/timeout-server") {
		t.Fatalf("patches not parsed: %+v", k)
	}
	b, err := Normalize(a)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("normalization is not stable", err)
	}
	data, err := toml.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "deployment = \"\"\"") {
		t.Fatalf("patches should export as multi-line strings:\n%s", data)
	}
	roundTrip, err := Parse(data)
	if err != nil || !reflect.DeepEqual(roundTrip, a) {
		t.Fatalf("TOML round trip: %v", err)
	}
	empty := a
	empty.Services = map[string]Service{"api": a.Services["api"]}
	s := empty.Services["api"]
	s.Kubernetes = &KubernetesPatches{}
	empty.Services["api"] = s
	if out, err := Normalize(empty); err != nil || out.Services["api"].Kubernetes != nil {
		t.Fatalf("an empty kubernetes table should normalize away: %v", err)
	}
}

func TestKubernetesPatchesRejected(t *testing.T) {
	cases := map[string]struct {
		modify func(*Service)
		want   string
	}{
		"invalid-yaml": {func(s *Service) { s.Kubernetes.Deployment = "spec: [unclosed" }, "kubernetes.deployment: invalid YAML"},
		"not-mapping":  {func(s *Service) { s.Kubernetes.Deployment = "- a\n- b\n" }, "must be a YAML mapping"},
		"scalar":       {func(s *Service) { s.Kubernetes.Service = "hello" }, "must be a YAML mapping"},
		"too-large": {func(s *Service) {
			s.Kubernetes.Deployment = "metadata: {}\n#" + strings.Repeat("x", MaxKubernetesPatchBytes)
		}, "exceeds"},
		"api-version":     {func(s *Service) { s.Kubernetes.Deployment = "apiVersion: apps/v1\n" }, "apiVersion cannot be patched"},
		"kind":            {func(s *Service) { s.Kubernetes.Service = "kind: Service\n" }, "kind cannot be patched"},
		"name":            {func(s *Service) { s.Kubernetes.Deployment = "metadata:\n  name: other\n" }, "metadata.name cannot be patched"},
		"namespace":       {func(s *Service) { s.Kubernetes.Ingress = "metadata:\n  namespace: kube-system\n" }, "metadata.namespace cannot be patched"},
		"ingress-private": {func(s *Service) { s.Public = false }, "kubernetes.ingress: requires a public service"},
		"service-no-port": {func(s *Service) {
			s.Public, s.Port, s.Kubernetes.Ingress, s.Kubernetes.Service = false, 0, "", "metadata: {}\n"
		}, "kubernetes.service: requires a service with a port"},
		"serverless":       {func(s *Service) { s.Serverless = &Serverless{} }, "kubernetes"},
		"deployment-job":   {func(s *Service) { s.Public, s.Port, s.Kubernetes.Ingress, s.Job = false, 0, "", &Job{} }, "Kubernetes patches"},
		"invalid-ingress":  {func(s *Service) { s.Kubernetes.Ingress = "metadata: [" }, "kubernetes.ingress: invalid YAML"},
		"null-document":    {func(s *Service) { s.Kubernetes.Deployment = "# only a comment\n" }, "must be a YAML mapping"},
		"metadata-name-ok": {func(s *Service) { s.Kubernetes.Deployment = "metadata:\n  labels:\n    name: fine\n" }, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := kubernetesFixture(t)
			s := a.Services["api"]
			k := *s.Kubernetes
			s.Kubernetes = &k
			tc.modify(&s)
			a.Services["api"] = s
			_, err := Normalize(a)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid patch rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestKubernetesPatchesRejectedOnSandboxSession(t *testing.T) {
	a := sessionSpecFixture()
	s := a.Services["worker"]
	s.Kubernetes = &KubernetesPatches{Deployment: "metadata: {}\n"}
	a.Services["worker"] = s
	if _, err := Normalize(a); err == nil || !strings.Contains(err.Error(), "Kubernetes patches") {
		t.Fatalf("session accepted a Kubernetes patch: %v", err)
	}
}

func TestKubernetesPatchesDiffAndChanged(t *testing.T) {
	before := kubernetesFixture(t)
	after := kubernetesFixture(t)
	if KubernetesPatchesChanged(before, after) {
		t.Fatal("unchanged patches need no kubernetes:patch permission")
	}
	if len(Diff(&before, after)) != 0 {
		t.Fatal("unchanged patches should not appear in the plan")
	}
	changed := after.Services["api"]
	k := *changed.Kubernetes
	k.Ingress = "metadata:\n  annotations:\n    haproxy.org/timeout-server: 300s\n"
	changed.Kubernetes = &k
	after.Services["api"] = changed
	if !KubernetesPatchesChanged(before, after) {
		t.Fatal("a changed patch must need kubernetes:patch")
	}
	var found *Change
	for _, c := range Diff(&before, after) {
		if c.Field == "kubernetes" && c.Service == "api" {
			found = &c
		}
	}
	if found == nil || found.Sensitive {
		t.Fatalf("patch change must be a visible, non-sensitive plan entry: %+v", found)
	}

	removed := kubernetesFixture(t)
	s := removed.Services["api"]
	s.Kubernetes = &KubernetesPatches{Deployment: s.Kubernetes.Deployment}
	removed.Services["api"] = s
	if KubernetesPatchesChanged(before, removed) {
		t.Fatal("removing a patch must not need kubernetes:patch")
	}
	none := kubernetesFixture(t)
	s = none.Services["api"]
	s.Kubernetes = nil
	none.Services["api"] = s
	if KubernetesPatchesChanged(before, none) || !KubernetesPatchesChanged(none, before) {
		t.Fatal("adding patches to a service must need kubernetes:patch; removing them must not")
	}
	if !KubernetesPatchesChanged(Application{}, before) {
		t.Fatal("a new application with patches must need kubernetes:patch")
	}
}
