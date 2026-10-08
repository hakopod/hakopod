package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type sessionNativeTrackedInput struct {
	io.Reader
	reads atomic.Int32
}

func (r *sessionNativeTrackedInput) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.Reader.Read(p)
}

// This gate uses real TLS HTTP, PostgreSQL authority and persistent Python kernels.
// The caller must select the named development cluster and a qualified runsc image.
func TestLiveSandboxSessionHTTPStateAndCleanup(t *testing.T) {
	if os.Getenv("HAKOPOD_SESSION_HTTP_TEST") != "1" {
		t.Skip("requires the named development cluster and disposable PostgreSQL")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	guardImage := os.Getenv("HAKOPOD_SESSION_GUARD_IMAGE")
	workerImage := os.Getenv("HAKOPOD_SESSION_TEST_IMAGE")
	runtimeClass := os.Getenv("HAKOPOD_SESSION_TEST_RUNTIME_CLASS")
	if !sandbox.ValidImage(guardImage) || !sandbox.ValidImage(workerImage) || runtimeClass == "" {
		t.Fatal("requires pinned guard/worker images and an installed runsc RuntimeClass")
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
	db := actionsDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, err := db.Bootstrap(ctx, "session-http-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "session-http", Services: map[string]spec.Service{"kernel": {
		Image: workerImage, Command: []string{"python", "-I", "-u", "-c", sessionPythonWorker}, WorkingDir: "/workspace", RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "session-http",
		Resources:       &spec.Resources{CPURequest: "25m", CPULimit: "600m", MemoryRequest: "96Mi", MemoryLimit: "384Mi"},
		TemporaryMounts: []spec.TemporaryMount{{MountPath: "/workspace", SizeMiB: 64, Memory: true}, {MountPath: "/tmp", SizeMiB: 32, Memory: true}},
		Session:         &spec.SandboxSession{AllowedIdentities: []string{admin.ID}, HelperCommand: []string{"python", "-I", "-u", "-c", sessionPythonHelper}, ReadyCommand: []string{"python", "-I", "-c", sessionPythonReady}, IdleSeconds: 120, LifetimeSeconds: 360},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := cluster.New(path, cluster.Options{RolloutTimeout: 60 * time.Second, SessionGuardImage: guardImage, RuntimeProfileBindings: []cluster.RuntimeProfileBinding{{Name: "session-http", Project: "demo", Environment: "development", Application: app.Name, Service: "kernel", RuntimeClass: runtimeClass, Handler: "runsc"}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, ok := any(native).(sandbox.Runtime)
	if !ok {
		t.Fatal("native session runtime is unavailable")
	}
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	deployment, err := db.Accept(ctx, admin, "demo", "development", app, 0, "session-http-template", app)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "session-http-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"sessions:create", "sessions:read", "sessions:call", "sessions:delete"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ owner, key string }{{"tenant-one", "kernel-one"}, {"tenant-two", "kernel-two"}}
	namespace := cluster.Namespace(deployment.ApplicationID)
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for _, item := range cases {
			owner, _ := sandbox.HashKey(item.owner)
			records, e := db.ListSessions(clean, principal, deployment.ApplicationID, "kernel", owner, item.key)
			if e != nil {
				t.Error("cannot inspect session fixture cleanup", e)
				continue
			}
			for _, record := range records {
				for {
					removed, e := runtime.CleanupSession(clean, record)
					if e != nil {
						t.Error("owned session cleanup failed", e)
						break
					}
					if removed {
						break
					}
					if !invocationPause(clean, time.Second) {
						t.Error("session fixture cleanup exceeded its deadline")
						break
					}
				}
			}
		}
		ns, e := kube.CoreV1().Namespaces().Get(clean, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return
		}
		if e != nil || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/application-id"] != strings.TrimPrefix(namespace, "hp-") {
			t.Error("application fixture ownership changed", e)
			return
		}
		uid := ns.UID
		if e = kube.CoreV1().Namespaces().Delete(clean, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); e != nil && !apierrors.IsNotFound(e) {
			t.Error(e)
			return
		}
		for {
			_, e = kube.CoreV1().Namespaces().Get(clean, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			if e != nil || !invocationPause(clean, time.Second) {
				t.Error("application fixture cleanup not confirmed", e)
				return
			}
		}
	})
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	observation, err := native.Deploy(ctx, cluster.Target{ApplicationID: deployment.ApplicationID, OperationID: deployment.ID, Project: "demo", Environment: "development", Revision: 1, Spec: app}, nil)
	if err != nil {
		claim.Release()
		t.Fatal("session template deployment failed", err)
	}
	if err = claim.Finish(ctx, "succeeded", "", observation); err != nil {
		claim.Release()
		t.Fatal(err)
	}
	claim.Release()
	server := &Server{Store: db, Cluster: native, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}}
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	httpServer.Client().Timeout = 20 * time.Second
	workerCtx, stopWorkers := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { defer close(stopped); server.RunSessions(workerCtx) }()
	defer func() { stopWorkers(); <-stopped }()
	root := "/api/v1/applications/" + deployment.ApplicationID + "/services/kernel/sessions"
	request := func(method, path, owner, idem, generation string, body []byte, want int) *http.Response {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, httpServer.URL+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Hakopod-Owner-Scope", owner)
		req.Header.Set("X-Hakopod-Session-Generation", generation)
		req.Header.Set("Idempotency-Key", idem)
		req.Header.Set("Content-Type", "application/json")
		if strings.HasSuffix(path, "/call") {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		response, e := httpServer.Client().Do(req)
		if e != nil {
			t.Fatal("session HTTP request failed", e)
		}
		if response.StatusCode != want {
			response.Body.Close()
			t.Fatalf("%s %s returned HTTP %d, want %d", method, path, response.StatusCode, want)
		}
		return response
	}
	control := func(method, path, owner, idem, generation string, body any, want int, result any) {
		t.Helper()
		response := request(method, path, owner, idem, generation, store.JSON(body), want)
		defer response.Body.Close()
		if result != nil && json.NewDecoder(response.Body).Decode(result) != nil {
			t.Fatal("invalid session receipt")
		}
	}
	waitState := func(owner, id, want string) sandbox.Record {
		t.Helper()
		for {
			var state sandbox.Record
			control("GET", root+"/"+id, owner, "", "", nil, 200, &state)
			if state.Status == want {
				return state
			}
			if want == sandbox.Ready && (state.Status == sandbox.Closing || state.Status == sandbox.Closed) {
				t.Fatal("kernel did not become ready", state.Message)
			}
			if !invocationPause(ctx, time.Second) {
				t.Fatal("session state deadline exceeded", want)
			}
		}
	}
	created := make([]sandbox.Record, 2)
	for n, item := range cases {
		input := sandbox.CreateRequest{ExpectedRevision: 1, ExpectedImage: workerImage, RuntimeKey: item.key}
		control("POST", root, item.owner, "native-create-"+item.key, "", input, 202, &created[n])
		var replay sandbox.Record
		control("POST", root, item.owner, "native-create-"+item.key, "", input, 202, &replay)
		if replay.ID != created[n].ID {
			t.Fatal("creation replay replaced the kernel")
		}
		created[n] = waitState(item.owner, created[n].ID, sandbox.Ready)
	}
	runCell := func(owner string, record sandbox.Record, requestID, code string) string {
		t.Helper()
		response := request("POST", root+"/"+record.ID+"/call", owner, requestID, record.Generation, store.JSON(map[string]string{"op": "exec", "code": code}), 200)
		defer response.Body.Close()
		decoder := json.NewDecoder(io.LimitReader(response.Body, sandbox.MaxOutputBytes+1))
		var output strings.Builder
		complete := false
		for {
			var frame struct{ Status, Stdout, Error string }
			err := decoder.Decode(&frame)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal("cell stream failed", err)
			}
			if frame.Status == "error" {
				t.Fatal("fixture cell failed", frame.Error)
			}
			output.WriteString(frame.Stdout)
			complete = complete || frame.Status == "ok"
		}
		if !complete {
			t.Fatal("cell stream ended without completion")
		}
		return output.String()
	}
	one, two := created[0], created[1]
	ownerHash, _ := sandbox.HashKey(cases[0].owner)
	stale, err := db.ReadSession(ctx, principal, deployment.ApplicationID, "kernel", ownerHash, one.ID, "sessions:read")
	if err != nil {
		t.Fatal(err)
	}
	stale.PodUID = "replaced-pod-uid"
	inputProbe := &sessionNativeTrackedInput{Reader: strings.NewReader("caller bytes must not reach a replacement Pod")}
	var rejectedOutput bytes.Buffer
	if err = runtime.CallSession(ctx, stale, inputProbe, &rejectedOutput); err == nil || inputProbe.reads.Load() != 0 || rejectedOutput.Len() != 0 {
		t.Fatal("stale Pod identity consumed caller input or returned output", err)
	}
	if output := runCell(cases[0].owner, one, "cell-network-boundary", "import socket\nfor family in (socket.AF_INET,socket.AF_INET6,socket.AF_PACKET):\n try: socket.socket(family,socket.SOCK_STREAM)\n except PermissionError: print('denied')\n else: raise RuntimeError('IP socket permitted')"); strings.TrimSpace(output) != "denied\ndenied\ndenied" {
		t.Fatal("worker IP socket boundary failed")
	}
	if output := runCell(cases[0].owner, one, "cell-initial", "total=41\nopen('/workspace/state.txt','w').write('persisted')\nprint('first-cell')"); !strings.Contains(output, "first-cell") {
		t.Fatal("first cell output missing")
	}
	if output := runCell(cases[0].owner, one, "cell-followup", "total+=1\nprint(total)\nprint(open('/workspace/state.txt').read())"); !strings.Contains(output, "42") || !strings.Contains(output, "persisted") {
		t.Fatal("kernel variables or files did not persist")
	}
	if output := runCell(cases[1].owner, two, "cell-isolation", "import os\nprint('total' in globals())\nprint(os.path.exists('/workspace/state.txt'))"); strings.TrimSpace(output) != "False\nFalse" {
		t.Fatal("another owner received kernel state or files")
	}
	// Keep the saturating call alive so it can clean up its own children.
	// Starting another helper in the exhausted UID is not a recovery guarantee.
	limited := request("POST", root+"/"+one.ID+"/call", cases[0].owner, "cell-process-limit", one.Generation, store.JSON(map[string]string{"op": "exec", "code": "import subprocess,errno,time\nchildren=[]\ntry:\n for n in range(160):\n  try: children.append(subprocess.Popen(['/bin/sleep','60']))\n  except OSError as error:\n   assert error.errno==errno.EAGAIN\n   assert len(children)>0\n   print('process-limit-denied',flush=True)\n   break\n else: raise RuntimeError('process limit not enforced')\n time.sleep(10)\nfinally:\n for child in children: child.terminate()\n for child in children: child.wait(timeout=5)\n children=[]\n print('children-cleaned',flush=True)"}), 200)
	limitDecoder := json.NewDecoder(limited.Body)
	var limitFrame struct{ Status, Stdout, Error string }
	if err = limitDecoder.Decode(&limitFrame); err != nil || !strings.Contains(limitFrame.Stdout, "process-limit-denied") {
		limited.Body.Close()
		t.Fatal("first tenant did not reach its process limit", err)
	}
	limitReached := time.Now()
	if output := runCell(cases[1].owner, two, "cell-other-tenant-process", "import subprocess\nprint(subprocess.check_output(['/bin/echo','other-tenant-child'],text=True).strip())"); strings.TrimSpace(output) != "other-tenant-child" || time.Since(limitReached) >= 8*time.Second {
		limited.Body.Close()
		t.Fatal("other tenant did not start its child while the first tenant remained saturated")
	}
	childrenCleaned, limitComplete := false, false
	for {
		err = limitDecoder.Decode(&limitFrame)
		if err == io.EOF {
			break
		}
		if err != nil || limitFrame.Status == "error" {
			limited.Body.Close()
			t.Fatal("saturating call failed to clean up its children", err, limitFrame.Error)
		}
		childrenCleaned = childrenCleaned || strings.Contains(limitFrame.Stdout, "children-cleaned")
		limitComplete = limitComplete || limitFrame.Status == "ok"
	}
	limited.Body.Close()
	if !childrenCleaned || !limitComplete {
		t.Fatal("owned child cleanup was not confirmed")
	}
	control("GET", root+"/"+one.ID, cases[1].owner, "", "", nil, 404, nil)
	control("POST", root+"/"+one.ID+"/call", cases[1].owner, "cross-owner", one.Generation, map[string]string{"op": "exec", "code": "raise RuntimeError('must not run')"}, 404, nil)
	control("POST", root+"/"+one.ID+"/call", cases[0].owner, "wrong-generation", "stale", map[string]string{"op": "exec", "code": "raise RuntimeError('must not run')"}, 409, nil)
	control("POST", root+"/"+one.ID+"/call", cases[0].owner, "cell-initial", one.Generation, map[string]string{"op": "exec", "code": "total=0"}, 409, nil)
	var recovered struct {
		Items []sandbox.Record `json:"items"`
	}
	control("GET", root+"?runtime_key="+cases[0].key, cases[0].owner, "", "", nil, 200, &recovered)
	if len(recovered.Items) != 1 || recovered.Items[0].ID != one.ID || recovered.Items[0].Generation != one.Generation {
		t.Fatal("broker recovery lost the live generation")
	}
	control("POST", root+"/"+one.ID+"/heartbeat", cases[0].owner, "", one.Generation, nil, 200, nil)
	// A frame must arrive before the second print and its three-second delay.
	start := time.Now()
	streaming := request("POST", root+"/"+one.ID+"/call", cases[0].owner, "streaming-cell", one.Generation, store.JSON(map[string]string{"op": "exec", "code": "import time\nprint('stream-start',flush=True)\ntime.sleep(3)\nprint('stream-end',flush=True)"}), 200)
	decoder := json.NewDecoder(streaming.Body)
	var frame struct{ Status, Stdout string }
	if err = decoder.Decode(&frame); err != nil || !strings.Contains(frame.Stdout, "stream-start") || time.Since(start) >= 2500*time.Millisecond {
		streaming.Body.Close()
		t.Fatal("cell output was buffered until completion", err)
	}
	if _, err = io.Copy(io.Discard, streaming.Body); err != nil {
		streaming.Body.Close()
		t.Fatal(err)
	}
	streaming.Body.Close()
	// Cancel a live call after its first output frame. Partial success must abort.
	active := request("POST", root+"/"+one.ID+"/call", cases[0].owner, "cancelled-cell", one.Generation, store.JSON(map[string]string{"op": "exec", "code": "print('cancel-start',flush=True)\ntime.sleep(30)"}), 200)
	decoder = json.NewDecoder(active.Body)
	if err = decoder.Decode(&frame); err != nil {
		active.Body.Close()
		t.Fatal("cancel fixture did not start", err)
	}
	control("DELETE", root+"/"+one.ID, cases[0].owner, "", "", nil, 202, nil)
	_, streamErr := io.Copy(io.Discard, active.Body)
	active.Body.Close()
	if streamErr == nil {
		t.Fatal("cancelled output ended with a successful EOF")
	}
	waitState(cases[0].owner, one.ID, sandbox.Closed)
	control("DELETE", root+"/"+two.ID, cases[1].owner, "", "", nil, 202, nil)
	waitState(cases[1].owner, two.ID, sandbox.Closed)
	for n, record := range created {
		owner, _ := sandbox.HashKey(cases[n].owner)
		stored, e := db.ReadSession(ctx, principal, deployment.ApplicationID, "kernel", owner, record.ID, "sessions:read")
		if e != nil || stored.CleanupPending || stored.PodUID == "" || stored.ContainerID == "" || stored.ImageID == "" {
			t.Fatal("terminal receipt did not retain qualified runtime identity", e)
		}
		removed, e := runtime.CleanupSession(ctx, stored)
		if e != nil || !removed {
			t.Fatal("closed receipt preceded native cleanup", e)
		}
	}
	t.Log(fmt.Sprintf("TLS API, two Python kernels, variables, files, streaming, owner isolation, replay denial and cancelled-call cleanup passed in %s", time.Since(start).Round(time.Millisecond)))
}
