package cluster

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestBindingRefreshPublishesEnvironmentAndTrustTogether(t *testing.T) {
	for _, profile := range []string{spec.DatabaseClientInfisicalPostgresV1, spec.DatabaseClientNodeExtraCAV1} {
		for _, scheduled := range []bool{false, true} {
			kind := "deployment"
			if scheduled {
				kind = "schedule"
			}
			t.Run(profile+"/"+kind, func(t *testing.T) {
				ctx := context.Background()
				target := testTarget(t)
				variable, protocol, address, port := "DB_CONNECTION_URI", "postgres", "postgres://fixture:original@database:5432/app", int32(5432)
				if profile == spec.DatabaseClientNodeExtraCAV1 {
					variable, protocol, address, port = "REDIS_URL", "redis", "rediss://fixture:original@database:6379/0", 6379
				}
				id := strings.Repeat("a", 32)
				svc := target.Spec.Services["api"]
				svc.Bindings = map[string]spec.Binding{variable: {ManagedDatabase: id, Protocol: protocol, Endpoint: "read_write"}}
				svc.DatabaseClientProfiles = map[string]string{variable: profile}
				if scheduled {
					svc.Port = 0
					svc.Job = &spec.Job{TimeoutSeconds: 60, Schedule: &spec.JobSchedule{Cron: "0 0 1 1 *"}}
				}
				target.Spec.Services["api"] = svc
				oldCA, newCA := trustFixtureCA(t), trustFixtureCA(t)
				target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {variable: {URL: address, Port: port, CA: oldCA}}}
				kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
				c := &Client{kube: kube}
				if err := c.prepareWorkloadSecrets(ctx, target, "api", svc); err != nil {
					t.Fatal(err)
				}
				dep := deployment(target, "api", svc, time.Minute)
				if _, err := c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template); err != nil {
					t.Fatal(err)
				}
				original := dep.Spec.Template.DeepCopy()
				dep.Annotations[bindingVerifiedSnapshot] = original.Annotations[environmentSnapshotLabel]
				ns := Namespace(target.ApplicationID)
				if scheduled {
					cron := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: scheduledJobName("api"), Namespace: ns, Labels: labelsFor(target, "api"), Annotations: map[string]string{jobRevision: "1"}}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: *original}}}}
					if _, err := kube.BatchV1().CronJobs(ns).Create(ctx, cron, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := kube.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				// Retained jobs keep both of their original references.
				job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "previous-run", Namespace: ns, Labels: labelsFor(target, "api")}, Spec: batchv1.JobSpec{Template: *original}}
				if _, err := kube.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				// Node has a CA-only rotation. Infisical changes its URL and its
				// generated CA environment value in the same update.
				newAddress := address
				if profile == spec.DatabaseClientInfisicalPostgresV1 {
					newAddress = strings.Replace(address, "original", "rotated", 1)
				}
				c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
					return map[string]map[string]DatabaseConnection{"api": {variable: {URL: newAddress, Port: port, CA: newCA}}}, nil
				})
				updates := 0
				kube.PrependReactor("update", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
					var candidate *corev1.PodTemplateSpec
					switch object := action.(ktesting.UpdateAction).GetObject().(type) {
					case *appsv1.Deployment:
						candidate = &object.Spec.Template
						if object.Annotations[bindingVerifiedSnapshot] != "" {
							t.Fatal("a new trust rollout kept its previous verification")
						}
					case *batchv1.CronJob:
						candidate = &object.Spec.JobTemplate.Spec.Template
					default:
						return false, nil, nil
					}
					updates++
					object, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), ns, candidate.Annotations[environmentSnapshotLabel])
					if err != nil {
						t.Fatal("template environment snapshot is unavailable", err)
					}
					secret := object.(*corev1.Secret)
					if string(secret.Data[variable]) != newAddress {
						t.Fatal("template was published before its current environment", err)
					}
					if profile == spec.DatabaseClientInfisicalPostgresV1 && string(secret.Data["DB_ROOT_CERT"]) != base64.StdEncoding.EncodeToString([]byte(newCA)) {
						t.Fatal("template contains stale generated CA environment")
					}
					trustFound := false
					for _, volume := range candidate.Spec.Volumes {
						if volume.Name != databaseTrustVolume {
							continue
						}
						object, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), ns, volume.ConfigMap.Name)
						if err != nil {
							t.Fatal("template trust snapshot is unavailable", err)
						}
						trust := object.(*corev1.ConfigMap)
						if trust.Data[id+".crt"] != newCA {
							t.Fatal("template was published with stale CA volume", err)
						}
						trustFound = true
					}
					if !trustFound {
						t.Fatal("template lost its trust volume")
					}
					return false, nil, nil
				})
				if err := c.RefreshDatabaseBindings(ctx, target, nil, "api"); err != nil {
					t.Fatal(err)
				}
				if updates != 1 {
					t.Fatalf("expected one atomic template update, got %d", updates)
				}
				if err := c.RefreshDatabaseBindings(ctx, target, nil, "api"); err != nil || updates != 1 {
					t.Fatal("unchanged settings published another template", err)
				}
				kept, err := kube.BatchV1().Jobs(ns).Get(ctx, job.Name, metav1.GetOptions{})
				if err != nil || !reflect.DeepEqual(job.Spec, kept.Spec) {
					t.Fatal("refresh changed a retained job", err)
				}
				if _, err := kube.CoreV1().Secrets(ns).Get(ctx, original.Annotations[environmentSnapshotLabel], metav1.GetOptions{}); err != nil {
					t.Fatal("retained job lost its environment", err)
				}
			})
		}
	}
}

func TestDatabaseTrustRenewalPreservesManualTrustSettings(t *testing.T) {
	ctx := context.Background()
	c, target := databaseTrustFixture(t)
	svc := target.Spec.Services["api"]
	svc.Env = map[string]string{"NODE_EXTRA_CA_CERTS": "/manual/node.pem", "SSL_CERT_FILE": "/manual/system.pem", "SSL_CERT_DIR": "/manual/certs"}
	target.Spec.Services["api"] = svc
	dep := deployment(target, "api", svc, time.Minute)
	before := dep.Spec.Template.Spec.Containers[0].DeepCopy()
	if _, err := c.kube.AppsV1().Deployments(dep.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	newCA := trustFixtureCA(t)
	c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://fixture", Port: 5432, CA: newCA}}}, nil
	})
	if err := c.RenewDatabaseTrust(ctx, target, nil, "api"); err != nil {
		t.Fatal("manual trust settings blocked unrelated managed CA renewal", err)
	}
	after, err := c.kube.AppsV1().Deployments(dep.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(before, &after.Spec.Template.Spec.Containers[0]) {
		t.Fatal("renewal changed manually configured trust", err)
	}
}

func TestBindingRefreshPreservesLegacyEnvironmentConsumers(t *testing.T) {
	ctx := context.Background()
	c, target, canonical, _ := environmentSnapshotFixture(t)
	oldCA, newCA := trustFixtureCA(t), trustFixtureCA(t)
	address := string(canonical.Data["DATABASE_URL"])
	target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: address, Port: 5432, CA: oldCA}}}
	if err := c.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "token", "fixture-token"); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareWorkloadSecrets(ctx, target, "api", target.Spec.Services["api"]); err != nil {
		t.Fatal(err)
	}
	dep := deployment(target, "api", target.Spec.Services["api"], time.Minute)
	ns := canonical.Namespace
	if _, err := c.kube.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	retained := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: ns, Labels: labelsFor(target, "api")}, Spec: appsv1.ReplicaSetSpec{Template: dep.Spec.Template}}
	if _, err := c.kube.AppsV1().ReplicaSets(ns).Create(ctx, retained, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "legacy-job", Namespace: ns, Labels: labelsFor(target, "api")}, Spec: batchv1.JobSpec{Template: dep.Spec.Template}}
	if _, err := c.kube.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	rotated := target
	rotated.databaseConnections = map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: strings.Replace(address, "fixture@", "rotated@", 1), Port: 5432, CA: newCA}}}
	// The normal deployment preparation path must preserve old consumers too.
	if err := c.prepareWorkloadSecrets(ctx, rotated, "api", rotated.Spec.Services["api"]); err != nil {
		t.Fatal(err)
	}
	c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return rotated.databaseConnections, nil
	})
	updates := 0
	c.kube.(*fake.Clientset).PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})
	if err := c.RefreshDatabaseBindings(ctx, target, nil, "api"); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatal("legacy migration did not publish exactly one template")
	}
	after, err := c.kube.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := c.kube.CoreV1().Secrets(ns).Get(ctx, after.Spec.Template.Annotations[environmentSnapshotLabel], metav1.GetOptions{})
	if err != nil || string(secret.Data["DATABASE_URL"]) != rotated.databaseConnections["api"]["DATABASE_URL"].URL {
		t.Fatal("legacy deployment did not adopt the resolved immutable snapshot", err)
	}
	currentCanonical, err := c.kube.CoreV1().Secrets(ns).Get(ctx, canonical.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(canonical.Data, currentCanonical.Data) {
		t.Fatal("old consumers were exposed to new credentials", err)
	}
	keptReplica, err := c.kube.AppsV1().ReplicaSets(ns).Get(ctx, retained.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(retained.Spec, keptReplica.Spec) {
		t.Fatal("legacy ReplicaSet was rewritten", err)
	}
	keptJob, err := c.kube.BatchV1().Jobs(ns).Get(ctx, job.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(job.Spec, keptJob.Spec) {
		t.Fatal("legacy job was rewritten", err)
	}
}

func TestDatabaseTrustRemovalDoesNotPublishStaleTrust(t *testing.T) {
	for _, refresh := range []string{"bindings", "trust"} {
		t.Run(refresh, func(t *testing.T) {
			ctx := context.Background()
			c, target := databaseTrustFixture(t)
			if _, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := c.prepareWorkloadSecrets(ctx, target, "api", target.Spec.Services["api"]); err != nil {
				t.Fatal(err)
			}
			dep := deployment(target, "api", target.Spec.Services["api"], time.Minute)
			if _, err := c.kube.AppsV1().Deployments(dep.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
				return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://fixture", Port: 5432}}}, nil
			})
			var err error
			if refresh == "bindings" {
				err = c.RefreshDatabaseBindings(ctx, target, nil, "api")
			} else {
				err = c.RenewDatabaseTrust(ctx, target, nil, "api")
			}
			if err == nil || !strings.Contains(err.Error(), "trust was removed") {
				t.Fatal("missing managed CA was silently accepted", err)
			}
			after, err := c.kube.AppsV1().Deployments(dep.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
			if err != nil || !reflect.DeepEqual(dep.Spec, after.Spec) {
				t.Fatal("failed trust removal published a partial template", err)
			}
		})
	}
}

func TestBindingProbeProfileRejectsStaleGeneratedCA(t *testing.T) {
	svc := spec.Service{Bindings: map[string]spec.Binding{"DB_CONNECTION_URI": {ManagedDatabase: strings.Repeat("a", 32)}}, DatabaseClientProfiles: map[string]string{"DB_CONNECTION_URI": spec.DatabaseClientInfisicalPostgresV1}}
	ca := trustFixtureCA(t)
	for _, fault := range []string{"", "old-ca", "mutable-secret", "missing-ref", "foreign-ref", "duplicate-ref"} {
		t.Run(fault, func(t *testing.T) {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "snapshot"}, Immutable: ptr(true), Data: map[string][]byte{"DB_ROOT_CERT": []byte(base64.StdEncoding.EncodeToString([]byte(ca)))}}
			env := corev1.EnvVar{Name: "DB_ROOT_CERT", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}, Key: "DB_ROOT_CERT"}}}
			pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{env}}}}}
			switch fault {
			case "old-ca":
				secret.Data["DB_ROOT_CERT"] = []byte(base64.StdEncoding.EncodeToString([]byte(trustFixtureCA(t))))
			case "mutable-secret":
				secret.Immutable = nil
			case "missing-ref":
				pod.Spec.Containers[0].Env = nil
			case "foreign-ref":
				pod.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name = "other-secret"
			case "duplicate-ref":
				pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, env)
			}
			if got := bindingProbeProfileMatches(svc, "DB_CONNECTION_URI", pod, secret, ca); got != (fault == "") {
				t.Fatalf("profile comparison accepted invalid app trust: fault=%q matches=%v", fault, got)
			}
		})
	}
}
