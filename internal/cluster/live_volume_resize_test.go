package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveVolumeResizeShrinkGrowAndRejectOverflow(t *testing.T) {
	if os.Getenv("HAKOPOD_VOLUME_RESIZE_TEST") != "1" {
		t.Skip("opt-in volume resize acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	name := fmt.Sprintf("resize-live-%d", time.Now().Unix())
	svc := spec.Service{Image: volumeCopyImage, Port: 8080, Command: []string{"python", "-m", "http.server", "8080"}, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 2}}
	app, err := spec.Normalize(spec.Application{Name: name, Services: map[string]spec.Service{"db": svc, "other": svc}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: name, Project: "acceptance", Environment: "test", Spec: app, Revision: 1}
	defer func() {
		bounded, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		_ = c.DeletePreview(bounded, target)
	}()
	defer func() {
		if !t.Failed() {
			return
		}
		bounded, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		pods, e := c.kube.CoreV1().Pods(Namespace(name)).List(bounded, metav1.ListOptions{Limit: 100})
		if e == nil {
			for _, pod := range pods.Items {
				data, _ := json.Marshal(pod.Status)
				t.Logf("pod %s status: %s", pod.Name, data)
			}
		}
		events, e := c.kube.CoreV1().Events(Namespace(name)).List(bounded, metav1.ListOptions{Limit: 100})
		if e == nil {
			for _, event := range events.Items {
				t.Logf("event %s %s: %s", event.InvolvedObject.Name, event.Reason, event.Message)
			}
		}
	}()
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	run := func(script string) string {
		t.Helper()
		out, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "exec", "-n", Namespace(name), "deployment/db", "--", "python", "-c", script).CombinedOutput()
		if e != nil {
			t.Fatalf("data probe: %v %s", e, out)
		}
		return string(out)
	}
	run(`from pathlib import Path; import os; p=Path('/data/records');p.mkdir();(p/'data').write_bytes(b'unchanged database bytes\x00'*4000);os.link(p/'data',p/'hardlink');(p/'symlink').symlink_to('data')`)
	before := run(`import hashlib;print(hashlib.sha256(open('/data/records/data','rb').read()).hexdigest())`)
	currentClaim := "db-data"
	for index, size := range []int64{1, 3} {
		op := fmt.Sprintf("resize-test-%d-%d", time.Now().Unix(), index)
		next, plan, e := spec.ResizeVolume(app, currentClaim, size, op)
		if e != nil {
			t.Fatal(e)
		}
		// Exercise a namespace at its storage/PVC limit, as hosted workspaces
		// are. Migration must reserve one bounded temporary allowance.
		quota, e := c.kube.CoreV1().ResourceQuotas(Namespace(name)).Get(ctx, "hakopod-budget", metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims] = resource.MustParse("2")
		existingGiB := int64(4)
		if index == 1 {
			existingGiB = 3
		}
		quota.Spec.Hard[corev1.ResourceRequestsStorage] = resource.MustParse(fmt.Sprintf("%dGi", existingGiB))
		if _, e = c.kube.CoreV1().ResourceQuotas(Namespace(name)).Update(ctx, quota, metav1.UpdateOptions{}); e != nil {
			t.Fatal(e)
		}
		target.OperationID = op
		target.Spec = app
		journal, e := c.InspectVolumeResize(ctx, target, plan)
		if e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(3 * time.Minute)
		for {
			ready, e := c.PrepareVolumeResize(ctx, target, next, plan, &journal)
			if e != nil {
				t.Fatal(e)
			}
			if ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("copy did not finish")
			}
			time.Sleep(time.Second)
		}
		if !journal.Verified || journal.Bytes == 0 {
			t.Fatal("verification not recorded")
		}
		if _, e = c.kube.CoreV1().PersistentVolumeClaims(Namespace(name)).Get(ctx, currentClaim, metav1.GetOptions{}); e != nil {
			t.Fatal("original lost before cutover", e)
		}
		target.Previous = &app
		target.Spec = next
		target.Revision++
		if _, e = c.Deploy(ctx, target, nil); e != nil {
			t.Fatal(e)
		}
		after := run(`import hashlib,os;print(hashlib.sha256(open('/data/records/data','rb').read()).hexdigest());assert os.stat('/data/records/data').st_ino==os.stat('/data/records/hardlink').st_ino;assert os.readlink('/data/records/symlink')=='data'`)
		if before != after {
			t.Fatal("data changed during resize")
		}
		pvc, e := c.kube.CoreV1().PersistentVolumeClaims(Namespace(name)).Get(ctx, plan.TargetClaim, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		quantity := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if quantity.Value() != size<<30 {
			t.Fatal("wrong target capacity")
		}
		other, e := c.kube.AppsV1().Deployments(Namespace(name)).Get(ctx, "other", metav1.GetOptions{})
		if e != nil || other.Status.ReadyReplicas != 1 {
			t.Fatal("unrelated service unavailable", e)
		}
		for {
			e = c.DeleteVolumes(ctx, target, []string{currentClaim})
			if e == nil {
				break
			}
			if ctx.Err() != nil {
				t.Fatal(e)
			}
			time.Sleep(time.Second)
		}
		app = next
		currentClaim = plan.TargetClaim
	}
	// A sparse file exceeds the proposed 1 GiB size without consuming 2 GiB of
	// runner disk. The helper must reject it before writing or switching data.
	run(`with open('/data/too-large','wb') as f: f.truncate(2*1024**3)`)
	target.Spec = app
	target.OperationID = "resize-overflow-" + fmt.Sprint(time.Now().Unix())
	next, plan, err := spec.ResizeVolume(app, currentClaim, 1, target.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := c.InspectVolumeResize(ctx, target, plan)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		_, err = c.PrepareVolumeResize(ctx, target, next, plan, &journal)
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("overflow was not rejected")
		}
		time.Sleep(time.Second)
	}
	if !strings.Contains(err.Error(), "headroom") {
		t.Fatal("unexpected overflow error", err)
	}
	for {
		gone, e := c.RemoveResizeHelper(ctx, target, &journal)
		if e != nil {
			t.Fatal(e)
		}
		if gone {
			break
		}
		time.Sleep(time.Second)
	}
	if err = c.ResumeResizeServices(ctx, target, plan); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Minute)
	for {
		o, e := c.Observe(ctx, target)
		if e != nil {
			t.Fatal(e)
		}
		if o.Status == "healthy" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original did not resume")
		}
		time.Sleep(time.Second)
	}
	if before != run(`import hashlib;print(hashlib.sha256(open('/data/records/data','rb').read()).hexdigest())`) {
		t.Fatal("overflow damaged original")
	}
	run(`import os;os.unlink('/data/too-large')`)
	for {
		err = c.DeleteVolumes(ctx, target, []string{plan.TargetClaim})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
}
