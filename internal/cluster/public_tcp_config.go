package cluster

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func ParsePublicTCPPorts(value string) ([]int32, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 4096 {
		return nil, fmt.Errorf("HAKOPOD_PUBLIC_TCP_PORTS exceeds the 4096-byte configuration limit")
	}
	values := strings.Split(value, ",")
	if len(values) > 256 {
		return nil, fmt.Errorf("HAKOPOD_PUBLIC_TCP_PORTS supports at most 256 ports")
	}
	ports := make([]int32, 0, len(values))
	seen := map[int32]bool{}
	for _, raw := range values {
		number, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
		port := int32(number)
		if err != nil || !spec.ValidPublicTCPPort(port) || seen[port] {
			return nil, fmt.Errorf("HAKOPOD_PUBLIC_TCP_PORTS requires unique, non-platform TCP ports separated by commas")
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}

var ErrPublicTCPDisabled = errors.New("public TCP is disabled")

const (
	DeploymentSelfHosted   = "self-hosted"
	DeploymentManagedCloud = "managed-cloud"
)

type PublicTCPPolicyStatus struct {
	Mode    string `json:"mode"`
	Allowed bool   `json:"allowed"`
	Message string `json:"message"`
}

func ParseDeploymentMode(value string) (string, error) {
	switch value {
	case "", DeploymentSelfHosted:
		return DeploymentSelfHosted, nil
	case DeploymentManagedCloud:
		return DeploymentManagedCloud, nil
	default:
		return "", fmt.Errorf("HAKOPOD_DEPLOYMENT_MODE must be self-hosted or managed-cloud")
	}
}

// Installation policy is independent of tenant roles and license entitlements.
func (c *Client) PublicTCPPolicy() PublicTCPPolicyStatus {
	mode, err := ParseDeploymentMode(c.options.DeploymentMode)
	if err != nil {
		return PublicTCPPolicyStatus{Mode: c.options.DeploymentMode, Allowed: false, Message: "Public TCP is unavailable because the installation deployment mode is invalid."}
	}
	if mode == DeploymentManagedCloud {
		return PublicTCPPolicyStatus{Mode: mode, Allowed: false, Message: "Public TCP is unavailable on managed cloud."}
	}
	return PublicTCPPolicyStatus{Mode: mode, Allowed: true, Message: "Public TCP is available on self-hosted installations after an administrator provisions and enables the required ports."}
}

// A mode switch never closes listeners implicitly. Refuse to start the managed
// service until application routes were removed and their reload acknowledged.
func (c *Client) ValidatePublicTCPInstallation(ctx context.Context) error {
	mode, err := ParseDeploymentMode(c.options.DeploymentMode)
	if err != nil {
		return err
	}
	if mode != DeploymentManagedCloud {
		return nil
	}
	if len(c.options.PublicTCPPorts) > 0 {
		return fmt.Errorf("managed-cloud installations cannot configure public TCP ports")
	}
	if c.dynamic == nil {
		return fmt.Errorf("cannot verify managed-cloud public TCP isolation without Kubernetes resource access")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, resource := range []schema.GroupVersionResource{publicTCPResource, {Group: "ingress.v1.haproxy.org", Version: "v1", Resource: "tcps"}} {
		list, err := c.dynamic.Resource(resource).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey, Limit: 256})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot verify managed-cloud public TCP route inventory")
		}
		if list.GetContinue() != "" || len(list.Items) > 256 {
			return fmt.Errorf("managed-cloud public TCP route inventory exceeds the 256-resource verification limit")
		}
		for _, item := range list.Items {
			labels := item.GetLabels()
			owner, isApplication := labels[ownerKey]
			if labels[managedBy] != "hakopod" || !isApplication {
				continue
			}
			if owner == "" || item.GetNamespace() != "hp-"+owner {
				return fmt.Errorf("managed-cloud public TCP route has invalid application ownership; operator review required")
			}
			models, found, err := unstructured.NestedSlice(item.Object, "spec")
			if err != nil || !found || models == nil {
				return fmt.Errorf("managed-cloud public TCP route cannot be inspected; operator review required")
			}
			if len(models) > 0 {
				return fmt.Errorf("remove existing application public TCP listeners and wait for their reload before switching to managed-cloud")
			}
			if item.GetAnnotations()["hakopod.io/tcp-acknowledged"] != publicTCPHash([]any{}) {
				return fmt.Errorf("application public TCP removal is not acknowledged; finish removal before switching to managed-cloud")
			}
		}
	}
	return nil
}
