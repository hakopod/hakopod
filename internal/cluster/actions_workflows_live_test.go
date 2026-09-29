package cluster

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This opt-in acceptance consumes exactly one explicitly registered development
// runner. Its GitHub workflow prints a generated mask and compiles a C program;
// it cannot publish or deploy a customer application.
func TestManagedWorkflowObserverLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ACTIONS_OBSERVER_TEST") != "1" {
		t.Skip("requires the isolated workflow observer fixture")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only k3d-hakopod-dev is permitted")
	}
	image := os.Getenv("HAKOPOD_ACTIONS_OBSERVER_IMAGE")
	if image == "" {
		image = spec.ActionsRunnerImage
	}
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("supply the candidate digest")
	}
	readPrivate := func(name string) []byte {
		t.Helper()
		p := os.Getenv(name)
		info, e := os.Lstat(p)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 128<<10 {
			t.Fatal("supply a protected fixture file for", name)
		}
		data, e := os.ReadFile(p)
		if e != nil {
			t.Fatal("fixture unavailable")
		}
		return data
	}
	var identity struct {
		SlotID   string `json:"slot_id"`
		RunnerID int64  `json:"runner_id"`
	}
	data, err := os.ReadFile(os.Getenv("HAKOPOD_ACTIONS_OBSERVER_IDENTITY"))
	if err != nil || json.Unmarshal(data, &identity) != nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(identity.SlotID) || identity.RunnerID < 1 {
		t.Fatal("invalid development runner identity")
	}
	jit := strings.TrimSpace(string(readPrivate("HAKOPOD_ACTIONS_OBSERVER_JIT")))
	provider, err := actions.NewWithBudget(strings.TrimSpace(string(readPrivate("HAKOPOD_ACTIONS_OBSERVER_TOKEN"))), &actions.RequestBudget{})
	if err != nil {
		t.Fatal("GitHub fixture credential unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ActionsAvailable(ctx); err != nil {
		t.Fatal(err)
	}
	target := runnerTarget(t)
	target.ApplicationID = "actions-workflow-development"
	svc := target.Spec.Services["runner"]
	svc.Actions.Repository = "hakopod/hakopod"
	svc.Actions.WorkspaceSizeGiB = 4
	svc.Actions.TimeoutMinutes = 12
	svc.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "1500m", MemoryRequest: "4Gi", MemoryLimit: "4Gi"}
	target.Spec.Services["runner"] = svc
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if file := os.Getenv("HAKOPOD_ACTIONS_OBSERVER_DIAGNOSTICS"); file != "" {
			stream, e := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).GetLogs("actions-"+identity.SlotID, &corev1.PodLogOptions{Container: "runner", LimitBytes: ptr(int64(256 << 10))}).Stream(cleanup)
			if e == nil {
				data, _ := io.ReadAll(io.LimitReader(stream, 256<<10))
				stream.Close()
				_ = os.WriteFile(file, data, 0600)
			}
		}
		if e := c.kube.CoreV1().Namespaces().Delete(cleanup, Namespace(target.ApplicationID), metav1.DeleteOptions{}); e != nil {
			t.Error("development namespace cleanup failed")
		}
		// A CI read-only token cannot administer registrations. In external
		// cleanup mode the dispatch harness owns this one explicit runner ID.
		if os.Getenv("HAKOPOD_ACTIONS_OBSERVER_EXTERNAL_CLEANUP") != "1" {
			if e := provider.Delete(cleanup, actions.Target{Repository: "hakopod/hakopod"}, identity.RunnerID); e != nil {
				t.Error("development runner registration cleanup failed")
			}
		}
	})
	if err = c.SaveActionsConfig(ctx, target, "runner", identity.SlotID, jit); err != nil {
		t.Fatal("saving JIT fixture failed")
	}
	pod := actionsPod(target, "runner", identity.SlotID, svc)
	pod.Spec.InitContainers[0].Image = image
	pod.Spec.Containers[0].Image = image
	// The imported candidate exists only in the named development node.
	pod.Spec.InitContainers[0].ImagePullPolicy = corev1.PullIfNotPresent
	pod.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	if _, err = c.kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Log("Development runner started; dispatch the observer-only workflow")
	live := false
	var observation *actions.Observation
	for {
		current, e := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		output, e := c.ActionsWorkflowOutput(ctx, target, "runner", identity.SlotID)
		if e == nil {
			if output.Job != nil && output.Job.Valid(actions.Target{Repository: "hakopod/hakopod"}) {
				observation = output.Job
			}
			for _, line := range output.Lines {
				_, text, found := strings.Cut(line.Text, " ")
				if found && strings.HasPrefix(text, "workflow-visible-line-") {
					if !strings.HasSuffix(text, " ***") {
						t.Fatal("workflow output was not masked")
					}
					if current.Status.Phase == corev1.PodRunning {
						live = true
					}
				}
			}
		}
		if current.Status.Phase == corev1.PodFailed {
			for _, status := range append(current.Status.InitContainerStatuses, current.Status.ContainerStatuses...) {
				if ended := status.State.Terminated; ended != nil {
					t.Logf("%s: %s exit %d", status.Name, ended.Reason, ended.ExitCode)
				}
			}
			if file := os.Getenv("HAKOPOD_ACTIONS_OBSERVER_DIAGNOSTICS"); file != "" {
				stream, e := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "runner", LimitBytes: ptr(int64(256 << 10))}).Stream(ctx)
				if e == nil {
					data, _ := io.ReadAll(io.LimitReader(stream, 256<<10))
					stream.Close()
					_ = os.WriteFile(file, data, 0600)
				}
			}
			t.Fatal("development runner failed")
		}
		if current.Status.Phase == corev1.PodSucceeded {
			break
		}
		if e = sleepContext(ctx, time.Second); e != nil {
			t.Fatal("runner deadline exceeded")
		}
	}
	if !live || observation == nil {
		t.Fatalf("live masked output=%v, run identity=%v", live, observation != nil)
	}
	for {
		job, e := provider.AssignedJob(ctx, *observation, identity.RunnerID, "hakopod-"+identity.SlotID)
		if e == nil && job != nil && job.Status == "completed" {
			if job.Conclusion != "success" || len(job.Steps) < 3 {
				t.Fatal("development workflow did not succeed with steps")
			}
			logs, truncated, e := provider.JobLogs(ctx, observation.Repository, job.ID)
			if e == nil {
				found := false
				for _, line := range logs {
					if strings.Contains(line.Text, "native-toolchain-ok") {
						found = true
					}
				}
				if !found || truncated {
					t.Fatal("completed workflow output incomplete")
				}
				t.Logf("Verified live masked output and completed GitHub logs: run %d job %d attempt %d", observation.RunID, job.ID, observation.Attempt)
				return
			}
		}
		if e = sleepContext(ctx, 3*time.Second); e != nil {
			t.Fatal("GitHub completed logs not available before deadline")
		}
	}
}
