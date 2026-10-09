package cluster

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveParallelSecretRequirements(t *testing.T) {
	if os.Getenv("HAKOPOD_SECRET_REQUIREMENTS_TEST") != "1" {
		t.Skip("opt-in development secret requirements acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal("development cluster client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	app := requirementsApp(21)
	app.Name = fmt.Sprintf("secret-read-%d", time.Now().UnixNano())
	const project = "secret-read-acceptance"
	const environment = "test"
	created := []string{}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		for _, name := range created {
			key := workloadSecretName(project, environment, app.Name, name)
			item, err := c.kube.CoreV1().Secrets(PlatformNamespace).Get(clean, key, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				t.Error("cannot inspect owned fixture for cleanup")
				continue
			}
			if item.Labels["hakopod.io/secret-name"] != name || item.Labels["hakopod.io/secret-scope"] != secretScope(project, environment, app.Name) {
				t.Error("fixture ownership changed; cleanup refused")
				continue
			}
			if err = c.kube.CoreV1().Secrets(PlatformNamespace).Delete(clean, key, deleteOptions(item)); err != nil {
				t.Error("fixture cleanup failed")
				continue
			}
			if _, err = c.kube.CoreV1().Secrets(PlatformNamespace).Get(clean, key, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Error("fixture cleanup not verified")
			}
		}
	})
	for i := 0; i < 21; i++ {
		name := fmt.Sprintf("token-%03d", i)
		if err = c.CreateWorkloadSecret(ctx, project, environment, app.Name, name, "disposable-development-value"); err != nil {
			t.Fatal("fixture secret creation failed")
		}
		created = append(created, name)
	}
	start := time.Now()
	missing, err := c.MissingWorkloadSecrets(ctx, project, environment, app)
	if err != nil || len(missing) != 0 {
		t.Fatal("21-reference requirements did not resolve")
	}
	elapsed := time.Since(start)
	if elapsed >= 10*time.Second {
		t.Fatal("requirements exceeded the existing deadline")
	}
	missing, err = c.MissingWorkloadSecrets(ctx, "different-project", environment, app)
	if err != nil || len(missing) != 21 {
		t.Fatal("another project reused fixture secrets")
	}
	cancelled, done := context.WithCancel(ctx)
	done()
	if missing, err = c.MissingWorkloadSecrets(cancelled, project, environment, app); err == nil || missing != nil {
		t.Fatal("cancelled request returned requirements")
	}
	t.Logf("Resolved 21 exact references in %s; another project received 21 missing references; cancellation returned no partial result", elapsed)
}
