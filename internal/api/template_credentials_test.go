package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientconfig "k8s.io/client-go/tools/clientcmd/api"
)

func templateSecretKube(t *testing.T) *cluster.Client {
	t.Helper()
	var mu sync.Mutex
	secrets := map[string]corev1.Secret{}
	version := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/v1/namespaces/hakopod-system" {
			write(w, 200, corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "hakopod-system", Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod"}}})
			return
		}
		base := "/api/v1/namespaces/hakopod-system/secrets"
		if !strings.HasPrefix(r.URL.Path, base) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, base+"/")
		if r.Method == "GET" && r.URL.Path == base {
			selector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
			if err != nil {
				t.Error(err)
				return
			}
			list := corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: []corev1.Secret{}}
			for _, item := range secrets {
				if selector.Matches(labels.Set(item.Labels)) {
					list.Items = append(list.Items, item)
				}
			}
			write(w, 200, list)
			return
		}
		if r.Method == "POST" || r.Method == "PUT" {
			var next corev1.Secret
			body, err := io.ReadAll(io.LimitReader(r.Body, 512<<10))
			if err != nil {
				t.Error(err)
				return
			}
			if _, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, &next); err != nil {
				t.Error("invalid Kubernetes secret request")
				return
			}
			if _, exists := secrets[next.Name]; exists && r.Method == "POST" {
				write(w, 409, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Reason: metav1.StatusReasonAlreadyExists, Code: 409})
				return
			}
			version++
			next.ResourceVersion = strconv.Itoa(version)
			next.APIVersion = "v1"
			next.Kind = "Secret"
			secrets[next.Name] = next
			write(w, 200, next)
			return
		}
		if item, exists := secrets[name]; exists {
			write(w, 200, item)
			return
		}
		write(w, 404, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Reason: metav1.StatusReasonNotFound, Code: 404})
	}))
	t.Cleanup(remote.Close)
	file := filepath.Join(t.TempDir(), "kubeconfig")
	configuration := clientconfig.Config{Clusters: map[string]*clientconfig.Cluster{"test": {Server: remote.URL}}, Contexts: map[string]*clientconfig.Context{"test": {Cluster: "test"}}, CurrentContext: "test"}
	if err := clientcmd.WriteToFile(configuration, file); err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New(file, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTemplateCredentialsAndReviewedDeployment(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "template-credentials")
	if err != nil {
		t.Fatal(err)
	}
	c := templateSecretKube(t)
	handler := (&Server{Store: db, Cluster: c}).Handler()
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Idempotency-Key", "template-review-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, expected %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{Name: "catalog", SiteURL: "https://secrets.example.test"}}
	var plan struct {
		Spec          spec.Application      `json:"spec"`
		TOML          string                `json:"toml"`
		Configuration templateConfiguration `json:"configuration"`
		Required      []string              `json:"required_secrets"`
	}
	if err = json.Unmarshal(call("POST", "/templates/infisical/plan", cfg, 200), &plan); err != nil {
		t.Fatal(err)
	}
	secretPath := func(name string) string {
		return "/templates/infisical/secrets/" + name + "?project=demo&environment=development&application=catalog"
	}
	call("PUT", secretPath("encryption-key"), map[string]string{"value": "incorrect-encryption-material"}, 400)
	call("PUT", secretPath("database-url"), map[string]bool{"generate": true}, 400)
	for _, name := range []string{"database-password", "redis-password", "encryption-key", "auth-secret", "database-url", "redis-url"} {
		body := call("PUT", secretPath(name), map[string]bool{"generate": true}, 200)
		if bytes.Contains(body, []byte(`"value"`)) {
			t.Fatal("secret write returned its value")
		}
	}
	values, err := c.ReadWorkloadSecrets(ctx, "demo", "development", "catalog", plan.Required)
	if err != nil {
		t.Fatal(err)
	}
	if err = spec.ValidateTemplateSecretSet("infisical", plan.Required, values); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReadWorkloadSecrets(ctx, "demo", "development", "other", plan.Required); err == nil {
		t.Fatal("template secrets escaped application scope")
	}
	call("PUT", secretPath("auth-secret"), map[string]bool{"generate": true}, 409)
	after, err := c.ReadWorkloadSecrets(ctx, "demo", "development", "catalog", []string{"auth-secret"})
	if err != nil || after["auth-secret"] != values["auth-secret"] {
		t.Fatal("retrying generation rotated a saved credential")
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": 0}
	main := plan.Spec.Services["main"]
	main.Size = "large"
	plan.Spec.Services["main"] = main
	changed, err := toml.Marshal(plan.Spec)
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/templates/infisical/deploy", map[string]any{"configuration": plan.Configuration, "toml": string(changed), "expected_revision": 0}, 409)
	call("PUT", secretPath("database-password"), map[string]any{"value": "a different private password", "replace": true}, 200)
	call("POST", "/templates/infisical/deploy", deploy, 400)
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid template queued a deployment", err)
	}
	call("PUT", secretPath("database-url"), map[string]bool{"generate": true, "replace": true}, 200)
	first := call("POST", "/templates/infisical/deploy", deploy, 202)
	second := call("POST", "/templates/infisical/deploy", deploy, 202)
	var accepted, replayed store.Deployment
	if json.Unmarshal(first, &accepted) != nil || json.Unmarshal(second, &replayed) != nil || accepted.ID != replayed.ID {
		t.Fatal("reviewed template retry was not idempotent")
	}
	for _, value := range values {
		if bytes.Contains(first, []byte(value)) || strings.Contains(plan.TOML, value) {
			t.Fatal("credential entered a deployment or TOML response")
		}
	}
	call("PUT", secretPath("auth-secret"), map[string]bool{"generate": true, "replace": true}, 409)
}
