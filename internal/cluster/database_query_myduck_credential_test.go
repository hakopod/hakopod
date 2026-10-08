package cluster

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMyDuckQueryCredentialRejectsChangesAfterCapture(t *testing.T) {
	d := myduckFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "development-namespace", Labels: databaseLabels(d)}}
	captured := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-credentials"), Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": []byte(strings.Repeat("a", 64))}}
	captured.UID, captured.ResourceVersion = "development-secret", "1"
	if err := verifyMyDuckQueryCredential(captured.DeepCopy(), captured, d, ns); err != nil {
		t.Fatal("owned unchanged credential rejected")
	}
	for _, scenario := range []string{"replacement", "revision", "ownership", "deleting", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			current := captured.DeepCopy()
			switch scenario {
			case "replacement":
				current.UID = "replacement-secret"
			case "revision":
				current.ResourceVersion = "2"
			case "ownership":
				current.OwnerReferences = nil
			case "deleting":
				now := metav1.Now()
				current.DeletionTimestamp = &now
			case "missing":
				current = nil
			}
			if verifyMyDuckQueryCredential(current, captured, d, ns) == nil {
				t.Fatal("changed credential accepted after capture")
			}
		})
	}
}
