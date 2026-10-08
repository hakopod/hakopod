package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func vitessQueryTargetFixture(t *testing.T) (*Client, database.Resource) {
	t.Helper()
	c, d, storage := vitessRevocationFixture(t, false)
	c.options.VitessBackup = func(context.Context, database.Resource) (VitessBackupStorage, error) { return storage, nil }
	ctx := context.Background()
	ns, e := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	cert := []byte("explicit development query certificate fixture")
	secret := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-tls"), Data: map[string][]byte{"tls.crt": cert}}
	if _, e = c.kube.CoreV1().Secrets(ns.Name).Create(ctx, secret, metav1.CreateOptions{}); e != nil {
		t.Fatal(e)
	}
	root, e := c.databaseObject(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	root.SetUID("database-root")
	if e = c.applyVitessIdentity(ctx, d, root); e != nil {
		t.Fatal(e)
	}
	if _, e = c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Update(ctx, root, metav1.UpdateOptions{}); e != nil {
		t.Fatal(e)
	}
	identity := fmt.Sprintf("%x", sha256.Sum256(cert))
	cpu, memory := resource.MustParse(database.VitessGatewayCPU), resource.MustParse(database.VitessGatewayMemory)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "query-gateway", Namespace: ns.Name, UID: "query-gateway-uid", Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod", vitessComponentLabel: "gateway"}, Annotations: map[string]string{vitessIdentityAnnotation: identity}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: root.GetUID(), Controller: ptr(true)}}}, Spec: corev1.PodSpec{ServiceAccountName: "database-vitess-workload", Containers: []corev1.Container{{Name: "vtgate", Image: vitessServerImage, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}, Limits: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if _, e = c.kube.CoreV1().Pods(ns.Name).Create(ctx, pod, metav1.CreateOptions{}); e != nil {
		t.Fatal(e)
	}
	return c, d
}

func TestVitessQueryTargetRejectsChangedAuthority(t *testing.T) {
	for _, scenario := range []string{"namespace_uid", "controller_uid", "revision", "spec", "pod_uid", "pod_spec", "not_ready", "identity", "deleting"} {
		t.Run(scenario, func(t *testing.T) {
			c, d := vitessQueryTargetFixture(t)
			ctx := context.Background()
			target, e := c.selectVitessQueryTarget(ctx, d)
			if e != nil {
				t.Fatal("initial target", e)
			}
			ns := DatabaseNamespace(d.ID)
			switch scenario {
			case "namespace_uid":
				n, _ := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
				n.UID = "replacement"
				c.kube.CoreV1().Namespaces().Update(ctx, n, metav1.UpdateOptions{})
			case "controller_uid", "revision", "spec":
				o, _ := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
				if scenario == "controller_uid" {
					o.SetUID("replacement")
				}
				if scenario == "revision" {
					o.SetAnnotations(map[string]string{"hakopod.io/database-revision": "999"})
				}
				if scenario == "spec" {
					o.Object["spec"].(map[string]any)["unreviewed"] = true
				}
				c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Update(ctx, o, metav1.UpdateOptions{})
			default:
				p, _ := c.kube.CoreV1().Pods(ns).Get(ctx, target.pod.Name, metav1.GetOptions{})
				switch scenario {
				case "pod_uid":
					p.UID = "replacement"
				case "pod_spec":
					p.Spec.Containers[0].Image = "unreviewed"
				case "not_ready":
					p.Status.Conditions = nil
				case "identity":
					p.Annotations[vitessIdentityAnnotation] = "changed"
				case "deleting":
					now := metav1.Now()
					p.DeletionTimestamp = &now
				}
				c.kube.CoreV1().Pods(ns).Update(ctx, p, metav1.UpdateOptions{})
			}
			if e = c.verifyVitessQueryTarget(ctx, d, target); e == nil {
				t.Fatal("changed authority accepted")
			}
		})
	}
}

func TestVitessQueryTargetRejectsInitialRevisionAndSpec(t *testing.T) {
	for _, field := range []string{"revision", "spec"} {
		t.Run(field, func(t *testing.T) {
			c, d := vitessQueryTargetFixture(t)
			ctx := context.Background()
			o, _ := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
			if field == "revision" {
				o.SetAnnotations(map[string]string{"hakopod.io/database-revision": "999"})
			} else {
				o.Object["spec"].(map[string]any)["unreviewed"] = true
			}
			c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Update(ctx, o, metav1.UpdateOptions{})
			if _, e := c.selectVitessQueryTarget(ctx, d); e == nil {
				t.Fatal("unreviewed controller accepted")
			}
		})
	}
}

func TestVitessQueryTargetRejectsUnboundedInventory(t *testing.T) {
	for _, scenario := range []string{"continue", "too_many", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			c, d := vitessQueryTargetFixture(t)
			c.kube.(*kubefake.Clientset).PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				list := &corev1.PodList{}
				if scenario == "continue" {
					list.Continue = "more"
				}
				if scenario == "too_many" {
					list.Items = make([]corev1.Pod, 17)
				}
				return true, list, nil
			})
			if _, e := c.selectVitessQueryTarget(context.Background(), d); e == nil {
				t.Fatal("invalid bounded inventory accepted")
			}
		})
	}
}

func TestSQLQueryCredentialRejectsRotationDuringConnectionSetup(t *testing.T) {
	c, d := vitessQueryTargetFixture(t)
	ctx := context.Background()
	ns := DatabaseNamespace(d.ID)
	baseline := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database-credentials", Namespace: ns, UID: "credential-before-connect", ResourceVersion: "1", Labels: databaseLabels(d)}, Data: map[string][]byte{"password": []byte("explicit development credential fixture")}}
	if _, e := c.kube.CoreV1().Secrets(ns).Create(ctx, baseline, metav1.CreateOptions{}); e != nil {
		t.Fatal(e)
	}
	// The connector authenticates against this frozen identity. Rotation before
	// the first user-SQL guard must not establish a replacement baseline.
	frozen, e := c.kube.CoreV1().Secrets(ns).Get(ctx, baseline.Name, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	rotated := baseline.DeepCopy()
	rotated.UID = "credential-after-connect"
	rotated.ResourceVersion = "2"
	rotated.Data["password"] = []byte("rotated development credential fixture")
	if _, e = c.kube.CoreV1().Secrets(ns).Update(ctx, rotated, metav1.UpdateOptions{}); e != nil {
		t.Fatal(e)
	}
	if e = c.verifySQLQueryCredential(ctx, d, frozen); e == nil {
		t.Fatal("connection credential rotation accepted")
	}
}
