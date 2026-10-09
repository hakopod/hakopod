package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveProvisionedWorkloadSecretOwnership(t *testing.T) {
	if os.Getenv("HAKOPOD_PROVISIONED_SECRET_TEST") != "1" {
		t.Skip("opt-in provisioned secret acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	project, environment := "provisioned-secret-acceptance", "development"
	application := fmt.Sprintf("provisioned-secret-%d", time.Now().UnixNano())
	operationID := strings.Repeat("a", 32)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		for _, name := range []string{"owned-password", "foreign-password"} {
			if e := c.DeleteWorkloadSecret(clean, project, environment, application, name); e != nil {
				t.Error(e)
			}
		}
	})

	if err = c.PutProvisionedWorkloadSecret(ctx, project, environment, application, "owned-password", "generated-value", operationID); err != nil {
		t.Fatal(err)
	}
	if err = c.PutProvisionedWorkloadSecret(ctx, project, environment, application, "owned-password", "generated-value", operationID); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	if err = c.PutProvisionedWorkloadSecret(ctx, project, environment, application, "owned-password", "changed-value", operationID); err == nil {
		t.Fatal("same owner replaced the original value")
	}
	if err = c.PutWorkloadSecret(ctx, project, environment, application, "foreign-password", "foreign-value"); err != nil {
		t.Fatal(err)
	}
	if err = c.PutProvisionedWorkloadSecret(ctx, project, environment, application, "foreign-password", "generated-value", operationID); err == nil {
		t.Fatal("provisioning replaced a foreign secret")
	}
	foreign, err := c.kube.CoreV1().Secrets(PlatformNamespace).Get(ctx, workloadSecretName(project, environment, application, "foreign-password"), metav1.GetOptions{})
	if err != nil || string(foreign.Data["value"]) != "foreign-value" {
		t.Fatalf("foreign value changed: %v", err)
	}
}
