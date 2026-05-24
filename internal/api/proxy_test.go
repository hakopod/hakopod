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
	patch := func(revision int64, version, value string, want int) {
		t.Helper()
		input := map[string]any{"settings": map[string]string{"timeout-client": value}, "expected_revision": revision, "expected_resource_version": version}
		r := httptest.NewRequest("PATCH", "http://localhost/api/v1/settings/haproxy", bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+raw)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("PATCH got %d: %s", out.Code, out.Body.String())
		}
	}
	patch(0, "100", "31s", 202)
	patch(1, "100", "32s", 409)
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
	_, _ = db.Pool.Exec(ctx, `UPDATE runtime_resources SET metadata=jsonb_set(metadata,'{status}','"queued"') WHERE kind='proxy'`)
	server.reconcileProxy(ctx)
	mu.Lock()
	afterReplay := writes
	mu.Unlock()
	if afterReplay != 1 {
		t.Fatal("crash recovery duplicated ConfigMap mutation")
	}
	patch(1, "101", "32s", 202)
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
