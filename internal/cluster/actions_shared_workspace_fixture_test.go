package cluster

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// This experiment changes only development fixtures, never actionsPod or the
// installed runtime. The shim discovers the source from the owned Pod UID.
func actionsSharedWorkspaceFixture(pod *corev1.Pod, workspaceGiB int64, flag, driver string) error {
	return actionsSharedWorkspaceProfile(pod, workspaceGiB, flag, driver, "runtime")
}

// The representative profile keeps the customer's original resource budget.
// It is separate from the runtime suite, whose disk-eviction fixture stays 2 GiB.
func actionsRepresentativeSharedWorkspaceFixture(pod *corev1.Pod, selection string) error {
	switch selection {
	case "", "baseline-vfs":
		return nil
	case "shared-overlay2-16":
		return actionsSharedWorkspaceProfile(pod, 16, "1", "overlay2", "representative-16")
	default:
		return fmt.Errorf("representative storage must be baseline-vfs or shared-overlay2-16")
	}
}

func actionsSharedWorkspaceProfile(pod *corev1.Pod, workspaceGiB int64, flag, driver, profile string) error {
	if profile != "runtime" && profile != "representative-16" {
		return fmt.Errorf("unknown development shared workspace profile")
	}
	if profile == "representative-16" && (flag != "1" || driver != "overlay2") {
		return fmt.Errorf("representative shared workspace requires explicit overlay2 selection")
	}
	if flag != "" && flag != "0" && flag != "1" {
		return fmt.Errorf("shared workspace flag must be 0 or 1")
	}
	if driver == "" {
		driver = "vfs"
	}
	if driver != "vfs" && driver != "overlay2" {
		return fmt.Errorf("development Docker storage driver must be vfs or overlay2")
	}
	if driver == "overlay2" && flag != "1" {
		return fmt.Errorf("development Docker overlay2 requires the shared workspace")
	}
	if flag != "1" {
		return nil
	}
	if profile == "runtime" {
		if workspaceGiB != 2 && workspaceGiB != 4 && workspaceGiB != 8 {
			return fmt.Errorf("shared workspace is limited to the existing development fixture sizes")
		}
	} else {
		if workspaceGiB != 16 || driver != "overlay2" || pod.Name != "actions-representative" || pod.Labels["hakopod.io/development-fixture"] != "actions-representative-build" || !reflect.DeepEqual(pod.Spec.NodeSelector, map[string]string{"kubernetes.io/arch": "arm64"}) {
			return fmt.Errorf("representative shared workspace requires its exact development identity and 16 GiB overlay2 profile")
		}
		budget := spec.Service{Resources: &spec.Resources{CPURequest: "2", CPULimit: "2", MemoryRequest: "6Gi", MemoryLimit: "6Gi"}, Actions: &spec.Actions{WorkspaceSizeGiB: 16}}
		if len(pod.Spec.InitContainers) != 2 || len(pod.Spec.Containers) != 1 || len(pod.Spec.Volumes) != 1 ||
			!reflect.DeepEqual(pod.Spec.InitContainers[0].Resources, actionsResources(budget, 4)) ||
			!reflect.DeepEqual(pod.Spec.InitContainers[1].Resources, actionsResources(budget, 3)) ||
			!reflect.DeepEqual(pod.Spec.Containers[0].Resources, actionsResources(budget, 1)) {
			return fmt.Errorf("representative shared workspace changed its 2 CPU, 6 GiB memory, or 16 GiB workspace budget")
		}
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
	if pod.Spec.InitContainers[1].Name != "docker" || pod.Spec.InitContainers[1].Image != ActionsDaemonImage || !reflect.DeepEqual(pod.Spec.InitContainers[1].Command, []string{"sh", "-c", actionsDaemon}) || strings.Count(actionsDaemon, "--storage-driver=vfs") != 1 {
		return fmt.Errorf("shared workspace requires the unchanged pinned Docker daemon command")
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
	// Only a freshly generated disposable fixture selects another driver. Never
	// migrate existing Docker stores or alter the daemon's sandbox permissions.
	if driver == "overlay2" {
		pod.Spec.InitContainers[1].Command[2] = strings.Replace(actionsDaemon, "--storage-driver=vfs", "--storage-driver=overlay2", 1)
	}
	pod.Spec.InitContainers[0].Command[2] += " && printf 'shared-workspace-v1\\n' > /runner/.hakopod-shared-prepare"
	pod.Spec.Containers[0].Command[3] = `import pathlib, time
_shared_root = pathlib.Path('/home/runner')
if (_shared_root / '.hakopod-shared-prepare').read_text() != 'shared-workspace-v1\n':
    raise RuntimeError('Prepare workspace was not shared with the runner')
(_shared_root / '.hakopod-shared-ready').touch(exist_ok=False)
for _shared_attempt in range(380):
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
			if err := actionsSharedWorkspaceFixture(pod, size, flag, ""); err != nil || !reflect.DeepEqual(pod, original) {
				t.Fatalf("disabled experiment changed product fixture: %v", err)
			}
		}
		if err := actionsSharedWorkspaceFixture(pod, size, "1", "vfs"); err != nil {
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
		if err := actionsSharedWorkspaceFixture(pod, 4, "1", "vfs"); err == nil || !reflect.DeepEqual(pod, before) {
			t.Fatal("unsafe experiment was accepted or partially applied")
		}
	}
	for _, flag := range []string{"true", "yes", " 1", "1\n"} {
		if err := actionsSharedWorkspaceFixture(original.DeepCopy(), 4, flag, "vfs"); err == nil {
			t.Fatal("malformed experiment flag was accepted")
		}
	}
	if err := actionsSharedWorkspaceFixture(original.DeepCopy(), 16, "1", "vfs"); err == nil {
		t.Fatal("experiment accepted a new workspace size")
	}
}

func TestActionsSharedWorkspaceOverlayDriverOnlyChangesFreshFixtureCommand(t *testing.T) {
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	s.NodeName = "k3d-hakopod-dev-server-0"
	s.Actions.WorkspaceSizeGiB = 4
	original := actionsPod(target, "runner", "runtime-fixture", s)
	original.Spec.Containers[0].Command = []string{"python3", "-u", "-c", "print('fixture')"}
	vfs, overlay := original.DeepCopy(), original.DeepCopy()
	if err := actionsSharedWorkspaceFixture(vfs, 4, "1", "vfs"); err != nil {
		t.Fatal(err)
	}
	if err := actionsSharedWorkspaceFixture(overlay, 4, "1", "overlay2"); err != nil {
		t.Fatal(err)
	}
	if overlay.Spec.InitContainers[1].Command[2] != strings.Replace(actionsDaemon, "--storage-driver=vfs", "--storage-driver=overlay2", 1) {
		t.Fatal("experiment did not select the exact overlay2 daemon command")
	}
	overlay.Spec.InitContainers[1].Command = vfs.Spec.InitContainers[1].Command
	if !reflect.DeepEqual(overlay, vfs) {
		t.Fatal("storage driver experiment changed resources, images, placement, or sandbox security")
	}
	for _, selection := range [][2]string{{"0", "overlay2"}, {"", "overlay2"}, {"1", "overlay"}, {"1", "overlay2 --privileged"}} {
		pod := original.DeepCopy()
		if err := actionsSharedWorkspaceFixture(pod, 4, selection[0], selection[1]); err == nil || !reflect.DeepEqual(pod, original) {
			t.Fatal("unsafe storage selection was accepted or partially applied")
		}
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.InitContainers[1].Image = "docker:latest" },
		func(p *corev1.Pod) { p.Spec.InitContainers[1].Command[2] += " --storage-driver=overlay2" },
	} {
		pod := original.DeepCopy()
		mutate(pod)
		before := pod.DeepCopy()
		if err := actionsSharedWorkspaceFixture(pod, 4, "1", "overlay2"); err == nil || !reflect.DeepEqual(pod, before) {
			t.Fatal("modified daemon image or command was accepted")
		}
	}
}

func TestActionsRepresentativeSharedWorkspaceRejectsOtherProfilesAndBudgets(t *testing.T) {
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	s.NodeName, s.Architecture = "k3d-hakopod-dev-server-0", "arm64"
	s.Resources = &spec.Resources{CPURequest: "2", CPULimit: "2", MemoryRequest: "6Gi", MemoryLimit: "6Gi"}
	s.Actions.WorkspaceSizeGiB = 16
	original := actionsPod(target, "runner", "representative", s)
	original.Labels["hakopod.io/development-fixture"] = "actions-representative-build"
	original.Spec.Volumes = original.Spec.Volumes[:1]
	original.Spec.Containers[0].Command = []string{"python3", "-u", "-c", "print('private fixture')"}
	for _, selection := range []string{"vfs", "overlay2", "1", "representative-16", "shared-overlay2-16\n"} {
		pod := original.DeepCopy()
		if err := actionsRepresentativeSharedWorkspaceFixture(pod, selection); err == nil || !reflect.DeepEqual(pod, original) {
			t.Fatalf("invalid representative storage selection was accepted or partially applied: %q", selection)
		}
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Name = "actions-runtime-fixture" },
		func(p *corev1.Pod) { delete(p.Labels, "hakopod.io/development-fixture") },
		func(p *corev1.Pod) { p.Spec.NodeSelector["kubernetes.io/arch"] = "amd64" },
		func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.SizeLimit = ptr(resource.MustParse("8Gi")) },
		func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.Medium = corev1.StorageMediumMemory },
		func(p *corev1.Pod) {
			p.Spec.InitContainers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("3")
		},
		func(p *corev1.Pod) {
			p.Spec.InitContainers[1].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("4Gi")
		},
		func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse("18Gi")
		},
		func(p *corev1.Pod) { p.Spec.Affinity = nil },
		func(p *corev1.Pod) {
			p.Annotations = map[string]string{"dev.gvisor.spec.mount.runner.source": "/other"}
		},
	} {
		pod := original.DeepCopy()
		mutate(pod)
		before := pod.DeepCopy()
		if err := actionsRepresentativeSharedWorkspaceFixture(pod, "shared-overlay2-16"); err == nil || !reflect.DeepEqual(pod, before) {
			t.Fatal("unsafe representative profile was accepted or partially applied")
		}
	}
	for _, size := range []int64{2, 4, 8, 17} {
		if err := actionsSharedWorkspaceProfile(original.DeepCopy(), size, "1", "overlay2", "representative-16"); err == nil {
			t.Fatal("representative shared workspace accepted another size")
		}
	}
	if err := actionsSharedWorkspaceFixture(original.DeepCopy(), 16, "1", "overlay2"); err == nil {
		t.Fatal("runtime fixture inherited the representative 16 GiB allowance")
	}
}
