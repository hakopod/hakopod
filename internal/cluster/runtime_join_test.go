package cluster

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Opt-in: one tainted 768 MiB temporary worker, joined using only the short-lived
// token this test creates. Token data is passed in a mode-0600 mounted file,
// never command arguments, process output or the control-plane's server token.
func TestEnrollmentLiveAgentJoin(t *testing.T) {
	if os.Getenv("HAKOPOD_TEST_JOIN_AGENT") != "1" {
		t.Skip("set HAKOPOD_TEST_JOIN_AGENT=1 for a temporary real worker join")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("worker join test requires the named k3d-hakopod-dev context")
	}
	const nodeName = "hakopod-enrollment-check"
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	clusterLabel, err := exec.CommandContext(ctx, "docker", "inspect", "k3d-hakopod-dev-server-0", "--format", `{{ index .Config.Labels "k3d.cluster" }}`).Output()
	if err != nil || strings.TrimSpace(string(clusterLabel)) != "hakopod-dev" {
		t.Fatal("named development server unavailable")
	}
	if exec.CommandContext(ctx, "docker", "inspect", nodeName).Run() == nil {
		t.Fatal("dedicated enrollment fixture container already exists; inspect before retry")
	}
	image, err := exec.CommandContext(ctx, "docker", "inspect", "k3d-hakopod-dev-server-0", "--format", "{{.Config.Image}}").Output()
	if err != nil || !strings.Contains(string(image), "@sha256:") {
		t.Fatal("development K3s image must be digest pinned")
	}
	client, err := New(kubeconfig, Options{SupervisorURL: "https://k3d-hakopod-dev-server-0:6443"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := client.CreateEnrollment(ctx, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.RevokeEnrollment(clean, enrollment.ID); err != nil {
			t.Error("test enrollment revocation failed")
		}
	}()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(tokenPath, []byte(enrollment.Token), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "-d", "--name", nodeName, "--hostname", nodeName, "--label", "com.hakopod.acceptance=enrollment", "--network", "k3d-hakopod-dev", "--privileged", "--memory", "768m", "--cpus", "1", "--tmpfs", "/run", "--tmpfs", "/var/run", "--mount", "type=bind,src=" + tokenPath + ",dst=/run/hakopod-token,readonly", strings.TrimSpace(string(image)), "agent", "--server", enrollment.Server, "--token-file", "/run/hakopod-token", "--node-name", nodeName, "--node-label", "hakopod.io/acceptance=enrollment", "--node-taint", "hakopod.io/acceptance=enrollment:NoSchedule"}
	if err = exec.CommandContext(ctx, "docker", args...).Run(); err != nil {
		t.Fatal("temporary enrollment worker could not start")
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pods, err := client.kube.CoreV1().Pods("").List(clean, metav1.ListOptions{FieldSelector: "spec.nodeName=" + nodeName, Limit: 1})
		if err != nil || len(pods.Items) > 0 {
			t.Error("unexpected pods on temporary node; fixture retained for diagnosis")
			return
		}
		marker, err := exec.CommandContext(clean, "docker", "inspect", nodeName, "--format", `{{ index .Config.Labels "com.hakopod.acceptance" }}`).Output()
		if err != nil || strings.TrimSpace(string(marker)) != "enrollment" {
			t.Error("fixture ownership changed; container retained")
			return
		}
		if err = exec.CommandContext(clean, "docker", "rm", "-f", nodeName).Run(); err != nil {
			t.Error("temporary worker cleanup failed")
			return
		}
		node, err := client.kube.CoreV1().Nodes().Get(clean, nodeName, metav1.GetOptions{})
		if err == nil {
			if node.Labels["hakopod.io/acceptance"] != "enrollment" {
				t.Error("node ownership changed; resource retained")
				return
			}
			if err = client.kube.CoreV1().Nodes().Delete(clean, nodeName, deleteOptions(node)); err != nil {
				t.Error("temporary Node resource cleanup failed")
			}
		}
	}()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		node, err := client.kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err == nil {
			tainted := false
			for _, taint := range node.Spec.Taints {
				if taint.Key == "hakopod.io/acceptance" && taint.Value == "enrollment" {
					tainted = true
				}
			}
			if !tainted {
				t.Fatal("temporary worker did not retain its isolation taint")
			}
			for _, condition := range node.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					t.Log("real temporary K3s agent joined and became Ready using the short-lived credential; cleanup follows")
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("temporary K3s agent did not become Ready before deadline")
}
