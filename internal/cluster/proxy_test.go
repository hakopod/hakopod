package cluster

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestProxyTypedSettings(t *testing.T) {
	for _, field := range ProxyFields() {
		t.Run(field.Name, func(t *testing.T) {
			for _, value := range []string{field.Example, ""} {
				if err := ValidateProxySettings(map[string]string{field.Name: value}); err != nil {
					t.Fatalf("example or reset rejected: %v", err)
				}
			}
			for _, value := range []string{"\n" + field.Example, field.Example + " ", strings.Repeat("x", 33)} {
				if err := ValidateProxySettings(map[string]string{field.Name: value}); err == nil {
					t.Fatal("whitespace or oversized value accepted")
				}
			}
		})
	}
	for _, tc := range []struct {
		name           string
		valid, invalid []string
	}{
		{"maxconn", []string{"16", "65536"}, []string{"0", "15", "65537", "+16", "1.5", "1e3"}},
		{"nbthread", []string{"1", "8"}, []string{"0", "9", "-1"}},
		{"pod-maxconn", []string{"16", "65536"}, []string{"0", "15", "65537", "+128", "128\nmaxconn 0"}},
		{"timeout-check", []string{"100ms", "5m"}, []string{"99ms", "301s", "5", "0.1s", "1m1s"}},
		{"timeout-queue", []string{"1ms", "1h"}, []string{"0s", "3601s"}},
		{"timeout-client-fin", []string{"1ms", "1h"}, []string{"0s", "3601s"}},
		{"timeout-server-fin", []string{"1ms", "1h"}, []string{"0s", "3601s"}},
		{"hard-stop-after", []string{"1s", "24h"}, []string{"999ms", "25h"}},
		{"check-interval", []string{"1s", "10m"}, []string{"999ms", "601s"}},
		{"timeout-client", []string{"1ms", "24h"}, []string{"0s", "25h", "1s1ms", "0.5s", "+1s", "1d", "1us", "100000000000000000000h"}},
		{"dontlognull", []string{"true", "false"}, []string{"1", "yes", "TRUE", "False"}},
		{"logasap", []string{"true", "false"}, []string{"0", "on", "FALSE"}},
		{"abortonclose", []string{"true", "false"}, []string{"1", "off", "True"}},
		{"load-balance", []string{"roundrobin", "static-rr", "leastconn", "first", "source", "random"}, []string{"roundrobin\ndefaults", "random(4)", "hdr(Host)", "uri whole", "least-connections"}},
		{"http-connection-mode", []string{"http-keep-alive", "http-server-close", "httpclose"}, []string{"keep-alive", "http-server-close log", "true"}},
	} {
		t.Run(tc.name+" bounds", func(t *testing.T) {
			for _, value := range tc.valid {
				if err := ValidateProxySettings(map[string]string{tc.name: value}); err != nil {
					t.Errorf("rejected %q: %v", value, err)
				}
			}
			for _, value := range tc.invalid {
				if err := ValidateProxySettings(map[string]string{tc.name: value}); err == nil {
					t.Errorf("accepted invalid %q", value)
				}
			}
		})
	}
	for _, values := range []map[string]string{nil, {}, {"global-config-snippet": "maxconn 0"}, {"log-format": "secret"}, {"load-balance": "leastconn", "check": "false"}} {
		if err := ValidateProxySettings(values); err == nil {
			t.Fatal("empty request or unsupported controller field accepted")
		}
	}
}

func TestProxyOwnershipConflictsAndRecovery(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", ResourceVersion: "100", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress", "operator.example/managed": "preserved"}}, Data: map[string]string{"timeout-client": "30s", "other-controller-setting": "preserved", "load-balance": "roundrobin", "dontlognull": "true"}}
	c := &Client{kube: fake.NewClientset(cm), options: Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"}}
	ctx := context.Background()
	values := map[string]string{"timeout-client": "31s", "load-balance": "leastconn", "dontlognull": "", "pod-maxconn": "128"}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "100", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "100", 1); err != nil {
		t.Fatal("accepted ConfigMap mutation did not resume idempotently", err)
	}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "stale", 2); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("stale review overwrote operator state")
	}
	current, err := c.kube.CoreV1().ConfigMaps("ingress").Get(ctx, "controller", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"timeout-client": "31s", "other-controller-setting": "preserved", "load-balance": "leastconn", "pod-maxconn": "128"}
	if !maps.Equal(current.Data, want) || current.Annotations["operator.example/managed"] != "preserved" {
		t.Fatal("patch failed to reset a field or preserve unrelated controller state")
	}
	observed, err := c.ProxyConfiguration(ctx)
	if err != nil || len(observed.Settings) != 3 || observed.Settings["load-balance"] != "leastconn" || observed.Settings["pod-maxconn"] != "128" {
		t.Fatal("observed settings omitted supported fields or exposed unmanaged fields")
	}
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"timeout-client": "32s", "pod-maxconn": "unlimited"}, "100", 2); err == nil {
		t.Fatal("invalid multi-field change accepted")
	}
	unchanged, err := c.kube.CoreV1().ConfigMaps("ingress").Get(ctx, "controller", metav1.GetOptions{})
	if err != nil || !maps.Equal(unchanged.Data, want) || unchanged.Annotations["hakopod.io/proxy-revision"] != "1" {
		t.Fatal("rejected change mutated controller state")
	}
	current.Annotations["meta.helm.sh/release-name"] = "foreign"
	_, _ = c.kube.CoreV1().ConfigMaps("ingress").Update(ctx, current, metav1.UpdateOptions{})
	if _, err := c.ProxyConfiguration(ctx); err == nil {
		t.Fatal("foreign ConfigMap adopted")
	}
	for _, v := range []map[string]string{{"global-config-snippet": "stats socket *:9000"}, {"maxconn": "9999999"}, {"nbthread": "0"}, {"timeout-client": "1s\nglobal"}, {"timeout-server": "0s"}} {
		if err := ValidateProxySettings(v); err == nil {
			t.Fatal("unsafe proxy setting accepted")
		}
	}
}

func TestContentLengthGuardOwnershipAndCloud(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", ResourceVersion: "100", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress"}}, Data: map[string]string{"backend-config-snippet": "# operator setting", "unrelated": "keep"}}
	c := &Client{kube: fake.NewClientset(cm), options: Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"}}
	ctx := context.Background()
	for _, v := range []string{"0", "-1", "1m", "10737418241", "123\nhttp-request allow"} {
		if ValidateProxySettings(map[string]string{"max-content-length": v}) == nil {
			t.Fatal("invalid limit accepted", v)
		}
	}
	if _, err := c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": "10485760"}, "100", 1); err != nil {
		t.Fatal(err)
	}
	observed, err := c.ProxyConfiguration(ctx)
	if err != nil || observed.Settings["max-content-length"] != "10485760" {
		t.Fatal("guard not observed", err)
	}
	c.options.DeploymentMode = DeploymentManagedCloud
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": ""}, "100", 2); err == nil {
		t.Fatal("cloud changed guard")
	}
	observed, err = c.ProxyConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range observed.Fields {
		if f.Name == "max-content-length" {
			t.Fatal("cloud exposed control")
		}
	}
	c.options.DeploymentMode = DeploymentSelfHosted
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": ""}, "100", 2); err != nil {
		t.Fatal(err)
	}
	current, _ := c.proxyConfigMap(ctx)
	if current.Data["backend-config-snippet"] != "# operator setting" || current.Data["unrelated"] != "keep" {
		t.Fatal("unrelated settings lost")
	}
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": "1024"}, "100", 3); err != nil {
		t.Fatal(err)
	}
	current, _ = c.proxyConfigMap(ctx)
	current.Data["backend-config-snippet"] = "# operator replaced snippet"
	if _, err = c.kube.CoreV1().ConfigMaps("ingress").Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ProxyConfiguration(ctx); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("missing guard falsely displayed as configured", err)
	}
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"max-content-length": "2048"}, "100", 4); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("external edit overwritten", err)
	}
}
