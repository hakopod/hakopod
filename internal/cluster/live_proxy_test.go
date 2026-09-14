package cluster

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveProxyConfiguration(t *testing.T) {
	if os.Getenv("HAKOPOD_PROXY_TEST") != "1" {
		t.Skip("requires explicit named-development-cluster proxy test")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing proxy test outside named development cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := New(path, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalDirectives, err := generatedProxyDirectives(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := c.kube.AppsV1().Deployments("haproxy-controller").Get(ctx, "hakopod-ingress-kubernetes-ingress", metav1.GetOptions{})
	if err != nil || controller.Spec.Replicas == nil || *controller.Spec.Replicas < 1 || controller.Status.ReadyReplicas != *controller.Spec.Replicas {
		t.Fatal("development ingress controller must have stable, ready replicas before this test")
	}
	revision := time.Now().UnixNano()
	revisionValue := strconv.FormatInt(revision, 10)
	value := "31s"
	if original.Data["timeout-client"] == value {
		value = "32s"
	}
	settings := map[string]string{
		"timeout-client":       value,
		"timeout-check":        "7s",
		"timeout-queue":        "7s",
		"timeout-client-fin":   "11s",
		"timeout-server-fin":   "13s",
		"hard-stop-after":      "31m",
		"check-interval":       "17s",
		"pod-maxconn":          "128",
		"load-balance":         "leastconn",
		"http-connection-mode": "http-server-close",
		"dontlognull":          "false",
		"logasap":              "true",
		"abortonclose":         "true",
	}
	appliedData := maps.Clone(original.Data)
	if appliedData == nil {
		appliedData = map[string]string{}
	}
	maps.Copy(appliedData, settings)
	appliedAnnotations := maps.Clone(original.Annotations)
	appliedAnnotations["hakopod.io/proxy-revision"] = revisionValue
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		current, e := c.proxyConfigMap(cleanup)
		if e != nil {
			t.Error(e)
			return
		}
		if maps.Equal(current.Data, original.Data) && maps.Equal(current.Annotations, original.Annotations) {
			return
		}
		if !maps.Equal(current.Data, appliedData) || !maps.Equal(current.Annotations, appliedAnnotations) {
			t.Error("proxy changed outside test; preserving operator state")
			return
		}
		current.Data = original.Data
		current.Annotations = original.Annotations
		if _, e = c.kube.CoreV1().ConfigMaps(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
			return
		}
		if e = waitForProxyDirectives(cleanup, path, func(lines []string) error {
			if !slices.Equal(lines, originalDirectives) {
				return fmt.Errorf("generated proxy directives have not returned to their original values")
			}
			return nil
		}); e != nil {
			t.Error(e)
			return
		}
		restored, e := c.proxyConfigMap(cleanup)
		if e != nil || !maps.Equal(restored.Data, original.Data) || !maps.Equal(restored.Annotations, original.Annotations) {
			t.Error("original proxy ConfigMap was not restored", e)
			return
		}
		if e = validateGeneratedProxy(cleanup, path); e != nil {
			t.Error("restored HAProxy configuration failed validation", e)
			return
		}
		t.Log("Original ConfigMap data, annotations and generated proxy directives restored and validated.")
	})
	if _, err = c.ApplyProxyConfiguration(ctx, settings, original.ResourceVersion, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ApplyProxyConfiguration(ctx, settings, original.ResourceVersion, revision); err != nil {
		t.Fatal("replay failed", err)
	}
	clientTimeout, _ := time.ParseDuration(value)
	expected := []string{
		fmt.Sprintf("timeout client %d", clientTimeout.Milliseconds()),
		"timeout check 7000", "timeout queue 7000", "timeout client-fin 11000", "timeout server-fin 13000",
		"hard-stop-after 1860000", "balance leastconn", "option http-server-close",
		"no option dontlognull", "option logasap", "option abortonclose",
	}
	maxconn := fmt.Sprintf(" maxconn %d ", 128/int(*controller.Spec.Replicas))
	if err = waitForProxyDirectives(ctx, path, func(lines []string) error {
		for _, line := range expected {
			if !slices.Contains(lines, line) {
				return fmt.Errorf("generated proxy configuration is missing %q", line)
			}
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "default-server ") && strings.Contains(" "+line+" ", " inter 17000 ") && strings.Contains(" "+line+" ", maxconn) {
				return nil
			}
		}
		return fmt.Errorf("generated backend defaults are missing the reviewed check interval and connection cap")
	}); err != nil {
		t.Fatal(err)
	}
	if err = validateGeneratedProxy(ctx, path); err != nil {
		t.Fatal("generated HAProxy configuration did not validate", err)
	}
	t.Log("All 12 added proxy controls reached generated HAProxy directives, duplicate apply resumed, and haproxy -c passed.")
}

func validateGeneratedProxy(ctx context.Context, path string) error {
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", "haproxy-controller", "exec", "deployment/hakopod-ingress-kubernetes-ingress", "--", "haproxy", "-c", "-f", "/etc/haproxy/haproxy.cfg")
	return command.Run()
}

func waitForProxyDirectives(ctx context.Context, path string, check func([]string) error) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last error
	for {
		lines, err := generatedProxyDirectives(ctx, path)
		if err == nil {
			err = check(lines)
		}
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for HAProxy controller: %w (last check: %v)", ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

func generatedProxyDirectives(ctx context.Context, path string) ([]string, error) {
	// Read only the reviewed directives, without dumping routes, certificates or log targets.
	args := []string{"--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", "haproxy-controller", "exec", "deployment/hakopod-ingress-kubernetes-ingress", "--", "sed", "-n"}
	for _, name := range []string{"hard-stop-after", "timeout", "option", "no option", "balance", "default-server", "http-request deny"} {
		args = append(args, "-e", "/^[[:space:]]*"+name+" /p")
	}
	args = append(args, "/etc/haproxy/haproxy.cfg")
	command := exec.CommandContext(ctx, "kubectl", args...)
	stream, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, (1<<20)+1))
	if readErr != nil || len(data) > 1<<20 {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("could not read bounded HAProxy directives")
	}
	if err = command.Wait(); err != nil {
		return nil, err
	}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if normalized := strings.Join(strings.Fields(line), " "); normalized != "" {
			lines = append(lines, normalized)
		}
	}
	slices.Sort(lines)
	return lines, nil
}
