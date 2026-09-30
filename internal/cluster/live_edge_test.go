package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveEdgeTrafficProtection(t *testing.T) {
	if os.Getenv("HAKOPOD_EDGE_TEST") != "1" {
		t.Skip("requires explicit named-development-cluster edge acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing edge acceptance outside the named development cluster")
	}
	c, err := New(kubeconfig, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress", AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	original, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalPolicy, err := readEdgePolicy(original)
	if err != nil || originalPolicy.Enabled && len(originalPolicy.Rules) > 0 {
		t.Fatal("edge acceptance requires a development ingress without an active operator edge policy", err)
	}
	// Development fixtures report the actual backend and received path. Distinct
	// prefix backends expose disagreement between policy and controller routing.
	fixture := func(backend string) spec.Service {
		return spec.Service{Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 8080, Public: true, Command: []string{"python", "-u", "-c"}, Args: []string{`
import http.server, json, sys
class Fixture(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_GET(self):
        body = json.dumps({'fixture': 'hakopod-edge-development', 'backend': sys.argv[1], 'path': self.path}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
http.server.ThreadingHTTPServer(('0.0.0.0', 8080), Fixture).serve_forever()
`, backend}}
	}
	app, err := spec.Normalize(spec.Application{Name: "edge-fixture", Services: map[string]spec.Service{"api": fixture("public"), "private": fixture("private")}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("edge-fixture-%d", time.Now().UnixNano()), Project: "edge-test", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	t.Cleanup(func() {
		deleteLiveProxyFixture(t, c, kubeconfig, target)
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	// Change only this development fixture's owned Ingress, never an operator
	// route. The ordinary root route remains for rate, country and ACME probes.
	ingresses := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID))
	ingress, err := ingresses.Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = owned(ingress, target); err != nil {
		t.Fatal(err)
	}
	pathType := networkingv1.PathTypePrefix
	for _, entry := range []struct{ path, service string }{{"/private", "private"}, {"/public", "api"}} {
		ingress.Spec.Rules[0].HTTP.Paths = append(ingress.Spec.Rules[0].HTTP.Paths, networkingv1.HTTPIngressPath{Path: entry.path, PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: entry.service, Port: networkingv1.ServiceBackendPort{Number: 8080}}}})
	}
	if _, err = ingresses.Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	known := []*corev1.ConfigMap{}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		current, e := c.proxyConfigMap(cleanup)
		if e != nil {
			t.Error(e)
			return
		}
		if maps.Equal(current.Data, original.Data) && maps.Equal(current.Annotations, original.Annotations) {
			return
		}
		ours := false
		for _, expected := range known {
			ours = ours || maps.Equal(current.Data, expected.Data) && maps.Equal(current.Annotations, expected.Annotations)
		}
		if !ours {
			t.Error("edge configuration changed outside acceptance; preserving operator state")
			return
		}
		// Explicitly remove enforcement and observe that reload before deleting
		// owned comments. The controller does not reliably clear an empty snippet.
		if original.Annotations[edgePolicyAnnotation] == "" {
			disabled, _ := NormalizeEdgePolicy(EdgePolicy{})
			disabledRevision := time.Now().UnixNano()
			expected := current.DeepCopy()
			if e = setEdgePolicy(expected, disabled); e != nil {
				t.Error(e)
				return
			}
			expected.Annotations["hakopod.io/proxy-revision"] = fmt.Sprint(disabledRevision)
			if _, e = c.ApplyProxyConfigurationWithEdge(cleanup, nil, &disabled, current.ResourceVersion, disabledRevision); e != nil {
				t.Error("could not acknowledge edge cleanup", e)
				return
			}
			current, e = c.proxyConfigMap(cleanup)
			if e != nil {
				t.Error(e)
				return
			}
			if !maps.Equal(current.Data, expected.Data) || !maps.Equal(current.Annotations, expected.Annotations) {
				t.Error("edge configuration changed during cleanup; preserving operator state")
				return
			}
		}
		restore := current.DeepCopy()
		restore.Data = maps.Clone(original.Data)
		restore.Annotations = maps.Clone(original.Annotations)
		if restore, e = c.kube.CoreV1().ConfigMaps(restore.Namespace).Update(cleanup, restore, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
			return
		}
		if original.Annotations[edgePolicyAnnotation] != "" {
			if e = c.waitEdgeApplied(cleanup, restore, originalPolicy); e != nil {
				t.Error(e)
			}
		}
		if e = validateGeneratedProxy(cleanup, kubeconfig); e != nil {
			t.Error("restored HAProxy configuration did not validate", e)
		}
	})
	revision := time.Now().UnixNano()
	apply := func(policy EdgePolicy) {
		t.Helper()
		current, e := c.proxyConfigMap(ctx)
		if e != nil {
			t.Fatal(e)
		}
		normalized, e := NormalizeEdgePolicy(policy)
		if e != nil {
			t.Fatal(e)
		}
		expected := current.DeepCopy()
		if expected.Data == nil {
			expected.Data = map[string]string{}
		}
		if expected.Annotations == nil {
			expected.Annotations = map[string]string{}
		}
		if e = setEdgePolicy(expected, normalized); e != nil {
			t.Fatal(e)
		}
		revision++
		expected.Annotations["hakopod.io/proxy-revision"] = fmt.Sprint(revision)
		known = append(known, expected)
		if _, e = c.ApplyProxyConfigurationWithEdge(ctx, nil, &normalized, current.ResourceVersion, revision); e != nil {
			t.Fatal("edge reload was not acknowledged", e)
		}
		if _, e = c.ApplyProxyConfigurationWithEdge(ctx, nil, &normalized, current.ResourceVersion, revision); e != nil {
			t.Fatal("edge acknowledgement replay failed", e)
		}
		if _, e = c.ApplyProxyConfigurationWithEdge(ctx, nil, &normalized, "stale", revision+1); !errors.Is(e, ErrProxyConflict) {
			t.Fatal("stale review was accepted", e)
		}
		if e = validateGeneratedProxy(ctx, kubeconfig); e != nil {
			t.Fatal("generated edge configuration failed haproxy -c", e)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 1, MaxIdleConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	host := c.hostname(target, "api")
	probe := func(probeCtx context.Context, path string, headers http.Header) (int, []byte, error) {
		req, e := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://127.0.0.1:18080"+path, nil)
		if e != nil {
			return 0, nil, e
		}
		req.Host = host
		req.Header = headers.Clone()
		response, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, 4097))
		if e != nil || len(body) > 4096 {
			return response.StatusCode, nil, fmt.Errorf("invalid or oversized fixture response: %v", e)
		}
		return response.StatusCode, body, nil
	}
	request := func(path string, headers http.Header, want int) []byte {
		t.Helper()
		status, body, e := probe(ctx, path, headers)
		if e != nil {
			t.Fatal(e)
		}
		if want != 0 && status != want {
			t.Fatalf("%s: HTTP %d, wanted %d", path, status, want)
		}
		if want == 0 {
			return []byte(fmt.Sprint(status))
		}
		return body
	}
	type fixtureResponse struct {
		Fixture string `json:"fixture"`
		Backend string `json:"backend"`
		Path    string `json:"path"`
	}
	// Workload readiness precedes controller routing updates. Probe new
	// connections until each prefix reaches its own real development backend;
	// an old keep-alive connection can still belong to the preceding worker.
	waitForRoute := func(path, backend string) {
		t.Helper()
		readyCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		for {
			client.CloseIdleConnections()
			status, body, probeErr := probe(readyCtx, path, nil)
			var observed fixtureResponse
			if probeErr == nil && status == http.StatusOK {
				probeErr = json.Unmarshal(body, &observed)
				if probeErr == nil && observed.Fixture == "hakopod-edge-development" && observed.Backend == backend && observed.Path == path {
					return
				}
			}
			if e := sleepContext(readyCtx, 500*time.Millisecond); e != nil {
				t.Fatalf("development route %s did not reach backend %s: last HTTP %d, fixture %q, backend %q, path %q, error %v", path, backend, status, observed.Fixture, observed.Backend, observed.Path, probeErr)
			}
		}
	}
	waitForRoute("/public", "public")
	waitForRoute("/private", "private")
	var received fixtureResponse
	connection := EdgePolicy{Enabled: true, Rules: []EdgeRule{{ID: "private", Host: host, PathPrefix: "/private", DenyCIDRs: []string{"0.0.0.0/0", "::/0"}}, {ID: "rate", Host: host, PathPrefix: "/rate", RequestsPerSecond: 2}}}
	apply(connection)
	request("/private", http.Header{"X-Forwarded-For": {"192.0.2.1"}, "Cf-Connecting-Ip": {"192.0.2.1"}}, http.StatusForbidden)
	for _, path := range []string{
		"/%70rivate", "/%70ublic", "/x/../private", "/private//child", "/private/../public", "/public/../private",
		"/private%2fchild", "/private%5cchild", "/%2570rivate", "/public/..;/private", "/..;/private", "/public/..%3b/private", "/public/..%3B/private",
		"/.well-known/acme-challenge/../../private", "/.well-known/acme-challenge/%2e%2e/%2e%2e/private", "/.well-known/acme-challenge/token/../../../private", "/.well-known/acme-challenge/%2f..%2fprivate",
	} {
		request(path, nil, http.StatusBadRequest)
	}
	request("/.well-known/acme-challenge/test-token", nil, http.StatusOK)
	for _, queryPath := range []string{"/public?name=%61+z&separator=%2f&matrix=a;b&encoded=%3b", "/public?"} {
		if err = json.Unmarshal(request(queryPath, nil, http.StatusOK), &received); err != nil || received.Path != queryPath || received.Backend != "public" {
			t.Fatal("edge changed the canonical request target or backend", received, err)
		}
	}
	request("/rate", nil, http.StatusOK)
	limited := false
	for attempt := 0; attempt < 20; attempt++ {
		status := string(request("/rate", nil, 0))
		if status == "429" {
			limited = true
			break
		}
		if status != "200" {
			t.Fatal("unexpected rate response", status)
		}
	}
	if !limited {
		t.Fatal("same client did not hit the native rate limit")
	}
	request("/public", nil, http.StatusOK)
	// Private ranges here belong only to this explicit development fixture. Real
	// operators must use the actual addresses of their authenticating proxy.
	trusted := EdgePolicy{Enabled: true, ClientIPSource: "trusted_proxy", TrustedProxyCIDRs: []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128"}, ClientIPHeader: "CF-Connecting-IP", CountryHeader: "CF-IPCountry", Rules: []EdgeRule{
		{ID: "public", Host: host, PathPrefix: "/public"},
		{ID: "country", Host: host, PathPrefix: "/country", AllowCIDRs: []string{"198.51.100.0/24", "2001:db8::/32"}, DenyCIDRs: []string{"198.51.100.13/32"}, AllowCountries: []string{"US", "CA"}, DenyCountries: []string{"CA"}},
		{ID: "trusted-rate", Host: host, PathPrefix: "/rate", RequestsPerSecond: 2},
		{ID: "catch-all", Host: host, PathPrefix: "/", DenyCIDRs: []string{"0.0.0.0/0", "::/0"}},
	}}
	apply(trusted)
	headers := http.Header{"Cf-Connecting-Ip": {"198.51.100.20"}, "Cf-Ipcountry": {"US"}}
	request("/public", headers, http.StatusOK)
	request("/country", headers, http.StatusOK)
	request("/unlisted", headers, http.StatusForbidden)
	request("/.well-known/acme-challenge/test-token", nil, http.StatusOK)
	for _, changes := range []http.Header{
		{"Cf-Connecting-Ip": {"198.51.100.13"}, "Cf-Ipcountry": {"US"}},
		{"Cf-Connecting-Ip": {"198.51.100.20"}, "Cf-Ipcountry": {"CA"}},
		{"Cf-Connecting-Ip": {"198.51.100.20"}, "Cf-Ipcountry": {"XX"}},
		{"Cf-Connecting-Ip": {"198.51.100.20"}},
		{"Cf-Connecting-Ip": {"198.51.100.20", "198.51.100.21"}, "Cf-Ipcountry": {"US"}},
		{"Cf-Connecting-Ip": {"198.51.100.20, 198.51.100.21"}, "Cf-Ipcountry": {"US"}},
		{"Cf-Connecting-Ip": {"198.51.100.20"}, "Cf-Ipcountry": {"US", "US"}},
		{"Cf-Connecting-Ip": {"invalid"}, "Cf-Ipcountry": {"US"}},
		{"Cf-Connecting-Ip": {"198.51.100.20:1234"}, "Cf-Ipcountry": {"US"}},
	} {
		request("/country", changes, http.StatusForbidden)
	}
	request("/country", http.Header{"Cf-Connecting-Ip": {"2001:db8::20"}, "Cf-Ipcountry": {"US"}}, http.StatusOK)
	request("/rate", headers, http.StatusOK)
	limited = false
	for attempt := 0; attempt < 20; attempt++ {
		if string(request("/rate", headers, 0)) == "429" {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("trusted client did not hit its own rate limit")
	}
	other := headers.Clone()
	other.Set("CF-Connecting-IP", "198.51.100.21")
	request("/rate", other, http.StatusOK)
	request("/public", headers, http.StatusOK)
	trusted.TrustedProxyCIDRs = []string{"192.0.2.0/24"}
	apply(trusted)
	request("/country", headers, http.StatusForbidden)
	request("/.well-known/acme-challenge/test-token", nil, http.StatusOK)
	trusted.Enabled = false
	apply(trusted)
	request("/country", nil, http.StatusOK)
	request("/unlisted", nil, http.StatusOK)
	t.Log("Real HAProxy accepted the owned configuration; active workers acknowledged every revision; IP, country, first-match, rate, spoofed header, canonical path, separate prefix backends, ACME bypass and disable checks passed.")
}

func deleteLiveProxyFixture(t *testing.T, c *Client, kubeconfig string, target Target) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	namespace, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err == nil {
		if err = owned(namespace, target); err != nil {
			t.Error("fixture namespace ownership changed; preserving it", err)
			return
		}
		if err = c.kube.CoreV1().Namespaces().Delete(cleanup, namespace.Name, deleteOptions(namespace)); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
			return
		}
	} else if !apierrors.IsNotFound(err) {
		t.Error(err)
		return
	}
	for {
		_, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// Wait for the controller as well: the next proxy test must not take
			// its baseline while a deleted fixture's backend is still disappearing.
			output := &tcpBoundedWriter{limit: 4096}
			command := exec.CommandContext(cleanup, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", c.options.ProxyNamespace, "exec", "deployment/"+c.options.ProxyConfigMap, "--", "awk", "-v", "prefix="+Namespace(target.ApplicationID)+"_", `$1=="backend" && index($2,prefix)==1 {print $2}`, "/etc/haproxy/haproxy.cfg")
			command.Stdout = output
			command.Stderr = io.Discard
			if e := command.Run(); e == nil && output.Len() == 0 {
				return
			}
		} else if err != nil {
			t.Error("waiting for development fixture cleanup", err)
			return
		}
		if err := sleepContext(cleanup, 500*time.Millisecond); err != nil {
			t.Error("development fixture namespace or generated backend did not disappear", err)
			return
		}
	}
}
