package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
)

func sharedVolumeTarget(t *testing.T) Target {
	t.Helper()
	target := testTarget(t)
	app, err := spec.Normalize(spec.Application{Name: "shared", Volumes: map[string]spec.NamedVolume{"media": {SizeGiB: 1}}, Services: map[string]spec.Service{
		"writer": {Image: "busybox:1", Mounts: []spec.Mount{{Volume: "media", MountPath: "/media"}}},
		"reader": {Image: "busybox:1", Mounts: []spec.Mount{{Volume: "media", MountPath: "/media", ReadOnly: true}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target.Spec = app
	return target
}

func TestSharedReadWriteOnceAffinityAllowsBootstrapAndExcludesOtherApplications(t *testing.T) {
	target := sharedVolumeTarget(t)
	for _, name := range []string{"writer", "reader"} {
		svc := target.Spec.Services[name]
		svc.NodeName, svc.Architecture = "worker", "amd64"
		target.Spec.Services[name] = svc
	}
	for _, name := range []string{"writer", "reader"} {
		d := deployment(target, name, target.Spec.Services[name], time.Minute)
		pod := d.Spec.Template.Spec
		if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || pod.Affinity.NodeAffinity == nil || pod.NodeSelector[corev1.LabelArchStable] != "amd64" {
			t.Fatal("shared placement lost recreate, node or architecture constraints")
		}
		terms := pod.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if len(terms) != 1 || terms[0].TopologyKey != corev1.LabelHostname || len(terms[0].Namespaces) != 1 || terms[0].Namespaces[0] != Namespace(target.ApplicationID) {
			t.Fatal("shared placement is not required on the same node and namespace")
		}
		selector, err := metav1.LabelSelectorAsSelector(terms[0].LabelSelector)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range []string{"reader", "writer"} {
			if !selector.Matches(labels.Set(labelsFor(target, member))) {
				t.Fatal("first pod or peer cannot bootstrap through self-affinity")
			}
		}
		foreign := target
		foreign.ApplicationID += "-other"
		if selector.Matches(labels.Set(labelsFor(foreign, name))) || selector.Matches(labels.Set(labelsFor(target, "unrelated"))) {
			t.Fatal("unrelated pod could control shared storage placement")
		}
	}
	volume := target.Spec.Volumes["media"]
	volume.AccessMode, volume.StorageClass = "ReadWriteMany", "nfs"
	target.Spec.Volumes["media"] = volume
	d := deployment(target, "reader", target.Spec.Services["reader"], time.Minute)
	if d.Spec.Template.Spec.Affinity.PodAffinity != nil {
		t.Fatal("ReadWriteMany was constrained to one node")
	}
}

func TestSharedReadWriteOncePlacementRetainsStoppedLocalStorage(t *testing.T) {
	target := sharedVolumeTarget(t)
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-volume-media", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "media"}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "media"}, Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"storage"}}}}}}}}}
	c := &Client{kube: fake.NewClientset(placementNode("storage"), placementNode("other"), claim, pv)}
	if err := c.validatePlacement(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	storage, _ := c.kube.CoreV1().Nodes().Get(context.Background(), "storage", metav1.GetOptions{})
	storage.Spec.Unschedulable = true
	_, _ = c.kube.CoreV1().Nodes().Update(context.Background(), storage, metav1.UpdateOptions{})
	if err := c.validatePlacement(context.Background(), target); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("stopped local data silently moved: %v", err)
	}
}

func TestSharedReadWriteOncePlacementRejectsSplitPodsAndForeignClaims(t *testing.T) {
	target := sharedVolumeTarget(t)
	ctx := context.Background()
	writer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "writer")}, Spec: corev1.PodSpec{NodeName: "first"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	reader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "reader")}, Spec: corev1.PodSpec{NodeName: "second"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	c := &Client{kube: fake.NewClientset(placementNode("first"), placementNode("second"), writer, reader)}
	if err := c.validatePlacement(ctx, target); err == nil || !strings.Contains(err.Error(), "different nodes") {
		t.Fatalf("split placement was not rejected: %v", err)
	}
	_ = c.kube.CoreV1().Pods(writer.Namespace).Delete(ctx, reader.Name, metav1.DeleteOptions{})
	if err := c.validatePlacement(ctx, target); err != nil {
		t.Fatal("remaining peer should anchor replacements", err)
	}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-volume-media", Namespace: writer.Namespace}}
	_, _ = c.kube.CoreV1().PersistentVolumeClaims(writer.Namespace).Create(ctx, claim, metav1.CreateOptions{})
	if err := c.validatePlacement(ctx, target); err == nil || !strings.Contains(err.Error(), "unowned") {
		t.Fatalf("foreign retained storage was accepted: %v", err)
	}
}

func TestSharedReadWriteOncePlacementHonorsPendingClaimsAndAllConsumerVolumes(t *testing.T) {
	target := sharedVolumeTarget(t)
	svc := target.Spec.Services["writer"]
	svc.Volume = &spec.Volume{MountPath: "/private", SizeGiB: 1}
	target.Spec.Services["writer"] = svc
	ctx := context.Background()
	media := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-volume-media", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, ""), Annotations: map[string]string{"volume.kubernetes.io/selected-node": "first"}}}
	private := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "writer-data", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "writer")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "private"}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "private"}, Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"second"}}}}}}}}}
	c := &Client{kube: fake.NewClientset(placementNode("first"), placementNode("second"), media, private, pv)}
	if err := c.validatePlacement(ctx, target); err == nil || !strings.Contains(err.Error(), "no common available node") {
		t.Fatalf("consumer's separate local data did not constrain the shared group: %v", err)
	}
	media.Annotations["volume.kubernetes.io/selected-node"] = "second"
	_, _ = c.kube.CoreV1().PersistentVolumeClaims(media.Namespace).Update(ctx, media, metav1.UpdateOptions{})
	if err := c.validatePlacement(ctx, target); err != nil {
		t.Fatal("compatible selected node and retained data were rejected", err)
	}
}

func TestSharedReadWriteOnceCapacityRequiresOneNodeAndDoesNotDoubleCount(t *testing.T) {
	target := sharedVolumeTarget(t)
	for name, svc := range target.Spec.Services {
		svc.Resources = &spec.Resources{CPURequest: "600m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "256Mi"}
		target.Spec.Services[name] = svc
	}
	c := &Client{kube: fake.NewClientset(placementNode("first"), placementNode("second"))}
	report, err := c.Preflight(context.Background(), target)
	if err != nil || report.Validate() == nil || !strings.Contains(report.Validate().Error(), "shared_volume_capacity") {
		t.Fatalf("aggregate capacity concealed group overload: %#v, %v", report, err)
	}
	for name, svc := range target.Spec.Services {
		svc.Resources = nil
		svc.NodeName = "first"
		target.Spec.Services[name] = svc
	}
	report, err = c.Preflight(context.Background(), target)
	if err != nil || report.Validate() != nil {
		t.Fatalf("pinned group was counted twice: %#v, %v", report, err)
	}
}

func TestSharedReadWriteOnceBootstrapUsesOnlyWholeGroupCandidates(t *testing.T) {
	target := sharedVolumeTarget(t)
	for name, svc := range target.Spec.Services {
		svc.Resources = &spec.Resources{CPURequest: "600m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "256Mi"}
		target.Spec.Services[name] = svc
	}
	large := placementNode("large")
	large.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("2")
	c := &Client{kube: fake.NewClientset(placementNode("small"), large)}
	report, err := c.Preflight(context.Background(), target)
	if err != nil || report.Validate() != nil {
		t.Fatalf("larger node did not admit group: %#v, %v", report, err)
	}
	target.sharedVolumeNodes = report.sharedVolumeNodes
	assertCandidate := func(name, wanted string) {
		t.Helper()
		d := deployment(target, name, target.Spec.Services[name], time.Minute)
		nodeAffinity := d.Spec.Template.Spec.Affinity.NodeAffinity
		if nodeAffinity == nil || nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			t.Fatal("group bootstrap has no required candidate nodes")
		}
		terms := nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		for _, node := range []string{"small", "large"} {
			fits := false
			for _, term := range terms {
				fits = fits || matchNodeRequirements(term.MatchFields, map[string]string{"metadata.name": node})
			}
			if fits != (node == wanted) {
				t.Fatalf("service %s admits node %s=%v, want only %s", name, node, fits, wanted)
			}
		}
	}
	assertCandidate("reader", "large")
	assertCandidate("writer", "large")

	// A reader can start before the writer. Its new media PVC must still bind
	// beside the writer's retained private volume, even when both nodes fit.
	for name, svc := range target.Spec.Services {
		svc.Resources = nil
		if name == "writer" {
			svc.Volume = &spec.Volume{MountPath: "/private", SizeGiB: 1}
		}
		target.Spec.Services[name] = svc
	}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "writer-data", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "writer")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "retained"}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "retained"}, Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"large"}}}}}}}}}
	_, _ = c.kube.CoreV1().PersistentVolumeClaims(claim.Namespace).Create(context.Background(), claim, metav1.CreateOptions{})
	_, _ = c.kube.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{})
	report, err = c.Preflight(context.Background(), target)
	if err != nil || report.Validate() != nil {
		t.Fatalf("retained private volume did not admit group: %#v, %v", report, err)
	}
	target.sharedVolumeNodes = report.sharedVolumeNodes
	assertCandidate("reader", "large")
}
