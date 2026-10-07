package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// PodExec runs one command without a shell wrapper, stdin, or TTY. Cancellation
// closes the transport. It does not prove that the remote process stopped.
func (c *Client) PodExec(ctx context.Context, t Target, service string, options TerminalOptions, uid types.UID, stdout, stderr io.Writer, authorize func(context.Context) error) error {
	if options.Pod == "" || options.Container == "" || len(options.Command) == 0 || uid == "" {
		return fmt.Errorf("pod, container, command and pod UID are required")
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if authorize == nil {
		return fmt.Errorf("command authorization check is required")
	}
	if err := authorize(ctx); err != nil {
		return err
	}
	current, err := c.TerminalPod(ctx, t, service, options)
	if err != nil {
		return err
	}
	if current != uid {
		return fmt.Errorf("pod changed before command execution")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("pod command transport is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace(Namespace(t.ApplicationID)).Name(options.Pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: options.Container, Command: options.Command, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return err
	}
	// Recheck the credential after target lookup and transport construction.
	if err := authorize(ctx); err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
}
