package cluster

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// TestManagedActionsCandidateJobLive runs one real candidate GitHub job in the
// named development cluster. Supply a disposable JIT configuration, never the
// long-lived credential used by the controller to manage a pool.
func TestManagedActionsCandidateJobLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ACTIONS_CANDIDATE_TEST") != "1" {
		t.Skip("requires an explicitly issued single-job candidate registration")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only the named development cluster is permitted")
	}
	file := os.Getenv("HAKOPOD_ACTIONS_CANDIDATE_JIT_FILE")
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 128<<10 {
		t.Fatal("supply a mode-0600 file containing one disposable JIT configuration")
	}
	raw, err := os.ReadFile(file)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		t.Fatal("single-job configuration is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 115*time.Minute)
	defer cancel()
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ActionsAvailable(ctx); err != nil {
		t.Fatal(err)
	}
	target := runnerTarget(t)
	target.ApplicationID = "actions-candidate-development"
	s := target.Spec.Services["runner"]
	s.Actions.WorkspaceSizeGiB = 8
	s.Actions.TimeoutMinutes = 115
	s.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "1500m", MemoryRequest: "4Gi", MemoryLimit: "4Gi"}
	target.Spec.Services["runner"] = s
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if output := os.Getenv("HAKOPOD_ACTIONS_CANDIDATE_LOG_FILE"); output != "" {
			stream, err := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).GetLogs("actions-candidate-job", &corev1.PodLogOptions{Container: "runner", LimitBytes: ptr(int64(4 << 20))}).Stream(cleanup)
			if err == nil {
				defer stream.Close()
				logs, readErr := io.ReadAll(io.LimitReader(stream, 4<<20))
				if readErr == nil {
					f, createErr := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if createErr == nil {
						_, writeErr := f.Write(logs)
						closeErr := f.Close()
						if writeErr != nil || closeErr != nil {
							t.Error("saving candidate diagnostics failed")
						}
					} else {
						t.Error("create a new path for candidate diagnostics")
					}
				}
			}
		}
		if err := c.kube.CoreV1().Namespaces().Delete(cleanup, Namespace(target.ApplicationID), metav1.DeleteOptions{}); err != nil {
			t.Error(err)
		}
	})
	const id = "candidate-job"
	if err = c.SaveActionsConfig(ctx, target, "runner", id, strings.TrimSpace(string(raw))); err != nil {
		t.Fatal("saving the disposable runner configuration failed")
	}
	if err = c.StartActionsPod(ctx, target, "runner", id, s); err != nil {
		t.Fatal(err)
	}
	for {
		pod, err := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			t.Log("The candidate runner completed; verify the job conclusion separately in GitHub")
			return
		}
		if pod.Status.Phase == corev1.PodFailed {
			for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
				if ended := status.State.Terminated; ended != nil {
					t.Logf("%s: %s (exit %d)", status.Name, ended.Reason, ended.ExitCode)
				}
			}
			t.Fatalf("candidate runner failed: %s", pod.Status.Reason)
		}
		if err = sleepContext(ctx, 5*time.Second); err != nil {
			t.Fatal("candidate runner did not finish before its deadline")
		}
	}
}
