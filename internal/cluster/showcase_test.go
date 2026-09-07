package cluster

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestShowcaseRemovalRequiresTrackedOwnershipAndNoPersistentData(t *testing.T) {
	target := Target{ApplicationID: "sample-removal-test", Project: "demo", Environment: "development"}
	ctx := context.Background()
	for _, scenario := range []string{"foreign", "persistent", "owned", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			c := &Client{kube: kube}
			if scenario != "absent" {
				labels := labelsFor(target, "")
				if scenario == "foreign" {
					labels[ownerKey] = "another-application"
				}
				if _, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), UID: "sample-uid", Labels: labels}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "persistent" {
				if _, err := kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "operator-data"}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			err := c.RemoveShowcase(ctx, target.ApplicationID)
			if scenario == "persistent" && !errors.Is(err, ErrSampleHasData) {
				t.Fatal("sample data was not protected", err)
			}
			if scenario == "foreign" && err == nil {
				t.Fatal("foreign namespace was removed")
			}
			if (scenario == "owned" || scenario == "absent") && err != nil {
				t.Fatal(err)
			}
			for _, a := range kube.Actions() {
				if a.GetVerb() == "delete" && (scenario == "foreign" || scenario == "persistent") {
					t.Fatal("unsafe namespace delete attempted")
				}
			}
		})
	}
}
