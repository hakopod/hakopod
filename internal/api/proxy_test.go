package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientconfig "k8s.io/client-go/tools/clientcmd/api"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestProxyDurableAcceptanceAndRevocation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "proxy-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	cm := corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", ResourceVersion: "100", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress"}}, Data: map[string]string{"timeout-client": "30s"}}
	var mu sync.Mutex
	writes := 0
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/api/v1/namespaces/ingress/configmaps/controller" {
			http.NotFound(w, r)
			return
		}
		if r.Method == "PUT" {
			var next corev1.ConfigMap
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_, _, decodeErr := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &next)
			if readErr != nil || decodeErr != nil {
				http.Error(w, "bad request", 400)
				return
			}
			if next.ResourceVersion != cm.ResourceVersion {
				http.Error(w, "conflict", 409)
				return
			}
			writes++
			next.ResourceVersion = strconv.Itoa(100 + writes)
			cm = next
		}
		write(w, 200, cm)
	}))
	defer kube.Close()
	file := filepath.Join(t.TempDir(), "kubeconfig")
	configuration := clientconfig.Config{Clusters: map[string]*clientconfig.Cluster{"test": {Server: kube.URL}}, Contexts: map[string]*clientconfig.Context{"test": {Cluster: "test"}}, CurrentContext: "test"}
	if err = clientcmd.WriteToFile(configuration, file); err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New(file, cluster.Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db, Cluster: c}
	handler := server.Handler()
	patch := func(revision int64, version string, settings map[string]string, want int) {
		t.Helper()
		input := map[string]any{"settings": settings, "expected_revision": revision, "expected_resource_version": version}
		r := httptest.NewRequest("PATCH", "http://localhost/api/v1/settings/haproxy", bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+raw)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("PATCH got %d: %s", out.Code, out.Body.String())
		}
	}
	settings := map[string]string{"timeout-client": "31s", "load-balance": "leastconn", "dontlognull": "false", "check-interval": "10s", "pod-maxconn": "128"}
	patch(0, "100", map[string]string{"load-balance": "leastconn", "check-interval": "1ms"}, 400)
	patch(0, "100", settings, 202)
	patch(1, "100", settings, 409)
	mu.Lock()
	initialWrites := writes
	mu.Unlock()
	if initialWrites != 0 {
		t.Fatal("acceptance mutated infrastructure before durable reconciliation")
	}
	server.reconcileProxy(ctx)
	row, err := db.RuntimeResource(ctx, "proxy", "", "", "haproxy")
	if err != nil {
		t.Fatal(err)
	}
	var result proxyChange
	_ = json.Unmarshal(row.Metadata, &result)
	if result.Status != "applied" {
		t.Fatalf("change did not reconcile: %s", result.Error)
	}
	mu.Lock()
	for name, value := range settings {
		if cm.Data[name] != value {
			t.Errorf("accepted setting %s did not reach Kubernetes", name)
		}
	}
	mu.Unlock()
	var audits, history int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='proxy.configured' AND identity_id=$1 AND key_id=$2", p.ID, p.KeyID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("accepted proxy change did not record exactly one attributed audit event", err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM runtime_resource_history WHERE kind='proxy' AND revision=1 AND identity_id=$1", p.ID).Scan(&history); err != nil || history != 1 {
		t.Fatal("accepted proxy change did not retain its reviewed revision", err)
	}
	_, _ = db.Pool.Exec(ctx, `UPDATE runtime_resources SET metadata=jsonb_set(metadata,'{status}','"queued"') WHERE kind='proxy'`)
	server.reconcileProxy(ctx)
	mu.Lock()
	afterReplay := writes
	mu.Unlock()
	if afterReplay != 1 {
		t.Fatal("crash recovery duplicated ConfigMap mutation")
	}
	server.Auth.DeploymentMode = cluster.DeploymentManagedCloud
	patch(1, "101", map[string]string{"max-content-length": "1024"}, 403)
	if _, err = db.Pool.Exec(ctx, `UPDATE runtime_resources SET metadata=$1 WHERE kind='proxy'`, store.JSON(proxyChange{Settings: map[string]string{"max-content-length": "1024"}, ResourceVersion: "101", KeyID: p.KeyID, Status: "queued"})); err != nil {
		t.Fatal(err)
	}
	server.reconcileProxy(ctx)
	cloudRow, _ := db.RuntimeResource(ctx, "proxy", "", "", "haproxy")
	var cloudChange proxyChange
	_ = json.Unmarshal(cloudRow.Metadata, &cloudChange)
	if cloudChange.Status != "failed" {
		t.Fatal("cloud replay accepted self-hosted guard")
	}
	server.Auth.DeploymentMode = cluster.DeploymentSelfHosted
	patch(1, "101", map[string]string{"timeout-client": "32s", "dontlognull": ""}, 202)
	_, _ = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID)
	server.reconcileProxy(ctx)
	row, _ = db.RuntimeResource(ctx, "proxy", "", "", "haproxy")
	_ = json.Unmarshal(row.Metadata, &result)
	if result.Status != "failed" {
		t.Fatal("revoked administrator retained queued infrastructure authority")
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 {
		t.Fatal("revoked change reached Kubernetes")
	}
}
