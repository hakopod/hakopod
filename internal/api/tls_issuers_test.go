package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientconfig "k8s.io/client-go/tools/clientcmd/api"
)

// Synthetic Kubernetes responses exercise real API authorization and durable
// revisions in PostgreSQL. The separate development-cluster test proves issuance.
type tlsAPIFixture struct {
	t                 *testing.T
	db                *store.Store
	owner             store.Principal
	app               store.Application
	other             store.Application
	token             string
	handler           http.Handler
	mu                sync.Mutex
	issuers           map[string]map[string]any
	secrets           map[string]corev1.Secret
	requests          int
	writes            int
	blockNamespaces   chan struct{}
	enteredNamespaces chan struct{}
}

func newTLSAPIFixture(t *testing.T) *tlsAPIFixture {
	t.Helper()
	f := &tlsAPIFixture{t: t, db: sourceDatabase(t), issuers: map[string]map[string]any{}, secrets: map[string]corev1.Secret{}}
	ctx := context.Background()
	raw, err := f.db.Bootstrap(ctx, "tls-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.owner, err = f.db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	newApp := func(name string) store.Application {
		app, err := spec.Normalize(spec.Application{Name: name, Services: map[string]spec.Service{"web": {Image: "example.invalid/synthetic@sha256:" + strings.Repeat("a", 64), Port: 8080, Public: true}}})
		if err != nil {
			t.Fatal(err)
		}
		dep, err := f.db.Accept(ctx, f.owner, "demo", "development", app, 0, "tls-initial-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", dep.ID); err != nil {
			t.Fatal(err)
		}
		value, err := f.db.Application(ctx, dep.ApplicationID)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	f.app, f.other = newApp("issuer-app"), newApp("other-app")
	f.token = f.key("writer", "issuer-app", "deployments:read", "deployments:write")
	kube := httptest.NewServer(http.HandlerFunc(f.kubernetes))
	t.Cleanup(kube.Close)
	file := filepath.Join(t.TempDir(), "kubeconfig")
	configuration := clientconfig.Config{Clusters: map[string]*clientconfig.Cluster{"fixture": {Server: kube.URL}}, Contexts: map[string]*clientconfig.Context{"fixture": {Cluster: "fixture"}}, CurrentContext: "fixture"}
	if err = clientcmd.WriteToFile(configuration, file); err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New(file, cluster.Options{DeploymentMode: cluster.DeploymentManagedCloud, TLSIssuer: "default", IngressClass: "haproxy", AppDomain: "apps.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = (&Server{Store: f.db, Cluster: c, Auth: AuthConfig{DeploymentMode: cluster.DeploymentManagedCloud}}).Handler()
	return f
}

func (f *tlsAPIFixture) key(name, application string, permissions ...string) string {
	f.t.Helper()
	_, token, err := f.db.CreateKey(context.Background(), f.owner, store.KeyInput{Name: name, Project: "demo", Environment: "development", Application: application, Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func (f *tlsAPIFixture) kubernetes(w http.ResponseWriter, r *http.Request) {
	if f.blockNamespaces != nil && (r.URL.Path == "/api/v1/namespaces/"+cluster.Namespace(f.app.ID) || r.URL.Path == "/api/v1/namespaces/"+cluster.Namespace(f.other.ID)) {
		select {
		case <-f.blockNamespaces:
		default:
			f.enteredNamespaces <- struct{}{}
			select {
			case <-f.blockNamespaces:
			case <-r.Context().Done():
				return
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	missing := func() {
		write(w, 404, metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
	}
	if r.URL.Path == "/apis/cert-manager.io/v1/clusterissuers/default" && r.Method == "GET" {
		write(w, 200, map[string]any{"kind": "ClusterIssuer", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{"acme": map[string]any{"email": "private-operator@example.test", "server": "https://acme-v02.api.letsencrypt.org/directory"}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "private account detail"}}}})
		return
	}
	for _, app := range []store.Application{f.app, f.other} {
		ns := cluster.Namespace(app.ID)
		base := "/api/v1/namespaces/" + ns
		if r.URL.Path == base && r.Method == "GET" {
			hash := sha256.Sum256([]byte(app.ID))
			write(w, 200, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/application-id": fmt.Sprintf("%x", hash[:16])}}})
			return
		}
		issuerPath := "/apis/cert-manager.io/v1/namespaces/" + ns + "/issuers"
		if r.URL.Path == issuerPath {
			if r.Method == "POST" {
				var item map[string]any
				if json.NewDecoder(r.Body).Decode(&item) != nil {
					f.t.Error("invalid issuer body")
					missing()
					return
				}
				name := item["metadata"].(map[string]any)["name"].(string)
				f.issuers[ns+"/"+name] = item
				f.writes++
				write(w, 201, item)
				return
			}
			items := []any{}
			for key, item := range f.issuers {
				if strings.HasPrefix(key, ns+"/") {
					items = append(items, item)
				}
			}
			write(w, 200, map[string]any{"items": items})
			return
		}
		if strings.HasPrefix(r.URL.Path, issuerPath+"/") && r.Method == "GET" {
			item, ok := f.issuers[ns+"/"+strings.TrimPrefix(r.URL.Path, issuerPath+"/")]
			if !ok {
				missing()
				return
			}
			write(w, 200, item)
			return
		}
		if r.URL.Path == base+"/secrets" {
			if r.Method == "POST" {
				var secret corev1.Secret
				data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
				if err == nil {
					_, _, err = scheme.Codecs.UniversalDeserializer().Decode(data, nil, &secret)
				}
				if err != nil {
					f.t.Error("invalid secret body")
					missing()
					return
				}
				f.secrets[ns+"/"+secret.Name] = secret
				f.writes++
				write(w, 201, secret)
				return
			}
			items := []corev1.Secret{}
			for key, item := range f.secrets {
				if strings.HasPrefix(key, ns+"/") {
					items = append(items, item)
				}
			}
			write(w, 200, corev1.SecretList{Items: items})
			return
		}
		if strings.HasPrefix(r.URL.Path, base+"/secrets/") && r.Method == "GET" {
			secret, ok := f.secrets[ns+"/"+strings.TrimPrefix(r.URL.Path, base+"/secrets/")]
			if !ok {
				missing()
				return
			}
			write(w, 200, secret)
			return
		}
	}
	f.t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.Path)
	missing()
}

func (f *tlsAPIFixture) request(method, path, token string, body any, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "tls-api-fixture-"+path)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestCloudTLSIssuerAPIVisibilityAndOwnership(t *testing.T) {
	f := newTLSAPIFixture(t)
	path := "/applications/" + f.app.ID + "/tls/issuers"
	input := map[string]any{"name": "custom", "email": "app@example.test", "production": false}
	f.request("GET", "/tls/issuers", "", nil, 401)
	response := f.request("GET", "/tls/issuers", f.token, nil, 200)
	var defaults cluster.TLSIssuers
	if json.Unmarshal(response.Body.Bytes(), &defaults) != nil || len(defaults.Items) != 1 || !defaults.Items[0].Default || !defaults.Items[0].Ready || defaults.Items[0].Kind != "ClusterIssuer" {
		t.Fatalf("default discovery: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private") {
		t.Fatal("Cloud exposed operator contact or condition details")
	}
	f.request("POST", "/tls/issuers", f.token, input, 403)
	reader := f.key("reader", "issuer-app", "deployments:read")
	f.request("GET", path, reader, nil, 200)
	f.request("POST", path, reader, input, 403)
	before := f.requests
	f.request("GET", "/applications/"+f.other.ID+"/tls/issuers", f.token, nil, 403)
	f.request("POST", "/applications/"+f.other.ID+"/tls/issuers", f.token, input, 403)
	if f.requests != before || f.writes != 0 {
		t.Fatal("unauthorized issuer request reached Kubernetes")
	}
	for i := 0; i < 2; i++ {
		created := f.request("POST", path, f.token, input, 201)
		var issuer cluster.TLSIssuer
		if json.Unmarshal(created.Body.Bytes(), &issuer) != nil || issuer.Name != "custom" || issuer.Kind != "Issuer" || issuer.Default || issuer.Ready {
			t.Fatalf("incorrect created issuer: %s", created.Body.String())
		}
		if strings.Contains(created.Body.String(), "PRIVATE KEY") || strings.Contains(created.Body.String(), "tls.key") {
			t.Fatal("account key escaped the cluster")
		}
	}
	if f.writes != 2 {
		t.Fatalf("retry changed immutable issuer/account: %d writes", f.writes)
	}
	var audits int
	if err := f.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='application-tls-issuer.configured' AND resource=$1", f.app.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("durable audit: %d %v", audits, err)
	}
	input["email"] = "changed@example.test"
	f.request("POST", path, f.token, input, 409)
	if f.writes != 2 {
		t.Fatal("conflicting retry mutated the issuer")
	}
}

func TestCloudTLSIssuerAPILocksAndAttachment(t *testing.T) {
	f := newTLSAPIFixture(t)
	path := "/applications/" + f.app.ID + "/tls/issuers"
	input := map[string]any{"name": "custom", "email": "app@example.test"}
	claim, err := f.db.ClaimRuntime(context.Background(), f.app.ID, f.app.Revision)
	if err != nil || claim == nil {
		t.Fatalf("runtime lock: %v", err)
	}
	f.request("POST", path, f.token, input, 409)
	claim.Release()
	if f.writes != 0 {
		t.Fatal("busy application created an issuer")
	}
	f.request("POST", path, f.token, input, 201)
	tlsPath := "/applications/" + f.app.ID + "/services/web/tls"
	f.request("POST", tlsPath, f.token, map[string]any{"expected_revision": 1, "issuer": "foreign"}, 409)
	f.request("POST", tlsPath, f.token, map[string]any{"expected_revision": 1, "issuer": "custom", "issuer_kind": "issuer"}, 400)
	f.request("POST", tlsPath, f.token, map[string]any{"expected_revision": 1, "certificate_pem": "invalid", "private_key_pem": "invalid", "issuer_kind": "Issuer"}, 400)
	reader := f.key("reader", "issuer-app", "deployments:read")
	f.request("POST", tlsPath, reader, map[string]any{"expected_revision": 1, "issuer": "default"}, 403)
	accepted := f.request("POST", tlsPath, f.token, map[string]any{"expected_revision": 1, "issuer": "custom", "issuer_kind": "Issuer"}, 202)
	var dep store.Deployment
	if json.Unmarshal(accepted.Body.Bytes(), &dep) != nil || dep.Revision != 2 {
		t.Fatalf("TLS revision: %s", accepted.Body.String())
	}
	saved, err := f.db.RuntimeBase(context.Background(), f.app.ID, 2)
	if err != nil || saved.Spec.Services["web"].TLS == nil || saved.Spec.Services["web"].TLS.IssuerKind != "Issuer" || saved.ResolvedSpec == nil || saved.ResolvedSpec.Services["web"].TLS.IssuerKind != "Issuer" {
		t.Fatalf("issuer kind was not retained in desired and immutable resolved revisions: %v", err)
	}
	f.request("POST", path, f.token, input, 409)
}

func TestCloudTLSDefaultAttachmentCompatibility(t *testing.T) {
	f := newTLSAPIFixture(t)
	path := "/applications/" + f.app.ID + "/services/web/tls"
	f.request("POST", path, f.token, map[string]any{"expected_revision": 1, "issuer": "default"}, 202)
	saved, err := f.db.RuntimeBase(context.Background(), f.app.ID, 2)
	if err != nil || saved.Spec.Services["web"].TLS == nil || saved.Spec.Services["web"].TLS.Issuer != "default" || saved.Spec.Services["web"].TLS.IssuerKind != "" {
		t.Fatalf("legacy default reference changed: %v", err)
	}
}

func TestCloudTLSIssuerOverloadLeavesAPIReadsAvailable(t *testing.T) {
	f := newTLSAPIFixture(t)
	f.blockNamespaces = make(chan struct{})
	f.enteredNamespaces = make(chan struct{}, 2)
	var release sync.Once
	writer := f.key("both-apps", "", "deployments:read", "deployments:write")
	input := map[string]any{"name": "custom", "email": "app@example.test"}
	var operations sync.WaitGroup
	defer func() {
		release.Do(func() { close(f.blockNamespaces) })
		operations.Wait()
	}()
	for _, app := range []store.Application{f.app, f.other} {
		operations.Add(1)
		go func() {
			defer operations.Done()
			f.request("POST", "/applications/"+app.ID+"/tls/issuers", writer, input, 201)
		}()
	}
	for range 2 {
		select {
		case <-f.enteredNamespaces:
		case <-time.After(5 * time.Second):
			t.Fatal("issuer operation did not reach its bounded Kubernetes request")
		}
	}
	response := f.request("POST", "/applications/"+f.app.ID+"/tls/issuers", writer, input, 503)
	if response.Header().Get("Retry-After") != "2" {
		t.Fatal("busy response omitted retry guidance")
	}
	f.request("GET", "/tls/issuers", writer, nil, 200)
	release.Do(func() { close(f.blockNamespaces) })
	operations.Wait()
	if f.writes != 4 {
		t.Fatalf("overload created extra resources: %d writes", f.writes)
	}
}
