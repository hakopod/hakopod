package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func trustFixtureCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func databaseTrustFixture(t *testing.T) (*Client, Target) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write"}}
	target.Spec.Services["api"] = svc
	target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://fixture", Port: 5432, CA: trustFixtureCA(t)}}}
	c := &Client{kube: fake.NewClientset()}
	if err := c.prepareDatabaseTrust(context.Background(), target, "api"); err != nil {
		t.Fatal(err)
	}
	return c, target
}

func TestDatabaseTrustRenewalFencesRevisionAndPreservesWorkload(t *testing.T) {
	for _, fault := range []string{"", "revision", "ownership", "permissions", "claim", "snapshot"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			c, target := databaseTrustFixture(t)
			old := databaseTrustObject(target, "api")
			dep := deployment(target, "api", target.Spec.Services["api"], time.Minute)
			switch fault {
			case "revision":
				dep.Annotations["hakopod.io/revision"] = "999"
			case "ownership":
				dep.Labels[ownerKey] = "other"
			case "permissions":
				for i := range dep.Spec.Template.Spec.Volumes {
					if dep.Spec.Template.Spec.Volumes[i].Name == databaseTrustVolume {
						dep.Spec.Template.Spec.Volumes[i].ConfigMap.DefaultMode = ptr(int32(0666))
					}
				}
			case "claim":
				target.BeforeStep = func(context.Context) error { return fmt.Errorf("claim lost") }
			case "snapshot":
				old.Data[strings.Repeat("a", 32)+".crt"] = "tampered"
				if _, err := c.kube.CoreV1().ConfigMaps(old.Namespace).Update(ctx, old, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.kube.AppsV1().Deployments(dep.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			before := dep.DeepCopy()
			newCA := trustFixtureCA(t)
			c.SetDatabaseBindingResolver(func(_ context.Context, _, _ string, a spec.Application) (map[string]map[string]DatabaseConnection, error) {
				if len(a.Services) != 1 {
					t.Fatal("maintenance resolved unrelated services")
				}
				return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://fixture", Port: 5432, CA: newCA}}}, nil
			})
			err := c.RenewDatabaseTrust(ctx, target, nil, "api")
			if (err != nil) != (fault != "") {
				t.Fatalf("renewal fault=%q error=%v", fault, err)
			}
			after, _ := c.kube.AppsV1().Deployments(dep.Namespace).Get(ctx, "api", metav1.GetOptions{})
			if fault != "" {
				if !reflect.DeepEqual(before.Spec, after.Spec) {
					t.Fatal("failed renewal mutated workload")
				}
				return
			}
			if reflect.DeepEqual(before.Spec, after.Spec) {
				t.Fatal("renewal did not change volume")
			}
			after.Spec.Template.Spec.Volumes = before.Spec.Template.Spec.Volumes
			if !reflect.DeepEqual(before.Spec, after.Spec) {
				t.Fatal("renewal modified unrelated workload configuration")
			}
		})
	}
}

func TestDatabaseTrustCleanupRetainsEveryWorkloadKind(t *testing.T) {
	ctx := context.Background()
	c, target := databaseTrustFixture(t)
	old := databaseTrustObject(target, "api")
	pod := corev1.PodSpec{Volumes: []corev1.Volume{{Name: databaseTrustVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: old.Name}}}}}}
	ns := old.Namespace
	_, _ = c.kube.AppsV1().ReplicaSets(ns).Create(ctx, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: pod}}}, metav1.CreateOptions{})
	_, _ = c.kube.BatchV1().Jobs(ns).Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: pod}}}, metav1.CreateOptions{})
	_, _ = c.kube.BatchV1().CronJobs(ns).Create(ctx, &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: pod}}}}}, metav1.CreateOptions{})
	for step := 0; step < 4; step++ {
		if err := c.cleanupDatabaseTrust(ctx, target, "api", "next"); err != nil {
			t.Fatal(err)
		}
		_, err := c.kube.CoreV1().ConfigMaps(ns).Get(ctx, old.Name, metav1.GetOptions{})
		if step < 3 && err != nil {
			t.Fatal("live workload lost its public CA", err)
		}
		if step == 3 && !apierrors.IsNotFound(err) {
			t.Fatal("unused CA was not retired")
		}
		switch step {
		case 0:
			_ = c.kube.AppsV1().ReplicaSets(ns).Delete(ctx, "old", metav1.DeleteOptions{})
		case 1:
			_ = c.kube.BatchV1().Jobs(ns).Delete(ctx, "old", metav1.DeleteOptions{})
		case 2:
			_ = c.kube.BatchV1().CronJobs(ns).Delete(ctx, "old", metav1.DeleteOptions{})
		}
	}
}
