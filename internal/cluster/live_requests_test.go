package cluster

import (
	"context"
	"encoding/hex"
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

func TestLiveRequestsCaptureAndRouting(t *testing.T) {
	if os.Getenv("HAKOPOD_REQUESTS_TEST") != "1" {
		t.Skip("requires named development ingress")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing non-development cluster")
	}
	c, err := New(path, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress", AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	original, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		current, e := c.proxyConfigMap(cleanup)
		if e != nil {
			t.Error(e)
			return
		}
		expected := maps.Clone(original.Data)
		for k, v := range map[string]string{"log-format": current.Data["log-format"], "syslog-server": current.Data["syslog-server"], "logasap": "false"} {
			expected[k] = v
		}
		if !maps.Equal(current.Data, expected) {
			t.Error("operator configuration changed during test; preserving it")
			return
		}
		current.Data = maps.Clone(original.Data)
		c.kube.CoreV1().ConfigMaps(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{})
	})
	if err = c.ConfigureRequestLogs(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "request-fixture", Services: map[string]spec.Service{"api": {Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 8080, Public: true, Command: []string{"python", "-u", "-m", "http.server", "8080"}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("request-fixture-%d", time.Now().UnixNano()), Project: "request-test", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	host := c.hostname(target, "api")
	found := false
	for attempt := 0; attempt < 30; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18080/missing-request-fixture?token=never-log-query", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer never-log-header")
		res, e := client.Do(req)
		if e == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		time.Sleep(time.Second)
		batch, e := c.CollectRequests(ctx, map[string]time.Time{})
		if e != nil {
			continue
		}
		for _, entry := range batch.Entries {
			if entry.Host == host && entry.Path == "/missing-request-fixture" && entry.Backend == Namespace(target.ApplicationID)+"_svc_api_service" {
				if entry.Status != 404 || entry.DurationMS < 0 || strings.Contains(string(entry.JSON()), "never-log") {
					t.Fatalf("bad observed request %+v", entry)
				}
				if entry.Backend != Namespace(target.ApplicationID)+"_svc_api_service" {
					t.Fatalf("unexpected backend %s", entry.Backend)
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("real HTTP request was not captured from HAProxy")
	}
	// Verify redaction at the ingress source, not only in our parser.
	pods, e := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + c.options.ProxyRelease})
	if e != nil {
		t.Fatal(e)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		logs, e := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{LimitBytes: ptr(int64(1 << 20))}).Stream(ctx)
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(logs)
		logs.Close()
		if e != nil {
			t.Fatal(e)
		}
		for _, secret := range []string{"never-log-query", "never-log-header"} {
			if strings.Contains(string(data), secret) || strings.Contains(strings.ToLower(string(data)), hex.EncodeToString([]byte(secret))) {
				t.Fatal("secret recorded by ingress")
			}
		}
	}
	routing, err := c.RequestRouting(ctx, target, "api")
	if err != nil || len(routing.Routes) != 1 || len(routing.Endpoints) == 0 || routing.Routes[0].Host != host {
		t.Fatalf("observed route incomplete: %+v %v", routing, err)
	}
}
