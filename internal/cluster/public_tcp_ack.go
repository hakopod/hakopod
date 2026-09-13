package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type publicTCPRuntime struct {
	pid           string
	configuration string
	active        []string
}

func (c *Client) publicTCPPoke(ctx context.Context, t Target) (map[string]publicTCPRuntime, error) {
	if c.execConfig == nil || c.restClient() == nil {
		return nil, fmt.Errorf("public TCP requires access to the owned ingress runtime for reload acknowledgement")
	}
	list, err := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Running", LabelSelector: "app.kubernetes.io/name=kubernetes-ingress,app.kubernetes.io/instance=" + c.options.ProxyRelease, Limit: 8})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 || list.Continue != "" || len(list.Items) > 8 {
		return nil, fmt.Errorf("public TCP requires 1–8 ingress pods for bounded reload acknowledgement")
	}
	result := map[string]publicTCPRuntime{}
	for _, pod := range list.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			return nil, fmt.Errorf("waiting for ingress pod readiness before public TCP acknowledgement")
		}
		// The script emits only this application's frontends and active frontend
		// names. It never returns certificates, unrelated routes or complete stats.
		script := `set -eu
printf 'PID\n'
printf 'show info\n' | socat -t 2 - UNIX-CONNECT:/var/run/haproxy-runtime-api.sock | awk '/^Pid: / {print $2}'
printf 'CONFIG\n'
awk -v prefix="$1" '/^[^ \t]/ {show=($1=="frontend" && index($2,prefix)==1)} show {print}' /etc/haproxy/haproxy.cfg
printf 'ACTIVE\n'
printf 'show stat\n' | socat -t 2 - UNIX-CONNECT:/var/run/haproxy-runtime-api.sock | awk -F, -v prefix="$1" 'index($1,prefix)==1 && $2=="FRONTEND" && $18=="OPEN" {print $1}'
printf 'END\n'
`
		command := []string{"sh", "-c", script, "public-tcp-probe", "tcpcr_" + Namespace(t.ApplicationID) + "_hp-" + ownerID(t.ApplicationID) + "-"}
		request := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "kubernetes-ingress-controller", Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
		executor, e := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, request.URL())
		if e != nil {
			return nil, e
		}
		output := &tcpBoundedWriter{limit: 64 << 10}
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		e = executor.StreamWithContext(probeCtx, remotecommand.StreamOptions{Stdout: output, Stderr: io.Discard})
		cancel()
		if e != nil {
			return nil, fmt.Errorf("inspect ingress runtime: %w", e)
		}
		state, e := parsePublicTCPRuntime(output.String())
		if e != nil {
			return nil, e
		}
		result[string(pod.UID)] = state
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no live ingress pod acknowledged public TCP")
	}
	return result, nil
}

type tcpBoundedWriter struct {
	bytes.Buffer
	limit int
}

func (w *tcpBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, fmt.Errorf("ingress runtime response exceeds 64 KiB")
	}
	return w.Buffer.Write(p)
}

func parsePublicTCPRuntime(value string) (publicTCPRuntime, error) {
	state := publicTCPRuntime{}
	if !strings.HasPrefix(value, "PID\n") || !strings.HasSuffix(value, "END\n") {
		return state, fmt.Errorf("incomplete ingress runtime acknowledgement")
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "PID\n"), "CONFIG\n", 2)
	if len(parts) != 2 {
		return state, fmt.Errorf("invalid ingress runtime acknowledgement")
	}
	state.pid = strings.TrimSpace(parts[0])
	if _, err := strconv.ParseUint(state.pid, 10, 64); err != nil {
		return state, fmt.Errorf("missing active HAProxy process")
	}
	sections := strings.SplitN(parts[1], "ACTIVE\n", 2)
	if len(sections) != 2 {
		return state, fmt.Errorf("invalid ingress runtime acknowledgement")
	}
	state.configuration = sections[0]
	for _, name := range strings.Fields(strings.TrimSuffix(sections[1], "END\n")) {
		state.active = append(state.active, name)
	}
	return state, nil
}

func (c *Client) publicTCPBaseline(ctx context.Context, t Target) (map[string]string, error) {
	if c.publicTCPAck != nil {
		return nil, nil
	}
	states, err := c.publicTCPPoke(ctx, t)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for uid, state := range states {
		out[uid] = state.pid
	}
	return out, nil
}

func (c *Client) waitPublicTCPApplied(ctx context.Context, t Target, wanted []any, baseline map[string]string) error {
	if c.publicTCPAck != nil {
		return c.publicTCPAck(ctx, t, wanted, baseline)
	}
	wait, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last error
	for {
		states, err := c.publicTCPPoke(wait, t)
		if err == nil {
			for uid, state := range states {
				if previous, exists := baseline[uid]; exists && state.pid == previous {
					err = fmt.Errorf("waiting for the active HAProxy worker to reload")
					break
				}
				if err = validatePublicTCPRuntime(t, wanted, state); err != nil {
					break
				}
			}
		}
		if err == nil {
			return nil
		}
		if wait.Err() == nil || last == nil {
			last = err
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("public TCP reload was not acknowledged: %w (last check: %v)", wait.Err(), last)
		case <-ticker.C:
		}
	}
}

func validatePublicTCPRuntime(t Target, wanted []any, state publicTCPRuntime) error {
	sections := map[string][]string{}
	name := ""
	for _, line := range strings.Split(state.configuration, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "frontend" && len(fields) == 2 {
			name = fields[1]
			sections[name] = []string{}
			continue
		}
		if name != "" {
			sections[name] = append(sections[name], strings.Join(fields, " "))
		}
	}
	if len(sections) != len(wanted) || len(state.active) != len(wanted) {
		return fmt.Errorf("generated and active TCP frontend counts do not match the reviewed listeners")
	}
	for _, raw := range wanted {
		model := raw.(map[string]any)
		frontend := model["frontend"].(map[string]any)
		name := "tcpcr_" + Namespace(t.ApplicationID) + "_" + frontend["name"].(string)
		lines, exists := sections[name]
		if !exists || !slices.Contains(state.active, name) {
			return fmt.Errorf("reviewed TCP frontend has not become active")
		}
		bind := frontend["binds"].(map[string]any)["v4"].(map[string]any)
		port := bind["port"].(int64)
		source := frontend["acl_list"].([]any)[0].(map[string]any)["value"].(string)
		service := model["service"].(map[string]any)
		serviceName := service["name"].(string)
		servicePort := service["port"].(int64)
		portName := ""
		for _, p := range spec.ServicePorts(t.Spec.Services[serviceName]) {
			if int64(p.Port) == servicePort && p.Protocol == "TCP" {
				portName = p.Name
			}
		}
		expected := []string{"mode tcp", "maxconn 256", "timeout client 300000", fmt.Sprintf("bind 0.0.0.0:%d name v4", port), "acl allowed_source src " + source, "tcp-request connection reject unless allowed_source", "default_backend " + Namespace(t.ApplicationID) + "_svc_" + serviceName + "_" + portName}
		for _, line := range expected {
			if !slices.Contains(lines, line) {
				return fmt.Errorf("TCP frontend does not match reviewed bind, source ACL and backend")
			}
		}
		// Additional binds, ACL rules, or backend switches could bypass the reviewed
		// route, so acknowledge only the generated single-listener configuration.
		for _, prefix := range []string{"bind ", "acl ", "tcp-request ", "default_backend "} {
			count := 0
			for _, line := range lines {
				if strings.HasPrefix(line, prefix) {
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("TCP frontend contains unreviewed routing directives")
			}
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "use_backend ") {
				return fmt.Errorf("TCP frontend contains unreviewed backend switching")
			}
		}
	}
	return nil
}
