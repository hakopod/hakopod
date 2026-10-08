package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// This opt-in gate uses real HTTP authentication, PostgreSQL receipts and K3s
// workloads. Only a random, ownership-checked namespace and test database change.
func TestLiveInvocationHTTPQueueAndCleanup(t *testing.T) {
	if os.Getenv("HAKOPOD_INVOCATION_HTTP_TEST") != "1" {
		t.Skip("requires named development cluster and disposable PostgreSQL")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 10 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.New(path, cluster.Options{RolloutTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	db := actionsDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	raw, err := db.Bootstrap(ctx, "http-invocation-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := spec.Showcase()
	app, err := spec.Normalize(spec.Application{Name: "invocation-http", Services: map[string]spec.Service{"report": {Image: sample.Services["api"].Image, Command: []string{"python", "-c"}, Args: []string{"import json,os,time; p=json.load(open('/run/hakopod/invocation/input.json')); assert os.getuid()!=0; assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token'); print('http-invocation-accepted',flush=True); time.sleep(90 if p['payload']=='cancel' else 1)"}, Job: &spec.Job{TimeoutSeconds: 120, Invocation: &spec.JobInvocation{AllowedIdentities: []string{admin.ID}, InputKeys: []string{"payload"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	d, err := db.Accept(ctx, admin, "demo", "development", app, 0, "native-http-template", app)
	if err != nil {
		t.Fatal(err)
	}
	namespace := cluster.Namespace(d.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		ns, e := kube.CoreV1().Namespaces().Get(clean, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return
		}
		if e != nil || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/application-id"] != strings.TrimPrefix(namespace, "hp-") {
			t.Error("namespace cleanup ownership could not be verified", e)
			return
		}
		uid := ns.UID
		if e = kube.CoreV1().Namespaces().Delete(clean, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); e != nil {
			t.Error(e)
			return
		}
		for {
			_, e = kube.CoreV1().Namespaces().Get(clean, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			if !invocationPause(clean, time.Second) {
				t.Error("namespace cleanup did not finish")
				return
			}
		}
	})
	deployment, err := db.Claim(ctx)
	if err != nil || deployment == nil {
		t.Fatal(err)
	}
	observation, err := runtime.Deploy(ctx, cluster.Target{ApplicationID: d.ApplicationID, OperationID: d.ID, Project: "demo", Environment: "development", Revision: 1, Spec: app}, func(cluster.Event) {})
	if err != nil {
		deployment.Release()
		t.Fatal("template deployment failed", err)
	}
	if err = deployment.Finish(ctx, "succeeded", "", observation); err != nil {
		deployment.Release()
		t.Fatal(err)
	}
	deployment.Release()
	_, token, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "native-http-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"jobs:invoke", "jobs:read", "jobs:cancel", "jobs:logs"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Cluster: runtime, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))}}
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	httpServer.Client().Timeout = 10 * time.Second
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.RunInvocations(workerCtx) }()
	defer func() { stop(); <-done }()
	root := "/api/v1/applications/" + d.ApplicationID + "/services/report/invocations"
	call := func(method, path, owner, idem string, body any, want int, result any) {
		t.Helper()
		request, e := http.NewRequestWithContext(ctx, method, httpServer.URL+path, bytes.NewReader(store.JSON(body)))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Hakopod-Owner-Scope", owner)
		request.Header.Set("Idempotency-Key", idem)
		response, e := httpServer.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s returned HTTP %d, want %d", method, response.StatusCode, want)
		}
		if result != nil && json.NewDecoder(response.Body).Decode(result) != nil {
			t.Fatal("invalid HTTP receipt")
		}
	}
	for _, mode := range []string{"success", "cancel"} {
		input := invocation.CreateRequest{ExpectedRevision: 1, ExpectedImage: app.Services["report"].Image, CorrelationID: "native-" + mode, OwnerScope: "team-one", Inputs: map[string]json.RawMessage{"payload": json.RawMessage(`"` + mode + `"`)}}
		var receipt invocation.Record
		call("POST", root, "team-one", "native-"+mode, input, 202, &receipt)
		var replay invocation.Record
		call("POST", root, "team-one", "native-"+mode, input, 202, &replay)
		if receipt.ID != replay.ID {
			t.Fatal("HTTP replay created duplicate work")
		}
		call("GET", root+"/"+receipt.ID, "other-team", "", nil, 404, nil)
		if mode == "cancel" {
			for {
				pods, e := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
				if e != nil {
					t.Fatal(e)
				}
				running := false
				for _, pod := range pods.Items {
					for _, owner := range pod.OwnerReferences {
						if owner.Name == "invocation-"+receipt.ID && pod.Status.Phase == "Running" {
							running = true
						}
					}
				}
				if running {
					break
				}
				if !invocationPause(ctx, time.Second) {
					t.Fatal("job did not start")
				}
			}
			call("POST", root+"/"+receipt.ID+"/cancel", "team-one", "", nil, 202, nil)
		}
		for {
			call("GET", root+"/"+receipt.ID, "team-one", "", nil, 200, &receipt)
			if receipt.Terminal() {
				break
			}
			if !invocationPause(ctx, time.Second) {
				t.Fatal("job did not reach cleaned terminal state")
			}
		}
		want := invocation.Succeeded
		if mode == "cancel" {
			want = invocation.Cancelled
		}
		if receipt.Status != want || receipt.CleanupPending {
			t.Fatal("unexpected final receipt", receipt.Status)
		}
		if mode == "success" {
			var logs struct {
				Text      string `json:"text"`
				Truncated bool   `json:"truncated"`
			}
			call("GET", root+"/"+receipt.ID+"/logs", "team-one", "", nil, 200, &logs)
			if !strings.Contains(logs.Text, "http-invocation-accepted") || logs.Truncated {
				t.Fatal("native output missing")
			}
		}
		jobs, e := kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
		if e != nil || len(jobs.Items) != 0 {
			t.Fatal("terminal HTTP status preceded job cleanup", e)
		}
		t.Log("HTTP queue, PostgreSQL receipt, native job and cleanup passed:", mode)
	}
}
