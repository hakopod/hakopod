package cluster

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type observabilityTransport func(*http.Request) (*http.Response, error)

func (f observabilityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiveKubeletFallbackAndLongLogs(t *testing.T) {
	if os.Getenv("HAKOPOD_OBSERVABILITY_TEST") != "1" {
		t.Skip("requires explicit development-cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	loaded, err := clientcmd.LoadFromFile(path)
	if err != nil || loaded.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev")
	}
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		return observabilityTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasPrefix(r.URL.Path, "/apis/metrics.k8s.io/") {
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServiceUnavailable","code":503}`))}, nil
			}
			return next.RoundTrip(r)
		})
	}
	c.kube, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	target := Target{ApplicationID: fmt.Sprintf("observability-dev-%d", time.Now().UnixNano()), Spec: spec.Application{Services: map[string]spec.Service{"api": {}}}}
	ns := Namespace(target.ApplicationID)
	_, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labelsFor(target, "")}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{})
	}()
	sample, _ := spec.Showcase()
	policy := deployment(target, "api", spec.Service{Image: sample.Services["api"].Image, Size: "small"}, time.Minute).Spec.Template.Spec.Containers[0].ImagePullPolicy
	if policy != corev1.PullAlways {
		t.Fatal("generated workload does not always pull")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", Labels: labelsFor(target, "api")}, Spec: corev1.PodSpec{NodeName: "k3d-hakopod-dev-server-0", Containers: []corev1.Container{{Name: "app", Image: sample.Services["api"].Image, ImagePullPolicy: policy, Command: []string{"python", "-u", "-c", "import time\nfor i in range(150):\n print('fixture line', i, flush=True)\n time.sleep(1)"}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("5m")}}}}}}
	if _, err = c.kube.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		p, e := c.kube.CoreV1().Pods(ns).Get(ctx, "api", metav1.GetOptions{})
		if e == nil && podReady(*p) {
			if p.Spec.Containers[0].ImagePullPolicy != corev1.PullAlways {
				t.Fatal("live workload pull policy changed")
			}
			break
		}
		if e == nil && p.Status.Phase == corev1.PodFailed {
			t.Fatalf("development fixture failed: %s", p.Status.Message)
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture not ready", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	stream, err := c.Logs(ctx, ns, "api", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reader := bufio.NewReader(stream)
	for time.Since(started) < 35*time.Second {
		line, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(line, "fixture line") {
			stream.Close()
			t.Fatal("live stream interrupted before 35 seconds", err, time.Since(started))
		}
	}
	stream.Close()
	for {
		runtime, e := c.ServiceRuntime(ctx, target, "api")
		if e != nil {
			t.Fatal(e)
		}
		if runtime.Metrics.Available && runtime.Metrics.Memory != nil && *runtime.Metrics.Memory > 0 && runtime.Metrics.CPU != nil {
			t.Logf("metrics-server unavailable: actual kubelet sample cpu_millicores=%.3f memory_bytes=%d; log stream remained open >30s", *runtime.Metrics.CPU, *runtime.Metrics.Memory)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no complete kubelet sample: %+v", runtime.Metrics)
		case <-time.After(2 * time.Second):
		}
	}
}
