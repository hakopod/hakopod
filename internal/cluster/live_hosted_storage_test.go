package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveHostedBlockStorageSurvivesRestartAndEnforcesSize(t *testing.T) {
	c, path := hostedStorageTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{Name: "storage-acceptance", Services: map[string]spec.Service{"db": {Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 8080, Command: []string{"python", "-m", "http.server", "8080"}, RunAsUser: 10000, RunAsGroup: 10000, FSGroup: 10000, Volume: &spec.Volume{SizeGiB: 1, MountPath: "/data"}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "hosted-storage-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "storage-test", Environment: "test", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	cleanupHostedStorageTest(t, c, target)

	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "db-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "hakopod-hosted-block" {
		t.Fatal("trusted storage class not enforced")
	}
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != "local.csi.openebs.io" || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatal("volume is not retained block-backed storage", err)
	}
	run := func(script string) string {
		t.Helper()
		out, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "exec", "-n", ns, "deployment/db", "--", "python", "-c", script).CombinedOutput()
		if e != nil {
			t.Fatalf("storage probe: %v %s", e, out)
		}
		return string(out)
	}
	run("import os\nopen('/data/marker','w').write('durable')\nos.sync()\ns=os.statvfs('/data')\nassert 0<s.f_blocks*s.f_frsize<=1024**3\n")
	target.Revision++
	service := target.Spec.Services["db"]
	service.Env = map[string]string{"ACCEPTANCE_REVISION": "2"}
	target.Spec.Services["db"] = service
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run("print(open('/data/marker').read())"), "durable") {
		t.Fatal("restart lost data")
	}
	after, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "db-data", metav1.GetOptions{})
	if err != nil || after.UID != claim.UID {
		t.Fatal("restart replaced PVC", err)
	}
	run("import os,errno\ntry:\n with open('/data/fill','wb',buffering=0) as f: os.posix_fallocate(f.fileno(),0,2*1024**3)\n raise AssertionError('allocated beyond 1 GiB volume')\nexcept OSError as e:\n assert e.errno==errno.ENOSPC,e\nfinally:\n os.unlink('/data/fill')\n os.sync()\nprint('ENOSPC enforced')")
	t.Log("Sandboxed volume survived revision replacement with the same PVC and rejected writes beyond its filesystem capacity")
}

func hostedStorageTestClient(t *testing.T) (*Client, string) {
	t.Helper()
	if os.Getenv("HAKOPOD_HOSTED_STORAGE_TEST") != "1" {
		t.Skip("requires disposable LVM storage and sandbox on named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development cluster")
	}
	policy := func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{StorageClass: "hakopod-hosted-block", Recreate: true, NodeName: "k3d-shared-free-test-0", Pool: "free", RuntimeClass: "runsc", Quota: map[string]string{"requests.storage": "1Gi", "persistentvolumeclaims": "1", "pods": "2"}}, nil
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, OperatorNodeLimit: 2, WorkloadPolicy: policy, RolloutTimeout: 120 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}
func cleanupHostedStorageTest(t *testing.T, c *Client, target Target) {
	t.Helper()
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		claims, e := c.kube.CoreV1().PersistentVolumeClaims(ns).List(clean, metav1.ListOptions{})
		if e == nil {
			for _, claim := range claims.Items {
				if owned(&claim, target) == nil && claim.Spec.VolumeName != "" {
					pv, e := c.kube.CoreV1().PersistentVolumes().Get(clean, claim.Spec.VolumeName, metav1.GetOptions{})
					if e == nil && pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.UID == claim.UID {
						pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
						_, _ = c.kube.CoreV1().PersistentVolumes().Update(clean, pv, metav1.UpdateOptions{})
					}
				}
			}
		}
		namespace, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(namespace))
		}
	})
}

// Use the catalog's database configuration with the actual hosted Free size and
// sandbox. Passing a generic PVC test alone does not prove these images boot.
func TestLiveHostedDatabaseTemplatesPersistOnFree(t *testing.T) {
	c, path := hostedStorageTestClient(t)
	for _, engine := range []string{"postgresql", "mysql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			app, err := spec.PlanTemplate(engine, spec.TemplateOptions{Name: "db-" + engine + "-" + strconv.FormatInt(time.Now().Unix(), 10), StorageGiB: 1, Size: "small"})
			if err != nil {
				t.Fatal(err)
			}
			service := app.Services["main"]
			if service.Env == nil {
				service.Env = map[string]string{}
			}
			target := Target{ApplicationID: "template-storage-" + engine + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "storage-test", Environment: "test", Revision: 1, Spec: app}
			cleanupHostedStorageTest(t, c, target)
			for _, ref := range service.Secrets {
				name := ref.Ref
				if err = c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, name, "disposable-acceptance-only"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					clean, done := context.WithTimeout(context.Background(), 10*time.Second)
					defer done()
					if err := c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, name); err != nil {
						t.Error(err)
					}
				})
			}

			if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
				t.Fatal(err)
			}
			run := func(script string) string {
				t.Helper()
				out, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "exec", "-n", Namespace(target.ApplicationID), "deployment/main", "--", "sh", "-ec", script).CombinedOutput()
				if e != nil {
					t.Fatalf("database probe: %v %s", e, out)
				}
				return string(out)
			}
			write, read := "", ""
			switch engine {
			case "postgresql":
				write = `psql -U hakopod -d app -v ON_ERROR_STOP=1 -c "CREATE TABLE persistence_check(value text); INSERT INTO persistence_check VALUES ('durable');"`
				read = `psql -U hakopod -d app -Atc 'SELECT value FROM persistence_check'`
			case "mysql":
				prefix := `export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; mysql --socket=/tmp/mysql.sock -u root --batch --skip-column-names app `
				write = prefix + `-e "CREATE TABLE persistence_check(value text); INSERT INTO persistence_check VALUES ('durable');"`
				read = prefix + `-e 'SELECT value FROM persistence_check'`
			case "redis":
				write = `export REDISCLI_AUTH="$REDIS_PASSWORD"; redis-cli SET persistence_check durable; redis-cli SAVE`
				read = `export REDISCLI_AUTH="$REDIS_PASSWORD"; redis-cli --raw GET persistence_check`
			}
			run(write)
			target.Revision++
			service.Env["ACCEPTANCE_REVISION"] = "2"
			target.Spec.Services["main"] = service
			if _, err = c.Deploy(ctx, target, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(run(read), "durable") {
				t.Fatal("database restart lost committed data")
			}
			t.Log("Catalog database persisted data across sandbox restart at Free compute and 1 GiB storage limits")
		})
	}
}
