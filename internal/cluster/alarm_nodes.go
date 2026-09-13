package cluster

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AlarmNodeCondition struct {
	Type, Status, Message string
}

type AlarmNode struct {
	Name       string
	Conditions []AlarmNodeCondition
}

// AlarmNodes reads only the four node conditions used by native alarms. It
// avoids the pod inventory and metrics scans used by the node detail screen.
func (c *Client) AlarmNodes(ctx context.Context) ([]AlarmNode, error) {
	items := make([]AlarmNode, 0)
	continuation := ""
	for {
		page, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 100, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("observe node conditions: %w", err)
		}
		for _, node := range page.Items {
			if len(items) >= 200 {
				return nil, fmt.Errorf("cluster exceeds the 200-node alarm observation limit")
			}
			item := AlarmNode{Name: node.Name, Conditions: make([]AlarmNodeCondition, 0, 4)}
			for _, condition := range node.Status.Conditions {
				switch condition.Type {
				case corev1.NodeReady, corev1.NodeDiskPressure, corev1.NodeMemoryPressure, corev1.NodePIDPressure:
					item.Conditions = append(item.Conditions, AlarmNodeCondition{Type: string(condition.Type), Status: string(condition.Status), Message: boundedMessage(condition.Reason + ": " + condition.Message)})
				}
			}
			items = append(items, item)
		}
		continuation = page.Continue
		if continuation == "" {
			return items, nil
		}
	}
}
