package cluster

import (
	"context"
	"maps"
	"testing"

	"github.com/hakopod/hakopod/internal/requestlog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRequestsLoggerPreservesCustomConfiguration(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress"}}, Data: map[string]string{"timeout-client": "30s", "log-format": "operator custom"}}
	kube := fake.NewClientset(cm)
	c := &Client{kube: kube, options: Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"}}
	if c.ConfigureRequestLogs(context.Background()) == nil {
		t.Fatal("custom logging overwritten")
	}
	current, _ := kube.CoreV1().ConfigMaps("ingress").Get(context.Background(), "controller", metav1.GetOptions{})
	if !maps.Equal(cm.Data, current.Data) {
		t.Fatal("operator settings changed")
	}
	delete(current.Data, "log-format")
	kube.CoreV1().ConfigMaps("ingress").Update(context.Background(), current, metav1.UpdateOptions{})
	if err := c.ConfigureRequestLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ = kube.CoreV1().ConfigMaps("ingress").Get(context.Background(), "controller", metav1.GetOptions{})
	if current.Data["log-format"] != requestlog.Format || current.Data["timeout-client"] != "30s" {
		t.Fatal("format missing or unrelated setting changed")
	}
}
