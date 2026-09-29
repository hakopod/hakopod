package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// ActionsPodImages reads one bounded, service-scoped pod inventory. Intent and
// cleanup records are not evidence that a container is running. Keep old images
// while a draining container still runs, and stop reporting them when it exits.
func (c *Client) ActionsPodImages(ctx context.Context, t Target, service string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(labelsFor(t, service)).String(), Limit: 21})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) > 20 || pods.Continue != "" {
		return nil, fmt.Errorf("runner pod inventory exceeds observation bound")
	}
	images := map[string]bool{}
	for _, pod := range pods.Items {
		if err := owned(&pod, t); err != nil {
			return nil, err
		}
		if pod.Labels[serviceKey] != service || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ActionsRuntime {
			return nil, fmt.Errorf("unexpected runner pod scope or runtime")
		}
		for _, container := range pod.Status.ContainerStatuses {
			if container.Name != service || container.State.Running == nil {
				continue
			}
			image := strings.TrimPrefix(container.ImageID, "docker-pullable://")
			// Some CRI implementations expose an opaque local ID. The status image is
			// still a runtime observation; the pod's requested image is never substituted.
			if !strings.Contains(image, "@sha256:") {
				image = container.Image
			}
			if image != "" && len(image) <= 2048 {
				images[image] = true
			}
		}
	}
	result := make([]string, 0, len(images))
	for image := range images {
		result = append(result, image)
	}
	sort.Strings(result)
	return result, nil
}
