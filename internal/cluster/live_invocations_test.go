package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLivePredefinedInvocations(t *testing.T) {
	if os.Getenv("HAKOPOD_INVOCATION_TEST") != "1" {
		t.Skip("opt-in predefined invocation acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	identity := strings.Repeat("a", 32)
	app, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("invoke-%d", time.Now().Unix()), Services: map[string]spec.Service{"report": {
		Image: sample.Services["api"].Image, Command: []string{"python", "-c"}, Args: []string{"import json,os,time; p=json.load(open('/run/hakopod/invocation/input.json')); assert os.getuid()!=0; assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token'); print('invocation-file-accepted',flush=True); time.sleep(90 if p['payload']=='cancel' else 1); raise SystemExit(7 if p['payload']=='fail' else 0)"},
		Job: &spec.Job{TimeoutSeconds: 120, Invocation: &spec.JobInvocation{AllowedIdentities: []string{identity}, InputKeys: []string{"payload"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: app.Name, OperationID: "invocation-template", Project: "acceptance", Environment: "test", Revision: 1, Spec: app}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return
		}
		if e != nil || owned(ns, target) != nil {
			t.Error("owned namespace cleanup unavailable", e)
			return
		}
		if e = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); e != nil {
			t.Error(e)
			return
		}
		for {
			_, e = c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				break
			}
			if e != nil || sleepContext(clean, time.Second) != nil {
				t.Error("invocation namespace did not finish cleanup", e)
				break
			}
		}
	}()
	observation, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) })
	if err != nil || len(observation.Services) != 1 || observation.Services[0].Status != "configured" {
		t.Fatal("template deployment failed", observation, err)
	}
	jobs, err := c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatal("deployment executed the template", err)
	}
	for index, mode := range []string{"success", "fail", "cancel"} {
		input := []byte(fmt.Sprintf(`{"payload":%q}`, mode))
		sum := sha256.Sum256(input)
		record := invocation.Record{ID: fmt.Sprintf("%032x", index+1), ApplicationID: target.ApplicationID, Service: "report", Project: target.Project, Environment: target.Environment, Revision: 1, IdentityID: identity, OwnerHash: strings.Repeat("b", 64), InputHash: fmt.Sprintf("%x", sum), Source: app}
		state, e := c.StartInvocation(ctx, record, input, nil)
		if e != nil {
			t.Fatal(mode, e)
		}
		record.RuntimeUID = state.RuntimeUID
		record.NamespaceUID = state.NamespaceUID
		// A new process can adopt this exact Job after an interrupted response.
		fresh, e := New(path, Options{RolloutTimeout: 60 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		recovered, e := fresh.StartInvocation(ctx, record, input, nil)
		if e != nil || recovered.RuntimeUID != record.RuntimeUID {
			t.Fatal("runtime recovery changed execution", e)
		}
		if mode == "cancel" {
			for {
				pods, e := c.kube.CoreV1().Pods(Namespace(record.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: invocationLabel + "=" + record.ID})
				if e != nil {
					t.Fatal(e)
				}
				running := false
				for _, pod := range pods.Items {
					running = running || pod.Status.Phase == "Running"
				}
				if running {
					break
				}
				if e = sleepContext(ctx, time.Second); e != nil {
					t.Fatal(e)
				}
			}
			record.CancelRequested = true
		} else {
			for state.Status == invocation.Running {
				if e = sleepContext(ctx, time.Second); e != nil {
					t.Fatal(e)
				}
				state, e = fresh.ObserveInvocation(ctx, record)
				if e != nil {
					t.Fatal(e)
				}
			}
			want := invocation.Succeeded
			if mode == "fail" {
				want = invocation.Failed
			}
			if state.Status != want || !strings.Contains(string(state.Log), "invocation-file-accepted") || state.ExitCode == nil {
				t.Fatal("terminal receipt/logs missing", state.Status, state.ExitCode)
			}
			if mode == "fail" && *state.ExitCode != 7 {
				t.Fatal("wrong failure exit code")
			}
			record.Status = state.Status // Durable-store behavior is qualified in its own suite.
		}
		for {
			finished, e := fresh.CleanupInvocation(ctx, record)
			if e != nil {
				t.Fatal(e)
			}
			if finished {
				break
			}
			if e = sleepContext(ctx, time.Second); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = fresh.StartInvocation(ctx, record, input, nil); e == nil {
			t.Fatal("cleaned recorded execution was recreated")
		}
		t.Log("native invocation", mode, "execution, recovery and cleanup passed")
	}
}
