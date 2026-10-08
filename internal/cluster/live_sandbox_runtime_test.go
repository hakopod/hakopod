package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveSandboxRuntimeGuardStreamsAndCleanup(t *testing.T) {
	if os.Getenv("HAKOPOD_SESSION_RUNTIME_TEST") != "1" {
		t.Skip("requires the named development cluster and pinned session images")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development cluster", err)
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%x", nonce)
	image := os.Getenv("HAKOPOD_SESSION_TEST_IMAGE")
	guard := os.Getenv("HAKOPOD_SESSION_GUARD_IMAGE")
	runtimeClass := os.Getenv("HAKOPOD_SESSION_TEST_RUNTIME_CLASS")
	identity := strings.Repeat("a", 32)
	application := "session-native-" + id[:8]
	app, err := spec.Normalize(spec.Application{Name: application, Services: map[string]spec.Service{"kernel": {Image: image, Command: []string{"python", "-I", "-u", "-c", "import time; time.sleep(300)"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "session-native", Resources: &spec.Resources{CPURequest: "25m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}, Session: &spec.SandboxSession{AllowedIdentities: []string{identity}, HelperCommand: []string{"python", "-I", "-u", "-c", "import sys; sys.stdout.buffer.write(sys.stdin.buffer.read()); sys.stdout.flush()"}, ReadyCommand: []string{"python", "-I", "-c", "pass"}, IdleSeconds: 300, LifetimeSeconds: 600}}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(path, Options{SessionGuardImage: guard, RuntimeProfileBindings: []RuntimeProfileBinding{{Name: "session-native", Project: "demo", Environment: "development", Application: application, Service: "kernel", RuntimeClass: runtimeClass, Handler: "runsc"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := sandbox.Record{ID: id, Generation: id, ApplicationID: application, Project: "demo", Environment: "development", Service: "kernel", Revision: 1, IdentityID: identity, OwnerHash: strings.Repeat("b", 64), RuntimeHash: strings.Repeat("c", 64), Image: image, Source: app, CreatedAt: now, ExpiresAt: now.Add(600 * time.Second), IdleUntil: now.Add(300 * time.Second)}
	target, svc, err := sandboxTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(application), Labels: labelsFor(target, "")}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for {
			done, e := c.CleanupSession(clean, r)
			if e != nil {
				t.Error("session cleanup failed", e)
				break
			}
			if done {
				break
			}
			if clean.Err() != nil {
				t.Error("session cleanup timed out")
				break
			}
			time.Sleep(time.Second)
		}
		if e := c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); e != nil && !apierrors.IsNotFound(e) {
			t.Error(e)
		}
		for {
			_, e := c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				break
			}
			if e != nil {
				t.Error(e)
				break
			}
			select {
			case <-clean.Done():
				t.Error("application namespace cleanup timed out")
				return
			case <-time.After(time.Second):
			}
		}
	})
	if err = c.applySessionTemplate(ctx, target, r.Service, svc); err != nil {
		t.Fatal(err)
	}
	state, err := c.StartSession(ctx, r, func(context.Context) error { return nil })
	r.NamespaceUID = state.NamespaceUID
	r.PodUID = state.PodUID
	if err != nil {
		t.Fatal("native session start", err)
	}
	for {
		state, err = c.ObserveSession(ctx, r)
		if err != nil {
			t.Fatal("native observation", err)
		}
		if state.Ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("kernel readiness timed out")
		case <-time.After(time.Second):
		}
	}
	r.ContainerID = state.ContainerID
	r.ImageID = state.ImageID
	var out bytes.Buffer
	payload := bytes.Repeat([]byte{0, 1, 2, 255}, 4096)
	if err = c.CallSession(ctx, r, bytes.NewReader(payload), &out); err != nil {
		t.Fatal("guarded binary session call", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("binary stream changed")
	}
	reader := &sandboxCountingReader{}
	stale := r
	stale.PodUID = "replaced"
	out.Reset()
	if err = c.CallSession(ctx, stale, reader, &out); err == nil || reader.calls.Load() != 0 || out.Len() != 0 {
		t.Fatal("stale session received caller input")
	}
	t.Logf("native identity namespace=%s pod=%s container=%s image=%s", r.NamespaceUID, r.PodUID, r.ContainerID, r.ImageID)
}
