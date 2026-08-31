package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// BackupPod selects a unique running owned service pod, never a caller-chosen
// namespace or an arbitrary platform pod. Multi-replica databases need an
// explicit primary selection protocol and are refused by this implementation.
func (c *Client) BackupPod(ctx context.Context, t Target, service string) (TerminalOptions, types.UID, error) {
	options := TerminalOptions{Container: "app"}
	if _, ok := t.Spec.Services[service]; !ok {
		return options, "", fmt.Errorf("backup service is not in this application")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return options, "", err
	}
	if err = owned(ns, t); err != nil {
		return options, "", err
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service, Limit: 3})
	if err != nil {
		return options, "", err
	}
	if len(pods.Items) != 1 || pods.Continue != "" {
		return options, "", fmt.Errorf("backups require exactly one running database pod")
	}
	options.Pod = pods.Items[0].Name
	uid, err := c.TerminalPod(ctx, t, service, options)
	return options, uid, err
}

// BackupExec preserves binary dump bytes by disabling TTY, while retaining the
// same exact ownership and pod-UID checks as the interactive terminal.
func (c *Client) BackupExec(ctx context.Context, t Target, service string, options TerminalOptions, uid types.UID, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := options.Validate(); err != nil {
		return err
	}
	current, err := c.TerminalPod(ctx, t, service, options)
	if err != nil {
		return err
	}
	if current != uid {
		return fmt.Errorf("database pod changed; create a new backup or restore review")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("database exec transport is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace(Namespace(t.ApplicationID)).Name(options.Pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: options.Container, Command: options.Command, Stdin: stdin != nil, Stdout: stdout != nil, Stderr: stderr != nil, TTY: false}, scheme.ParameterCodec).URL()
	exec, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr, Tty: false})
}
