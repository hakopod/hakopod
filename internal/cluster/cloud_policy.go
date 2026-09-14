package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var ErrCloudLimit = errors.New("managed-cloud limit exceeded")

// CloudCapabilities describes enforced capacity for this runtime. Customers
// retain the initial single-node BYO policy.
type CloudCapabilities struct {
	Version                int      `json:"version"`
	Mode                   string   `json:"mode"`
	Enforced               bool     `json:"enforced"`
	NodeLimit              int      `json:"node_limit"`
	NodeCount              int      `json:"node_count"`
	NodeCountComplete      bool     `json:"node_count_complete"`
	ServicesPerApplication int      `json:"services_per_application"`
	ReplicasPerService     int32    `json:"replicas_per_service"`
	Profiles               []string `json:"profiles"`
	PublicTCP              bool     `json:"public_tcp"`
	GPU                    bool     `json:"gpu"`
	AWSIdentity            bool     `json:"aws_identity"`
}

func (c *Client) CloudMode() bool { return c.options.DeploymentMode == DeploymentManagedCloud }

func (c *Client) CloudCapabilities(ctx context.Context) (CloudCapabilities, error) {
	if !c.CloudMode() {
		return CloudCapabilities{}, fmt.Errorf("%w: this installation is self-hosted", ErrCloudLimit)
	}
	if c.kube == nil {
		return CloudCapabilities{}, errors.New("Kubernetes client is unavailable")
	}
	limit := c.options.OperatorNodeLimit
	if limit == 0 {
		limit = 1
	}
	if limit < 1 || limit > 2 {
		return CloudCapabilities{}, fmt.Errorf("%w: invalid operator node limit", ErrCloudLimit)
	}
	result := CloudCapabilities{Version: 1, Mode: DeploymentManagedCloud, Enforced: true, NodeLimit: limit, ServicesPerApplication: 10, ReplicasPerService: 3, Profiles: []string{"small", "medium", "large"}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: int64(limit + 1)})
	if err != nil {
		return CloudCapabilities{}, fmt.Errorf("read managed-cloud node capacity: %w", err)
	}
	result.PublicTCP = c.options.DedicatedPublicTCPNode != "" && nodes.Continue == "" && len(nodes.Items) == 1 && nodes.Items[0].Name == c.options.DedicatedPublicTCPNode
	result.NodeCount = len(nodes.Items)
	result.NodeCountComplete = nodes.Continue == ""
	if result.NodeCount > limit+1 {
		result.NodeCount = limit + 1
		result.NodeCountComplete = false
	}
	return result, nil
}

func (c *Client) ValidateCloudSpec(app spec.Application) error {
	if !c.CloudMode() {
		return nil
	}
	if len(app.Services) > 10 {
		return fmt.Errorf("%w: use at most 10 services per application", ErrCloudLimit)
	}
	for name, service := range app.Services {
		if service.GPU != nil || service.AWSIdentity != "" {
			return fmt.Errorf("%w: %s cannot use GPU or AWS workload identity on Cloud", ErrCloudLimit, name)
		}
		if service.Size != "" && service.Size != "small" && service.Size != "medium" && service.Size != "large" {
			return fmt.Errorf("%w: %s must use small, medium or large", ErrCloudLimit, name)
		}
		if service.Replicas < 0 || service.Replicas > 3 {
			return fmt.Errorf("%w: %s supports at most three replicas", ErrCloudLimit, name)
		}
		if a := service.Autoscaling; a != nil && (a.MinReplicas > 3 || a.MaxReplicas > 3) {
			return fmt.Errorf("%w: %s autoscaling supports at most three replicas", ErrCloudLimit, name)
		}
	}
	return nil
}

func (c *Client) ValidateCloudCapacity(ctx context.Context) error {
	if !c.CloudMode() {
		return nil
	}
	value, err := c.CloudCapabilities(ctx)
	if err != nil {
		return err
	}
	if value.NodeCount < 1 || value.NodeCount > value.NodeLimit || !value.NodeCountComplete {
		return fmt.Errorf("%w: this runtime requires between one and %d registered nodes", ErrCloudLimit, value.NodeLimit)
	}
	return nil
}
