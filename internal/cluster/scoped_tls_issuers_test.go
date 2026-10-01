package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestApplicationIssuersIsolationAndImmutableRetry(t *testing.T) {
	target := Target{ApplicationID: "issuer-a", Project: "a", Environment: "production"}
	var issuer map[string]any
	var secret corev1.Secret
	posts := 0
	secretPosts := 0
	writes := 0
	orphanCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		missing := func() {
			w.WriteHeader(404)
			write(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
		}
		switch {
		case r.URL.Path == "/api/v1/namespaces/"+Namespace(target.ApplicationID):
			write(corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
		case strings.Contains(r.URL.Path, "/secrets"):
			if r.Method == "POST" {
				secretPosts++
				_ = json.NewDecoder(r.Body).Decode(&secret)
				write(secret)
			} else if r.URL.Query().Get("limit") != "" {
				write(corev1.SecretList{Items: make([]corev1.Secret, orphanCount)})
			} else if secret.Name == "" {
				missing()
			} else {
				write(secret)
			}
		case r.URL.Path == applicationIssuerPath(target):
			if r.Method == "POST" {
				posts++
				_ = json.NewDecoder(r.Body).Decode(&issuer)
				write(issuer)
			} else {
				items := []any{}
				if issuer != nil {
					items = append(items, issuer)
				}
				write(map[string]any{"items": items})
			}
		case r.URL.Path == applicationIssuerPath(target)+"/custom":
			if issuer == nil {
				missing()
			} else {
				write(issuer)
			}
		case r.URL.Path == "/apis/cert-manager.io/v1/clusterissuers/default":
			write(map[string]any{"kind": "ClusterIssuer", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{"acme": map[string]any{"email": "private@example.test", "server": "https://acme-v02.api.letsencrypt.org/directory"}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "private account details"}}}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			missing()
		}
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{kube: kube, options: Options{DeploymentMode: DeploymentManagedCloud, TLSIssuer: "default", IngressClass: "haproxy"}}
	ctx := context.Background()
	defaults, err := c.DefaultTLSIssuers(ctx)
	if err != nil || len(defaults.Items) != 1 {
		t.Fatalf("default: %#v %v", defaults, err)
	}
	if v := defaults.Items[0]; !v.Default || v.Kind != "ClusterIssuer" || v.Email != "" || len(v.Conditions) != 0 || !v.Ready {
		t.Fatalf("unsafe default view: %#v", v)
	}
	if err = c.ValidateTLSIssuer(ctx, target, "foreign", ""); err == nil {
		t.Fatal("Cloud allowed foreign global issuer")
	}
	for i := 0; i < 2; i++ {
		v, e := c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", false)
		if e != nil || v.Kind != "Issuer" {
			t.Fatalf("create %d: %#v %v", i, v, e)
		}
	}
	if posts != 1 {
		t.Fatalf("retry created %d issuers", posts)
	}
	assertIssuerSolverSecurityContext(t, issuer)
	// A pre-upgrade issuer differs only by its missing managed seccomp default.
	acme := issuer["spec"].(map[string]any)["acme"].(map[string]any)
	solver := acme["solvers"].([]any)[0].(map[string]any)
	security := solver["http01"].(map[string]any)["ingress"].(map[string]any)["podTemplate"].(map[string]any)["spec"].(map[string]any)["securityContext"].(map[string]any)
	profile := security["seccompProfile"]
	delete(security, "seccompProfile")
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", false); err != nil {
		t.Fatalf("legacy issuer retry: %v", err)
	}
	if _, present := security["seccompProfile"]; present || posts != 1 || secretPosts != 1 || writes != 2 {
		t.Fatal("legacy retry changed issuer or account key")
	}
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "changed@example.test", false); err == nil {
		t.Fatal("legacy retry accepted a changed email")
	}
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", true); err == nil {
		t.Fatal("legacy retry accepted a changed ACME environment")
	}
	security["seccompProfile"] = map[string]any{"type": "Unconfined"}
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", false); err == nil {
		t.Fatal("retry accepted an explicit non-default seccomp profile")
	}
	if posts != 1 || secretPosts != 1 || writes != 2 {
		t.Fatal("conflicting retries changed issuer or account key")
	}
	security["seccompProfile"] = profile
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "changed@example.test", false); err == nil {
		t.Fatal("changed immutable configuration")
	}
	if err = c.ValidateTLSIssuer(ctx, target, "custom", "Issuer"); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ApplicationTLSIssuers(ctx, target)
	if err != nil || len(listed.Items) != 2 {
		t.Fatalf("list: %#v %v", listed, err)
	}
	metadata := issuer["metadata"].(map[string]any)
	metadata["labels"] = map[string]any{managedBy: "hakopod", ownerKey: ownerID("other")}
	if err = c.ValidateTLSIssuer(ctx, target, "custom", "Issuer"); err == nil {
		t.Fatal("foreign issuer accepted")
	}
	metadata["labels"] = labelsFor(target, "")
	secret.Labels[ownerKey] = ownerID("other")
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", false); err == nil {
		t.Fatal("foreign account secret accepted")
	}
	issuer = nil
	secret = corev1.Secret{}
	orphanCount = 16
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "custom", "app@example.test", false); err == nil || !strings.Contains(err.Error(), "16 certificate issuer account keys") {
		t.Fatalf("orphan account bound: %v", err)
	}
	if posts != 1 {
		t.Fatal("quota rejection created an issuer")
	}
}

func TestCloudTLSPreflightRejectsForeignIssuer(t *testing.T) {
	c := &Client{options: Options{DeploymentMode: DeploymentManagedCloud, TLSIssuer: "default"}}
	target := Target{Spec: spec.Application{Services: map[string]spec.Service{"web": {TLS: &spec.TLSConfig{Issuer: "foreign"}}}}}
	if err := c.validateDeliveryPolicy(context.Background(), target); err == nil || !strings.Contains(err.Error(), "default ClusterIssuer") {
		t.Fatalf("foreign preflight: %v", err)
	}
}

func TestMissingDefaultDistinguishesCertificateAPIAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, configured string
		installed        bool
	}{
		{"unconfigured default", "", true},
		{"missing default", "missing", true},
		{"missing controller", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/apis/cert-manager.io/v1" && tc.installed {
					_ = json.NewEncoder(w).Encode(metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "issuers", Namespaced: true}}})
					return
				}
				if r.URL.Path != "/apis/cert-manager.io/v1" && r.URL.Path != "/apis/cert-manager.io/v1/clusterissuers/missing" {
					t.Errorf("issuer discovery listed another resource: %s", r.URL.Path)
				}
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
			}))
			defer server.Close()
			kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			c := &Client{kube: kube, options: Options{TLSIssuer: tc.configured}}
			value, err := c.DefaultTLSIssuers(context.Background())
			if err != nil || value.Installed != tc.installed || len(value.Items) != 0 || value.Message == "" {
				t.Fatalf("discovery: %#v %v", value, err)
			}
		})
	}
}

func TestIssuerReadinessRequiresCurrentObservedGeneration(t *testing.T) {
	var item issuerDocument
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"default","generation":2},"status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}`), &item); err != nil {
		t.Fatal(err)
	}
	if issuerView(item).Ready {
		t.Fatal("stale issuer condition reported the new configuration ready")
	}
	item.Status.Conditions[0].ObservedGeneration = 2
	if !issuerView(item).Ready {
		t.Fatal("current issuer condition was ignored")
	}
}

// Inspect the request sent to Kubernetes, not just the configuration helper.
func assertIssuerSolverSecurityContext(t *testing.T, issuer map[string]any) {
	t.Helper()
	raw, err := json.Marshal(issuer)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Spec struct {
			ACME struct {
				Solvers []struct {
					HTTP01 struct {
						Ingress struct {
							PodTemplate struct {
								Spec struct{ SecurityContext corev1.PodSecurityContext }
							}
						}
					}
				}
			}
		}
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Spec.ACME.Solvers) != 1 {
		t.Fatal("expected one constrained HTTP01 solver")
	}
	security := document.Spec.ACME.Solvers[0].HTTP01.Ingress.PodTemplate.Spec.SecurityContext
	if security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.RunAsUser == nil || *security.RunAsUser != 1001 {
		t.Fatal("solver must retain its non-root identity")
	}
	if security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("solver must use RuntimeDefault seccomp for restricted pod admission")
	}
}

func TestInstallationIssuerSolverSecurityContext(t *testing.T) {
	var issuer map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/cert-manager.io/v1/clusterissuers" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewDecoder(r.Body).Decode(&issuer); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(issuer)
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{kube: kube, options: Options{IngressClass: "haproxy"}}
	if _, err = c.CreateTLSIssuer(context.Background(), "default", "fixture@example.test", false); err != nil {
		t.Fatal(err)
	}
	assertIssuerSolverSecurityContext(t, issuer)
}
