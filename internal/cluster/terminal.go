package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type TerminalOptions struct {
	Pod       string   `json:"pod"`
	Container string   `json:"container,omitempty"`
	Command   []string `json:"command,omitempty"`
	Cols      uint16   `json:"cols,omitempty"`
	Rows      uint16   `json:"rows,omitempty"`
}

func (o *TerminalOptions) Validate() error {
	if o.Container == "" {
		o.Container = "app"
	}
	if len(o.Command) == 0 {
		o.Command = []string{"/bin/sh"}
	}
	if o.Cols == 0 {
		o.Cols = 100
	}
	if o.Rows == 0 {
		o.Rows = 30
	}
	if len(validation.IsDNS1123Subdomain(o.Pod)) > 0 || len(validation.IsDNS1123Label(o.Container)) > 0 {
		return fmt.Errorf("select a valid pod and container")
	}
	if len(o.Command) > 32 || o.Cols > 400 || o.Rows > 200 || o.Cols < 20 || o.Rows < 5 {
		return fmt.Errorf("terminal size or command exceeds supported bounds")
	}
	for _, arg := range o.Command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("command arguments must be at most 4096 bytes and contain no NUL")
		}
	}
	if o.Command[0] == "" {
		return fmt.Errorf("command executable is required")
	}
	return nil
}

// TerminalPod checks exact ownership and container state at session creation
// and again immediately before opening exec. Its UID fences pod replacement.
func (c *Client) TerminalPod(ctx context.Context, t Target, service string, o TerminalOptions) (types.UID, error) {
	if _, ok := t.Spec.Services[service]; !ok {
		return "", fmt.Errorf("service does not exist")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err = owned(ns, t); err != nil {
		return "", err
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, o.Pod, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err = owned(pod, t); err != nil {
		return "", err
	}
	if pod.Labels[serviceKey] != service || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return "", fmt.Errorf("selected pod must be a running pod in this service")
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == o.Container && status.State.Running != nil {
			return pod.UID, nil
		}
	}
	return "", fmt.Errorf("selected container is not running")
}
func (c *Client) Terminal(ctx context.Context, t Target, service string, o TerminalOptions, uid types.UID, stdin io.Reader, stdout io.Writer, sizes remotecommand.TerminalSizeQueue) error {
	current, err := c.TerminalPod(ctx, t, service, o)
	if err != nil {
		return err
	}
	if uid != current {
		return fmt.Errorf("pod was replaced; open a new session")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("interactive exec transport is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace(Namespace(t.ApplicationID)).Name(o.Pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: o.Container, Command: o.Command, Stdin: true, Stdout: true, Stderr: false, TTY: true}, scheme.ParameterCodec).URL()
	exec, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: true, TerminalSizeQueue: sizes})
}
