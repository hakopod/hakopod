package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTerminalPollLive(t *testing.T) {
	if os.Getenv("HAKOPOD_TERMINAL_POLL_TEST") != "1" {
		t.Skip("enable disposable named development terminal poll acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev")
	}
	rest, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db := sourceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bootstrap, err := db.Bootstrap(ctx, "terminal-poll-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "terminal-poll-fixture", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "pods:exec"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	id := store.NewID()
	app := spec.Application{SchemaVersion: 1, Name: "terminal-poll-fixture", Services: map[string]spec.Service{"worker": {Image: "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0", Replicas: 1}}}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,revision,status,spec) VALUES($1,'demo','development',$2,1,'healthy',$3)", id, app.Name, store.JSON(app)); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(id))
	labels := map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/application-id": fmt.Sprintf("%x", hash[:16]), "hakopod.io/acceptance": "terminal-poll"}
	ns, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cluster.Namespace(id), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		current, e := kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if e != nil || current.UID != ns.UID || current.Labels["hakopod.io/acceptance"] != "terminal-poll" {
			t.Error("fixture ownership changed")
			return
		}
		if e = kube.CoreV1().Namespaces().Delete(clean, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); e != nil {
			t.Error(e)
			return
		}
		for clean.Err() == nil {
			_, e := kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Error("terminal fixture namespace cleanup timed out")
	})
	labels["hakopod.io/service"] = "worker"
	automount := false
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "terminal", Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &automount, Containers: []corev1.Container{{Name: "app", Image: app.Services["worker"].Image, Command: []string{"sleep", "3600"}, Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
	}}}}}
	if _, err = kube.CoreV1().Pods(ns.Name).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		p, e := kube.CoreV1().Pods(ns.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if e == nil && p.Status.Phase == corev1.PodRunning {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("fixture pod did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	server := &Server{Store: db, Cluster: runtime}
	handler := server.Handler()
	defer server.CloseTerminals()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	base := "/applications/" + id + "/services/worker/terminal"
	w := call("POST", base, `{"pod":"terminal","container":"app","command":["/bin/sh","-c","read -r value; printf 'received:%s\\n' \"$value\"; exit 7"]}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	base += "/" + session.ID
	cursor := "0"
	var output strings.Builder
	sent := false
	exited := false
	for !exited && ctx.Err() == nil {
		w = call("GET", base+"/poll?cursor="+cursor, "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var sample struct {
			Frames []terminalFrame `json:"frames"`
			Cursor string          `json:"next_cursor"`
			Done   bool            `json:"done"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &sample); err != nil {
			t.Fatal(err)
		}
		cursor = sample.Cursor
		for _, f := range sample.Frames {
			var frame struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Code int    `json:"code"`
			}
			json.Unmarshal(f.Data, &frame)
			if frame.Type == "output" {
				data, _ := base64.StdEncoding.DecodeString(frame.Data)
				output.Write(data)
			}
			if frame.Type == "exit" {
				if frame.Code != 7 {
					t.Fatal(frame.Code)
				}
				exited = true
			}
		}
		if !sent {
			w = call("POST", base+"/input", `{"data":"Zml4dHVyZS1pbnB1dAo="}`)
			if w.Code != 204 {
				t.Fatal(w.Code, w.Body.String())
			}
			sent = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !exited || !strings.Contains(output.String(), "received:fixture-input") {
		t.Fatal("real terminal output/exit missing", output.String())
	}
	t.Run("product-authority-revocation", func(t *testing.T) {
		principal, err := db.Authenticate(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		var revoked atomic.Bool
		scope := RuntimeScope{AllowMachine: true, Identity: principal.ID, Project: "demo", Environment: "development",
			Permissions: []string{"deployments:read", "deployments:write", "pods:exec"},
			Authorize: func(context.Context) error {
				if revoked.Load() {
					return store.ErrForbidden
				}
				return nil
			},
		}
		scopedCall := func(method, path, body string) *httptest.ResponseRecorder {
			requestContext, requestDone := context.WithCancel(ctx)
			defer requestDone()
			r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body)).WithContext(requestContext)
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, WithRuntimeScope(r, scope))
			return w
		}
		base := "/applications/" + id + "/services/worker/terminal"
		created := scopedCall("POST", base, `{"pod":"terminal","container":"app","command":["/bin/sh","-c","printf 'ready\\n'; read -r value"]}`)
		if created.Code != 201 {
			t.Fatal(created.Code, created.Body.String())
		}
		var session struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		base += "/" + session.ID
		server.terminalMu.Lock()
		x := server.terminals[session.ID]
		server.terminalMu.Unlock()
		if x == nil || x.ctx.Err() != nil {
			t.Fatal("creation request cancellation closed the terminal")
		}
		defer server.closeTerminal(x)
		if _, ok := x.ctx.Value(runtimeScopeKey{}).(RuntimeScope); !ok {
			t.Fatal("terminal lost its trusted product scope")
		}
		started := scopedCall("GET", base+"/poll", "")
		if started.Code != 200 {
			t.Fatal(started.Code, started.Body.String())
		}
		// Wait for actual container output before revoking the product authority.
		readyDeadline := time.Now().Add(10 * time.Second)
		for {
			x.mu.Lock()
			buffer := x.poll
			x.mu.Unlock()
			buffer.mu.Lock()
			ready := false
			for _, sample := range buffer.frames {
				var frame struct{ Type, Data string }
				if json.Unmarshal(sample.Data, &frame) == nil && frame.Type == "output" {
					data, _ := base64.StdEncoding.DecodeString(frame.Data)
					ready = ready || strings.Contains(string(data), "ready")
				}
			}
			buffer.mu.Unlock()
			if ready {
				break
			}
			if time.Now().After(readyDeadline) {
				t.Fatal("scoped terminal did not produce output")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if x.ctx.Err() != nil {
			t.Fatal("scoped terminal stopped before product authority revocation")
		}
		revoked.Store(true)
		select {
		case <-x.ctx.Done():
		case <-time.After(6 * time.Second):
			t.Fatal("terminal survived product authority revocation")
		}
		closedDeadline := time.Now().Add(3 * time.Second)
		for {
			x.poll.mu.Lock()
			closed := x.poll.done
			x.poll.mu.Unlock()
			if closed {
				break
			}
			if time.Now().After(closedDeadline) {
				t.Fatal("terminal output handler survived product authority revocation")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if _, err := db.Authenticate(ctx, key); err != nil {
			t.Fatal("product revocation changed the underlying key", err)
		}
		if denied := scopedCall("GET", base+"/poll", ""); denied.Code != 403 {
			t.Fatal("revoked product authority retained terminal output", denied.Code)
		}
	})
	// Exact key revocation must stop access even while completed output is retained.
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=(SELECT id FROM api_keys WHERE name='terminal-poll-fixture' LIMIT 1)"); err != nil {
		t.Fatal(err)
	}
	revoked := call("GET", base+"/poll?cursor="+cursor, "")
	if revoked.Code != 401 && revoked.Code != 403 {
		t.Fatal("revoked key retained output", revoked.Code)
	}
	server.CloseTerminals()
}
