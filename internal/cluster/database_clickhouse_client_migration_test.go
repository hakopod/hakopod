package cluster

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func clickhouseMigrationObject(t *testing.T, legacy bool) (database.Resource, *unstructured.Unstructured, corev1.PodSpec) {
	t.Helper()
	d := clickhouseFixture()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("owned-controller")
	object.SetResourceVersion("7")
	_, _, pod, err := clickhouseIdentityTemplate(object)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		volumes := []corev1.Volume{}
		for _, volume := range pod.Volumes {
			if volume.Name != clickhouseClientTLSSecret {
				volumes = append(volumes, volume)
			}
		}
		pod.Volumes = volumes
		mounts := []corev1.VolumeMount{}
		for _, mount := range pod.Containers[0].VolumeMounts {
			if mount.Name != clickhouseClientTLSSecret {
				mounts = append(mounts, mount)
			}
		}
		pod.Containers[0].VolumeMounts = mounts
		clickhouseMigrationSetPod(t, object, pod)
		configuration := strings.ReplaceAll(clickhouseServerConfiguration(d), "/etc/hakopod-client-tls/", "/etc/hakopod-tls/")
		if err := unstructured.SetNestedField(object.Object, configuration, "spec", "configuration", "files", "zz-hakopod.xml"); err != nil {
			t.Fatal(err)
		}
	}
	return d, object, pod
}

func clickhouseMigrationSetPod(t *testing.T, object *unstructured.Unstructured, pod corev1.PodSpec) {
	t.Helper()
	converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
	if err != nil {
		t.Fatal(err)
	}
	templates, _, err := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
	if err != nil || len(templates) != 1 {
		t.Fatal("missing fixture template", err)
	}
	templates[0].(map[string]any)["spec"] = converted
	if err := unstructured.SetNestedSlice(object.Object, templates, "spec", "templates", "podTemplates"); err != nil {
		t.Fatal(err)
	}
}

func clickhouseMigrationClient(d database.Resource, object *unstructured.Unstructured) (*Client, *kubefake.Clientset, *dynamicfake.FakeDynamicClient) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "owned-namespace", Labels: databaseLabels(d)}}
	kube := kubefake.NewClientset(ns)
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	return &Client{kube: kube, dynamic: dynamic}, kube, dynamic
}

func clickhouseMigrationAssertNoWrites(t *testing.T, actions []clienttesting.Action) {
	t.Helper()
	for _, action := range actions {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("unexpected migration mutation: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestClickHouseClientMigrationFencedAndIdempotent(t *testing.T) {
	d, original, oldPod := clickhouseMigrationObject(t, true)
	c, kube, dynamic := clickhouseMigrationClient(d, original)
	ctx := context.Background()
	calls := 0
	if err := c.reconcileClickHouseClientIdentityConfiguration(ctx, d, func() error {
		calls++
		clickhouseMigrationAssertNoWrites(t, kube.Actions())
		clickhouseMigrationAssertNoWrites(t, dynamic.Actions())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one migration fence, got %d", calls)
	}
	updated, err := dynamic.Resource(clickhouseDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetUID() != original.GetUID() || updated.GetResourceVersion() != original.GetResourceVersion() {
		t.Fatal("migration lost update identity or resource version")
	}
	if err := clickhouseClientIdentityConfigured(updated, d); err != nil {
		t.Fatal("migrated controller configuration rejected", err)
	}
	_, _, projected, err := clickhouseIdentityTemplate(updated)
	if err != nil {
		t.Fatal(err)
	}
	if !databasePodMatches(corev1.Pod{Spec: projected}, d) {
		t.Fatal("new client projection is not observable")
	}
	if databasePodMatches(corev1.Pod{Spec: oldPod}, d) {
		t.Fatal("old pod passed observation before projection rollout")
	}
	// The accepted update adds precisely the projection and server paths.
	expected := original.DeepCopy()
	mode := int32(0440)
	oldPod.Volumes = append(oldPod.Volumes, corev1.Volume{Name: clickhouseClientTLSSecret, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: clickhouseClientTLSSecret, DefaultMode: &mode}}})
	oldPod.Containers[0].VolumeMounts = append(oldPod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: clickhouseClientTLSSecret, MountPath: "/etc/hakopod-client-tls", ReadOnly: true})
	clickhouseMigrationSetPod(t, expected, oldPod)
	if err := unstructured.SetNestedField(expected.Object, clickhouseServerConfiguration(d), "spec", "configuration", "files", "zz-hakopod.xml"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected.Object, updated.Object) {
		t.Fatal("migration changed fields outside client projection and server paths")
	}
	clickhouseMigrationAssertNoWrites(t, kube.Actions())
	dynamic.ClearActions()
	if err := c.reconcileClickHouseClientIdentityConfiguration(ctx, d, func() error { t.Fatal("idempotent migration reached write fence"); return nil }); err != nil {
		t.Fatal(err)
	}
	clickhouseMigrationAssertNoWrites(t, dynamic.Actions())
}

func TestClickHouseClientMigrationLostLeaseDoesNotWrite(t *testing.T) {
	d, object, _ := clickhouseMigrationObject(t, true)
	c, kube, dynamic := clickhouseMigrationClient(d, object)
	lost := errors.New("maintenance lease lost")
	if err := c.reconcileClickHouseClientIdentityConfiguration(context.Background(), d, func() error { return lost }); !errors.Is(err, lost) {
		t.Fatal("lease refusal not propagated", err)
	}
	clickhouseMigrationAssertNoWrites(t, kube.Actions())
	clickhouseMigrationAssertNoWrites(t, dynamic.Actions())
	current, err := dynamic.Resource(clickhouseDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(object.Object, current.Object) {
		t.Fatal("lease refusal changed controller", err)
	}
}

func TestClickHouseClientMigrationRejectsUnrecognizedState(t *testing.T) {
	for _, kind := range []string{"foreign-namespace", "foreign-controller", "revision", "configuration", "malformed-template", "duplicate-template", "wrong-image", "wrong-secret", "optional", "subpath", "duplicate-volume", "duplicate-mount", "init-container", "client-shadow", "client-ancestor"} {
		t.Run(kind, func(t *testing.T) {
			d, object, pod := clickhouseMigrationObject(t, true)
			switch kind {
			case "foreign-controller":
				object.SetLabels(map[string]string{databaseOwner: "someone-else", managedBy: "hakopod"})
			case "revision":
				object.SetAnnotations(map[string]string{"hakopod.io/database-revision": "999"})
			case "configuration":
				if err := unstructured.SetNestedField(object.Object, "<clickhouse/>", "spec", "configuration", "files", "zz-hakopod.xml"); err != nil {
					t.Fatal(err)
				}
			case "malformed-template":
				if err := unstructured.SetNestedField(object.Object, "invalid", "spec", "templates", "podTemplates"); err != nil {
					t.Fatal(err)
				}
			case "duplicate-template":
				templates, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
				if err := unstructured.SetNestedSlice(object.Object, append(templates, templates[0]), "spec", "templates", "podTemplates"); err != nil {
					t.Fatal(err)
				}
			case "wrong-image":
				pod.Containers[0].Image = "foreign:latest"
			case "wrong-secret":
				pod.Volumes[0].Secret.SecretName = "foreign-tls"
			case "optional":
				optional := true
				pod.Volumes[0].Secret.Optional = &optional
			case "subpath":
				pod.Containers[0].VolumeMounts[0].SubPath = "stale"
			case "duplicate-volume":
				pod.Volumes = append(pod.Volumes, pod.Volumes[0])
			case "duplicate-mount":
				pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, pod.Containers[0].VolumeMounts[0])
			case "client-shadow", "client-ancestor":
				path := "/etc/hakopod-client-tls/tls.crt"
				if kind == "client-ancestor" {
					path = "/etc"
				}
				pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "shadow", MountPath: path, ReadOnly: true})
			case "init-container":
				pod.InitContainers = []corev1.Container{{Name: "foreign-init", Image: clickhouseServerImage}}
			}
			switch kind {
			case "wrong-image", "wrong-secret", "optional", "subpath", "duplicate-volume", "duplicate-mount", "init-container", "client-shadow", "client-ancestor":
				clickhouseMigrationSetPod(t, object, pod)
			}
			c, kube, dynamic := clickhouseMigrationClient(d, object)
			if kind == "foreign-namespace" {
				ns, err := kube.CoreV1().Namespaces().Get(context.Background(), DatabaseNamespace(d.ID), metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				ns.Labels[databaseOwner] = "someone-else"
				if _, err := kube.CoreV1().Namespaces().Update(context.Background(), ns, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				kube.ClearActions()
			}
			if err := c.reconcileClickHouseClientIdentityConfiguration(context.Background(), d, func() error { return nil }); err == nil {
				t.Fatal("unrecognized migration state accepted")
			}
			clickhouseMigrationAssertNoWrites(t, kube.Actions())
			clickhouseMigrationAssertNoWrites(t, dynamic.Actions())
		})
	}
}

func TestClickHouseClientProjectionRejectsDrift(t *testing.T) {
	for _, kind := range []string{"missing", "wrong-secret", "optional", "items", "mode", "subpath", "subpath-expression", "writable", "wrong-path", "duplicate-volume", "duplicate-mount", "shadow-file", "shadow-directory", "shadow-ancestor", "sidecar-mount", "original-wrong-secret", "init-container"} {
		t.Run(kind, func(t *testing.T) {
			d, object, pod := clickhouseMigrationObject(t, false)
			switch kind {
			case "missing":
				pod.Volumes = pod.Volumes[1:]
			case "wrong-secret":
				pod.Volumes[0].Secret.SecretName = "database-tls"
			case "optional":
				optional := true
				pod.Volumes[0].Secret.Optional = &optional
			case "items":
				pod.Volumes[0].Secret.Items = []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}}
			case "mode":
				mode := int32(0644)
				pod.Volumes[0].Secret.DefaultMode = &mode
			case "subpath":
				pod.Containers[0].VolumeMounts[0].SubPath = "stale"
			case "subpath-expression":
				pod.Containers[0].VolumeMounts[0].SubPathExpr = "$(STALE)"
			case "writable":
				pod.Containers[0].VolumeMounts[0].ReadOnly = false
			case "wrong-path":
				pod.Containers[0].VolumeMounts[0].MountPath = "/elsewhere"
			case "duplicate-volume":
				pod.Volumes = append(pod.Volumes, pod.Volumes[0])
			case "duplicate-mount":
				pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, pod.Containers[0].VolumeMounts[0])
			case "original-wrong-secret":
				pod.Volumes[1].Secret.SecretName = "foreign-internal"
			case "init-container":
				pod.InitContainers = []corev1.Container{{Name: "foreign-init", Image: clickhouseServerImage}}
			case "shadow-file", "shadow-directory", "shadow-ancestor":
				path := "/etc/hakopod-client-tls/tls.crt"
				if kind == "shadow-directory" {
					path = "/etc/hakopod-client-tls"
				}
				if kind == "shadow-ancestor" {
					path = "/etc"
				}
				pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "shadow", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "foreign"}}})
				pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "shadow", MountPath: path, SubPath: "tls.crt", ReadOnly: true})
			case "sidecar-mount":
				pod.Containers = append(pod.Containers, corev1.Container{Name: "sidecar", Image: clickhouseServerImage, VolumeMounts: []corev1.VolumeMount{pod.Containers[0].VolumeMounts[0]}})
			}
			if clickhouseClientIdentityPodMatches(corev1.Pod{Spec: pod}) || databasePodMatches(corev1.Pod{Spec: pod}, d) {
				t.Fatal("unsafe client projection passed observation")
			}
			clickhouseMigrationSetPod(t, object, pod)
			if err := clickhouseClientIdentityConfigured(object, d); err == nil {
				t.Fatal("unsafe client projection passed controller convergence")
			}
		})
	}
}
