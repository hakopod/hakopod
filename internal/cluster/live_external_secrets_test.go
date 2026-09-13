package cluster

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This checks real Kubernetes writes with a deterministic in-process provider.
// HTTP protocol and access-policy behavior are covered by provider mock servers.
func TestLiveExternalSecretSnapshot(t *testing.T) {
	if os.Getenv("HAKOPOD_EXTERNAL_SECRETS_TEST") != "1" {
		t.Skip("set HAKOPOD_EXTERNAL_SECRETS_TEST=1 for the named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing external secret test outside the named development cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value := "fixture-first"
	unavailable := false
	calls := 0
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", ExternalSecrets: func(context.Context, string, string, spec.Application) (map[string]map[string][]byte, error) {
		calls++
		if unavailable {
			return nil, errors.New("fixture provider outage")
		}
		return map[string]map[string][]byte{"api": {"PASSWORD": []byte(value)}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := testTarget(t)
	target.ApplicationID = "secret-fixture-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	service := target.Spec.Services["api"]
	service.Secrets = map[string]spec.SecretRef{"PASSWORD": {Provider: "fixture", Key: "password"}}
	target.Spec.Services = map[string]spec.Service{"api": service}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}
	created, err := c.kube.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = c.kube.CoreV1().Namespaces().Delete(cleanup, created.Name, deleteOptions(created))
	})
	for _, next := range []string{"fixture-first", "fixture-rotated"} {
		value = next
		if err = c.snapshotWorkloadSecrets(ctx, &target); err != nil {
			t.Fatal(err)
		}
		if err = c.prepareWorkloadSecrets(ctx, target, "api", service); err != nil {
			t.Fatal(err)
		}
		secret, e := c.kube.CoreV1().Secrets(created.Name).Get(ctx, "api-environment", metav1.GetOptions{})
		if e != nil || string(secret.Data["PASSWORD"]) != next || owned(secret, target) != nil {
			t.Fatal("owned secret snapshot did not refresh")
		}
	}
	unavailable = true
	if _, err = c.Deploy(ctx, target, nil); err == nil || calls != 3 {
		t.Fatal("outage failed to stop deployment at provider resolution")
	}
	secret, err := c.kube.CoreV1().Secrets(created.Name).Get(ctx, "api-environment", metav1.GetOptions{})
	if err != nil || string(secret.Data["PASSWORD"]) != "fixture-rotated" {
		t.Fatal("outage destroyed last-good secret")
	}
}
