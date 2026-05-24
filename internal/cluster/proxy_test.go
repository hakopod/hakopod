package cluster

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestProxyOwnershipConflictsAndRecovery(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", ResourceVersion: "100", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress"}}, Data: map[string]string{"timeout-client": "30s", "other-controller-setting": "preserved"}}
	c := &Client{kube: fake.NewClientset(cm), options: Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"}}
	ctx := context.Background()
	values := map[string]string{"timeout-client": "31s"}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "100", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "100", 1); err != nil {
		t.Fatal("accepted ConfigMap mutation did not resume idempotently", err)
	}
	if _, err := c.ApplyProxyConfiguration(ctx, values, "stale", 2); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("stale review overwrote operator state")
	}
	current, _ := c.kube.CoreV1().ConfigMaps("ingress").Get(ctx, "controller", metav1.GetOptions{})
	if current.Data["other-controller-setting"] != "preserved" {
		t.Fatal("unmanaged controller field overwritten")
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
