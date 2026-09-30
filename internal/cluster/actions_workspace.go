package cluster

import (
	"context"
	_ "embed"
	"fmt"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
)

// ActionsWorkspaceProfile is installation-owned runtime configuration. It is
// never accepted from application TOML or copied to existing runner pods.
type ActionsWorkspaceProfile string

const (
	ActionsWorkspaceVFS              ActionsWorkspaceProfile = "vfs"
	ActionsWorkspaceSharedOverlay2V1 ActionsWorkspaceProfile = "shared-overlay2-v1"
	ActionsWorkspaceCapabilityLabel                          = "hakopod.io/actions-workspace"
)

//go:embed actions-workspace-start.py
var actionsWorkspaceStartupGuard string

func normalizedActionsWorkspaceProfile(profile ActionsWorkspaceProfile) (ActionsWorkspaceProfile, error) {
	switch profile {
	case "", ActionsWorkspaceVFS:
		return ActionsWorkspaceVFS, nil
	case ActionsWorkspaceSharedOverlay2V1:
		return profile, nil
	default:
		return "", fmt.Errorf("invalid Actions workspace profile")
	}
}

func githubActionsService(s spec.Service) bool {
	return s.Actions != nil && (s.Actions.Provider == "" || s.Actions.Provider == actions.ProviderGitHub)
}

func (p *actionsPlacement) matches(node corev1.Node) bool {
	return (p.nodeName == "" || node.Name == p.nodeName) &&
		labels.SelectorFromSet(p.selector).Matches(labels.Set(node.Labels)) && actionsNodeUnavailable(node, p.policy) == ""
}

func (c *Client) revalidateActionsWorkspacePlacement(ctx context.Context, placement *actionsPlacement) error {
	if placement.workspaceProfile != ActionsWorkspaceSharedOverlay2V1 {
		return nil
	}
	if placement.selector[ActionsWorkspaceCapabilityLabel] != string(ActionsWorkspaceSharedOverlay2V1) {
		return fmt.Errorf("Actions workspace capability is missing from placement")
	}
	if err := c.actionsRuntimeAvailable(ctx); err != nil {
		return err
	}
	nodes, err := c.actionsPlacementNodes(ctx, placement)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if placement.matches(node) {
			return nil
		}
	}
	return fmt.Errorf("The selected Actions workspace capability is no longer available")
}

// applyActionsWorkspaceProfile accepts only a fresh product pod. The caller
// owns capability admission; this function preserves its resource and security
// envelope while changing the three mount hints and Docker's storage driver.
func applyActionsWorkspaceProfile(pod *corev1.Pod, workspaceGiB int64, profile ActionsWorkspaceProfile) error {
	selected, err := normalizedActionsWorkspaceProfile(profile)
	if err != nil || selected == ActionsWorkspaceVFS {
		return err
	}
	if workspaceGiB < 2 || workspaceGiB > 16 || pod == nil || pod.UID != "" || pod.ResourceVersion != "" || !pod.CreationTimestamp.IsZero() || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ActionsRuntime {
		return fmt.Errorf("shared Actions workspace requires its bounded sandbox")
	}
	for key := range pod.Annotations {
		if strings.HasPrefix(key, "dev.gvisor.") {
			return fmt.Errorf("shared Actions workspace cannot override runtime annotations")
		}
	}
	if len(pod.Spec.Volumes) < 1 || pod.Spec.Volumes[0].Name != "runner" || pod.Spec.Volumes[0].EmptyDir == nil || pod.Spec.Volumes[0].EmptyDir.Medium != "" || pod.Spec.Volumes[0].EmptyDir.SizeLimit == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit.Cmp(resource.MustParse(fmt.Sprintf("%dGi", workspaceGiB))) != 0 {
		return fmt.Errorf("shared Actions workspace requires the unchanged bounded disk EmptyDir")
	}
	if len(pod.Spec.InitContainers) != 2 || pod.Spec.InitContainers[0].Name != "prepare" || !reflect.DeepEqual(pod.Spec.InitContainers[0].Command, []string{"sh", "-c", "cp -R /home/runner/. /runner/"}) || len(pod.Spec.Containers) != 1 || !reflect.DeepEqual(pod.Spec.Containers[0].Command, []string{"sh", "-c", "exec /usr/bin/python3 /usr/local/lib/hakopod/observe.py"}) {
		return fmt.Errorf("shared Actions workspace requires the unchanged prepare and GitHub observer")
	}
	if pod.Spec.InitContainers[1].Name != "docker" || pod.Spec.InitContainers[1].Image != ActionsDaemonImage || !reflect.DeepEqual(pod.Spec.InitContainers[1].Command, []string{"sh", "-c", actionsDaemon}) || strings.Count(actionsDaemon, "--storage-driver=vfs") != 1 {
		return fmt.Errorf("shared Actions workspace requires the unchanged pinned Docker daemon")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	// The qualified shim uses these exact hints for a shared disk filestore.
	// Keep kubelet's EmptyDir threshold and the existing one GiB headroom.
	pod.Annotations["dev.gvisor.spec.mount.runner.type"] = "bind"
	pod.Annotations["dev.gvisor.spec.mount.runner.share"] = "pod"
	pod.Annotations["dev.gvisor.spec.mount.runner.options"] = fmt.Sprintf("rw,rprivate,mode=0770,uid=1001,gid=1001,size=%dg", workspaceGiB+1)
	pod.Spec.InitContainers[1].Command[2] = strings.Replace(actionsDaemon, "--storage-driver=vfs", "--storage-driver=overlay2", 1)
	pod.Spec.InitContainers[0].Command[2] += " && printf 'shared-workspace-v1\\n' > /runner/.hakopod-shared-prepare"
	pod.Spec.Containers[0].Command = []string{"/usr/bin/python3", "-c", actionsWorkspaceStartupGuard + "\nimport os\nos.execv('/usr/bin/python3', ['/usr/bin/python3', '/usr/local/lib/hakopod/observe.py'])\n"}
	return nil
}
