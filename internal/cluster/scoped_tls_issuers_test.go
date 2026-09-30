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
	orphanCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
