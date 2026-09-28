package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/actions"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

func (c *Client) ActionsWorkflowOutput(ctx context.Context, t Target, service, id string) (actions.Output, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pod, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
	if err != nil {
		return actions.Output{}, err
	}
	if err = owned(pod, t); err != nil {
		return actions.Output{}, err
	}
	if pod.Labels["hakopod.io/service"] != service {
		return actions.Output{}, fmt.Errorf("runner belongs to another service")
	}
	stream, err := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: service, LimitBytes: ptr(int64(2 << 20))}).Stream(ctx)
	if err != nil {
		return actions.Output{}, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 2<<20))
	if err != nil {
		return actions.Output{}, err
	}
	return actions.ParseOutput(data), nil
}
