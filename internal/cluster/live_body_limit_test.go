package cluster

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveContentLengthGuard(t *testing.T) {
	if os.Getenv("HAKOPOD_BODY_LIMIT_TEST") != "1" {
		t.Skip("requires named development ingress")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing test outside named development cluster")
	}
	c, err := New(path, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress", AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, err := os.ReadFile("../../examples/shop/hakopod.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	delete(app.Services, "web")
	svc := app.Services["api"]
	svc.Public = true
	app.Services["api"] = svc
	target := Target{ApplicationID: fmt.Sprintf("body-limit-fixture-%d", time.Now().UnixNano()), Project: "body-limit-test", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	original, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision := time.Now().UnixNano()
	applied := original.DeepCopy()
	if applied.Annotations == nil {
		applied.Annotations = map[string]string{}
	}
	if applied.Data == nil {
		applied.Data = map[string]string{}
	}
	if err = setBodyLimit(applied, "1024"); err != nil {
		t.Fatal(err)
	}
	applied.Annotations["hakopod.io/proxy-revision"] = fmt.Sprint(revision)
	expectedStates := []*corev1.ConfigMap{applied}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		current, e := c.proxyConfigMap(cleanup)
		if e != nil {
			t.Error(e)
			return
		}
		if maps.Equal(current.Data, original.Data) && maps.Equal(current.Annotations, original.Annotations) {
			return
		}
		known := false
		for _, expected := range expectedStates {
			known = known || maps.Equal(current.Data, expected.Data) && maps.Equal(current.Annotations, expected.Annotations)
		}
		if !known {
			t.Error("intervening operator edit; preserving it")
			return
		}
		current.Data = maps.Clone(original.Data)
		if current.Data["backend-config-snippet"] == "" {
			current.Data["backend-config-snippet"] = "# No Hakopod Content-Length guard."
		}
		current.Annotations = original.Annotations
		if _, e = c.kube.CoreV1().ConfigMaps(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
			return
		}
		if e = waitForProxyDirectives(cleanup, path, func(lines []string) error {
			for _, line := range lines {
				if strings.Contains(line, "req.hdr(content-length)") && !strings.Contains(original.Data["backend-config-snippet"], line) {
					return fmt.Errorf("body guard still present")
				}
			}
			return nil
		}); e != nil {
			t.Error(e)
			return
		}
		// Restore exact ConfigMap contents after the explicit clearing update has
		// removed the generated rule. Do not overwrite intervening operator edits.
		if !maps.Equal(current.Data, original.Data) {
			latest, e := c.proxyConfigMap(cleanup)
			if e != nil {
				t.Error(e)
				return
			}
			if !maps.Equal(latest.Data, current.Data) || !maps.Equal(latest.Annotations, current.Annotations) {
				t.Error("proxy changed during cleanup; preserving operator state")
				return
			}
			latest.Data = maps.Clone(original.Data)
			if _, e = c.kube.CoreV1().ConfigMaps(latest.Namespace).Update(cleanup, latest, metav1.UpdateOptions{}); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": "1024"}, original.ResourceVersion, revision); err != nil {
		t.Fatal(err)
	}
	if err = waitForProxyDirectives(ctx, path, func(lines []string) error {
		for _, line := range lines {
			if strings.Contains(line, "req.hdr(content-length)") && strings.Contains(line, "1024") {
				return nil
			}
		}
		return fmt.Errorf("waiting for Content-Length ACL")
	}); err != nil {
		t.Fatal(err)
	}
	if err = validateGeneratedProxy(ctx, path); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	// The controller writes haproxy.cfg before the new worker is serving.
	// Wait for effective admission on a fresh connection before boundary checks.
	for attempt := 0; attempt < 40; attempt++ {
		request, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:18080/", strings.NewReader(strings.Repeat("x", 1025)))
		request.Host = c.hostname(target, "api")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == 413 {
			break
		}
		if attempt == 39 {
			t.Fatalf("Content-Length ACL never became effective: HTTP %d", response.StatusCode)
		}
		if err = sleepContext(ctx, 500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		size    int
		chunked bool
		denied  bool
	}{{1024, false, false}, {1025, false, true}, {2048, true, false}} {
		body := strings.NewReader(strings.Repeat("x", tc.size))
		request, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:18080/", body)
		request.Host = c.hostname(target, "api")
		if tc.chunked {
			request.ContentLength = -1
		}
		response, e := client.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if (response.StatusCode == 413) != tc.denied {
			t.Fatalf("size=%d chunked=%v: got %d", tc.size, tc.chunked, response.StatusCode)
		}
	}
	current, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resetExpected := current.DeepCopy()
	if err = setBodyLimit(resetExpected, ""); err != nil {
		t.Fatal(err)
	}
	resetExpected.Annotations["hakopod.io/proxy-revision"] = fmt.Sprint(revision + 1)
	expectedStates = append(expectedStates, resetExpected)
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": ""}, current.ResourceVersion, revision+1); err != nil {
		t.Fatal(err)
	}
	if err = waitForProxyDirectives(ctx, path, func(lines []string) error {
		for _, line := range lines {
			if strings.Contains(line, "req.hdr(content-length)") {
				return fmt.Errorf("reset guard still present")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Generated files can precede reload activation; use fresh connections and
	// observe the effective reset, without assuming the ConfigMap ack is readiness.
	for attempt := 0; attempt < 20; attempt++ {
		request, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:18080/", strings.NewReader(strings.Repeat("x", 1025)))
		request.Host = c.hostname(target, "api")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 413 {
			break
		}
		if attempt == 19 {
			t.Fatal("reset still denies declared upload")
		}
		if err = sleepContext(ctx, 500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("Generated HAProxy config valid; exact boundary allowed, larger declared body rejected with 413, documented streaming limitation verified")
}
