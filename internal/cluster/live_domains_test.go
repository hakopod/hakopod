package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
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

func TestLiveCustomDomainRoutingAndTLS(t *testing.T) {
	if os.Getenv("HAKOPOD_DOMAIN_TEST") != "1" {
		t.Skip("set HAKOPOD_DOMAIN_TEST=1 for a disposable custom-domain fixture")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("explicit k3d-hakopod-dev context required")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, PublicHTTPSPort: 18443, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	custom := fmt.Sprintf("domain-%d.example.test", time.Now().UnixNano())
	a, err := spec.Normalize(spec.Application{Name: "domain-fixture", Domains: map[string]string{custom: "web"}, Services: map[string]spec.Service{"web": {Image: hostTerminalImage, Public: true, Port: 8080, Healthcheck: "/", Command: []string{"/bin/sh", "-c"}, Args: []string{"mkdir -p /tmp/www; printf 'domain-fixture-ready' > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www"}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("domain-fixture-%d", time.Now().UnixNano()), Project: "domain-test", Environment: "test", OperationID: "domain-start", Revision: 1, Spec: a}
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "domains"
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		current, err := c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if err != nil || current.UID != ns.UID || current.Labels["hakopod.io/acceptance"] != "domains" {
			t.Error("domain fixture ownership changed")
			return
		}
		if err = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(current)); err != nil {
			t.Error(err)
		}
	})
	if _, err = c.Deploy(ctx, target, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	probe := func(client *http.Client, url string, want bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			request.Host = custom
			response, err := client.Do(request)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
				response.Body.Close()
				matched := response.StatusCode == 200 && strings.Contains(string(body), "domain-fixture-ready")
				if matched == want {
					return
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatal("custom domain routing did not reach requested state", want)
	}
	httpClient := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe(httpClient, "http://127.0.0.1:18080", false)
	c.options.ApprovedDomains = func(context.Context, string) (map[string]bool, error) { return map[string]bool{custom: true}, nil }
	if err = c.applyIngress(ctx, target, "web", target.Spec.Services["web"]); err != nil {
		t.Fatal(err)
	}
	probe(httpClient, "http://127.0.0.1:18080", true)
	wrongCert, wrongKey := testTLSCertificate(t, c.hostname(target, "web"), time.Now().Add(time.Hour))
	if _, err = c.PutTLSCertificate(ctx, target, "web", wrongCert, wrongKey); err == nil {
		t.Fatal("certificate lacking custom domain accepted")
	}
	cert, key := testTLSCertificate(t, c.hostname(target, "web"), time.Now().Add(time.Hour), custom)
	secret, err := c.PutTLSCertificate(ctx, target, "web", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["web"]
	svc.TLS = &spec.TLSConfig{Certificate: secret}
	target.Spec.Services["web"] = svc
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(cert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust, ServerName: custom, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	probe(&http.Client{Timeout: 2 * time.Second, Transport: transport}, "https://127.0.0.1:18443", true)
	target.Spec.Domains = nil
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	probe(httpClient, "http://127.0.0.1:18080", false)
	target.Spec.Domains = map[string]string{custom: "web"}
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	probe(&http.Client{Timeout: 2 * time.Second, Transport: transport}, "https://127.0.0.1:18443", true)
	t.Log("custom HTTP Host routing, verified custom SNI certificate, route removal and reapply reached actual fixture backend")
}
