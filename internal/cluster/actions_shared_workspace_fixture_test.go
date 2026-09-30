package cluster

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// This experiment changes only development fixtures, never actionsPod or the
// installed runtime. The shim discovers the source from the owned Pod UID.
func actionsSharedWorkspaceFixture(pod *corev1.Pod, workspaceGiB int64, flag string) error {
	if flag != "" && flag != "0" && flag != "1" {
		return fmt.Errorf("shared workspace flag must be 0 or 1")
	}
	if flag != "1" {
		return nil
	}
	if workspaceGiB != 2 && workspaceGiB != 4 && workspaceGiB != 8 {
		return fmt.Errorf("shared workspace is limited to the existing development fixture sizes")
	}
	placement := corev1.PodSpec{}
	pinActionsNode(&placement, "k3d-hakopod-dev-server-0")
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ActionsRuntime || !reflect.DeepEqual(pod.Spec.Affinity, placement.Affinity) {
		return fmt.Errorf("shared workspace requires the named development runtime and node")
	}
	for key := range pod.Annotations {
		if strings.HasPrefix(key, "dev.gvisor.") {
			return fmt.Errorf("shared workspace cannot override existing runtime annotations")
		}
	}
	if len(pod.Spec.Volumes) < 1 || pod.Spec.Volumes[0].Name != "runner" || pod.Spec.Volumes[0].EmptyDir == nil || pod.Spec.Volumes[0].EmptyDir.Medium != "" || pod.Spec.Volumes[0].EmptyDir.SizeLimit == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit.Cmp(resource.MustParse(fmt.Sprintf("%dGi", workspaceGiB))) != 0 {
		return fmt.Errorf("shared workspace requires the unchanged bounded disk EmptyDir")
	}
	if len(pod.Spec.InitContainers) != 2 || pod.Spec.InitContainers[0].Name != "prepare" || !reflect.DeepEqual(pod.Spec.InitContainers[0].Command, []string{"sh", "-c", "cp -R /home/runner/. /runner/"}) || len(pod.Spec.Containers) != 1 || len(pod.Spec.Containers[0].Command) != 4 || !reflect.DeepEqual(pod.Spec.Containers[0].Command[:3], []string{"python3", "-u", "-c"}) {
		return fmt.Errorf("shared workspace requires the existing development prepare and Python workload")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	// bind is intentional: the pinned shim turns an empty EmptyDir hint into
	// internal tmpfs while retaining the child bind, selecting a disk filestore.
	// Supplying tmpfs here would instead select memory backing.
	pod.Annotations["dev.gvisor.spec.mount.runner.type"] = "bind"
	pod.Annotations["dev.gvisor.spec.mount.runner.share"] = "pod"
	// Leave the EmptyDir eviction threshold unchanged. The extra GiB matches
	// the existing runner ephemeral limit and lets kubelet observe overage.
	pod.Annotations["dev.gvisor.spec.mount.runner.options"] = fmt.Sprintf("rw,rprivate,mode=0770,uid=1001,gid=1001,size=%dg", workspaceGiB+1)
	pod.Spec.InitContainers[0].Command[2] += " && printf 'shared-workspace-v1\\n' > /runner/.hakopod-shared-prepare"
	pod.Spec.Containers[0].Command[3] = `import pathlib, time
_shared_root = pathlib.Path('/home/runner')
if (_shared_root / '.hakopod-shared-prepare').read_text() != 'shared-workspace-v1\n':
    raise RuntimeError('Prepare workspace was not shared with the runner')
(_shared_root / '.hakopod-shared-ready').touch(exist_ok=False)
for _shared_attempt in range(240):
    if (_shared_root / '.hakopod-shared-continue').exists():
        break
    time.sleep(1)
else:
    raise RuntimeError('Shared workspace host checks did not release the development fixture')
` + pod.Spec.Containers[0].Command[3]
	return nil
}

func TestActionsSharedWorkspaceFixtureIsExplicitAndBounded(t *testing.T) {
	for _, size := range []int64{2, 4, 8} {
		target := runnerTarget(t)
		s := target.Spec.Services["runner"]
		s.NodeName = "k3d-hakopod-dev-server-0"
		s.Actions.WorkspaceSizeGiB = size
		pod := actionsPod(target, "runner", "runtime-fixture", s)
		pod.Spec.Containers[0].Command = []string{"python3", "-u", "-c", "print('original fixture')"}
		original := pod.DeepCopy()
		for _, flag := range []string{"", "0"} {
			if err := actionsSharedWorkspaceFixture(pod, size, flag); err != nil || !reflect.DeepEqual(pod, original) {
				t.Fatalf("disabled experiment changed product fixture: %v", err)
			}
		}
		if err := actionsSharedWorkspaceFixture(pod, size, "1"); err != nil {
			t.Fatal(err)
		}
		if len(pod.Annotations) != 3 || pod.Annotations["dev.gvisor.spec.mount.runner.type"] != "bind" || pod.Annotations["dev.gvisor.spec.mount.runner.share"] != "pod" || pod.Annotations["dev.gvisor.spec.mount.runner.options"] != fmt.Sprintf("rw,rprivate,mode=0770,uid=1001,gid=1001,size=%dg", size+1) {
			t.Fatal("shared workspace did not use the exact disk-backed mount hint")
		}
		if !strings.HasSuffix(pod.Spec.Containers[0].Command[3], original.Spec.Containers[0].Command[3]) || !strings.Contains(pod.Spec.InitContainers[0].Command[2], ".hakopod-shared-prepare") {
			t.Fatal("shared workspace lost its init marker or original workload")
		}
		pod.Annotations = original.Annotations
		pod.Spec.Containers[0].Command = original.Spec.Containers[0].Command
		pod.Spec.InitContainers[0].Command = original.Spec.InitContainers[0].Command
		if !reflect.DeepEqual(pod, original) {
			t.Fatal("experiment changed resources, credentials, placement, or sandbox security")
		}
	}
}

func TestActionsSharedWorkspaceFixtureRejectsUnsafeConfiguration(t *testing.T) {
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	s.NodeName = "k3d-hakopod-dev-server-0"
	s.Actions.WorkspaceSizeGiB = 4
	original := actionsPod(target, "runner", "runtime-fixture", s)
	original.Spec.Containers[0].Command = []string{"python3", "-u", "-c", "print('fixture')"}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.RuntimeClassName = nil },
		func(p *corev1.Pod) { p.Spec.Affinity = nil },
		func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.Medium = corev1.StorageMediumMemory },
		func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.SizeLimit = nil },
		func(p *corev1.Pod) {
			p.Annotations = map[string]string{"dev.gvisor.spec.mount.runner.source": "/other"}
		},
		func(p *corev1.Pod) { p.Spec.InitContainers[0].Command[2] = "echo replaced" },
	} {
		pod := original.DeepCopy()
		mutate(pod)
		before := pod.DeepCopy()
		if err := actionsSharedWorkspaceFixture(pod, 4, "1"); err == nil || !reflect.DeepEqual(pod, before) {
			t.Fatal("unsafe experiment was accepted or partially applied")
		}
	}
	for _, flag := range []string{"true", "yes", " 1", "1\n"} {
		if err := actionsSharedWorkspaceFixture(original.DeepCopy(), 4, flag); err == nil {
			t.Fatal("malformed experiment flag was accepted")
		}
	}
	if err := actionsSharedWorkspaceFixture(original.DeepCopy(), 16, "1"); err == nil {
		t.Fatal("experiment accepted a new workspace size")
	}
}
