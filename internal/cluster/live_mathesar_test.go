package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

const mathesarFixtureLabel = "hakopod.io/mathesar-acceptance"
const mathesarFixtureImage = "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"

// This uses the catalog plan unchanged except for explicit development node
// placement. The optional hostPath fixture verifies actual sharing and durable
// bytes on ONE node. It is not a production RWX storage implementation.
func TestLiveMathesarTemplate(t *testing.T) {
	if os.Getenv("HAKOPOD_MATHESAR_TEST") != "1" {
		t.Skip("set HAKOPOD_MATHESAR_TEST=1 for Mathesar runtime acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires explicit k3d-hakopod-dev kubeconfig")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	nodeName := "k3d-hakopod-dev-server-0"
	node, err := c.kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal("named development node unavailable", err)
	}
	runID := fmt.Sprintf("msar-%d", time.Now().UnixNano())
	class := os.Getenv("HAKOPOD_MATHESAR_MEDIA_STORAGE_CLASS")
	localFixture := class == ""
	if localFixture {
		if os.Getenv("HAKOPOD_MATHESAR_SINGLE_NODE_FIXTURE") != "1" {
			t.Fatal("set a real HAKOPOD_MATHESAR_MEDIA_STORAGE_CLASS or explicitly enable HAKOPOD_MATHESAR_SINGLE_NODE_FIXTURE=1")
		}
		class = runID
	}
	options := spec.TemplateOptions{Name: runID, StorageGiB: 1, SiteURL: "https://mathesar.example.test", Values: map[string]string{"media-storage-class": class}}
	app, err := spec.PlanTemplate("mathesar", options)
	if err != nil {
		t.Fatal(err)
	}
	for name, service := range app.Services {
		service.NodeName = nodeName
		service.Architecture = node.Status.NodeInfo.Architecture
		app.Services[name] = service
	}
	mathesarCapacity(t, ctx, c, node, app)
	target := Target{ApplicationID: runID, Project: "mathesar-acceptance", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	labels := labelsFor(target, "")
	labels[mathesarFixtureLabel] = runID
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(runID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var fixtureNS *corev1.Namespace
	var fixturePV *corev1.PersistentVolume
	var fixtureClass *storagev1.StorageClass
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		current, e := c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if e != nil || current.UID != ns.UID || owned(current, target) != nil || current.Labels[mathesarFixtureLabel] != runID {
			t.Error("fixture namespace ownership changed; retaining resources")
			return
		}
		for _, secret := range spec.TemplateSecretNames(app) {
			if e := c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, secret); e != nil {
				t.Error(e)
			}
		}
		claims, e := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(clean, metav1.ListOptions{})
		if e != nil {
			t.Error(e)
			return
		}
		dynamic := []string{}
		for _, claim := range claims.Items {
			if claim.Spec.VolumeName != "" && (fixturePV == nil || claim.Spec.VolumeName != fixturePV.Name) {
				dynamic = append(dynamic, claim.Spec.VolumeName)
			}
		}
		if e = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(current)); e != nil {
			t.Error(e)
			return
		}
		for clean.Err() == nil {
			_, e = c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if clean.Err() != nil {
			t.Error("application namespace did not finish cleanup; retaining fixture files")
			return
		}
		if fixturePV != nil {
			p, e := c.kube.CoreV1().PersistentVolumes().Get(clean, fixturePV.Name, metav1.GetOptions{})
			if e != nil || p.UID != fixturePV.UID || p.Labels[mathesarFixtureLabel] != runID {
				t.Error("fixture PV ownership changed")
				return
			}
			if e = c.kube.CoreV1().PersistentVolumes().Delete(clean, p.Name, deleteOptions(p)); e != nil {
				t.Error(e)
			}
		}
		if fixtureNS != nil {
			n, e := c.kube.CoreV1().Namespaces().Get(clean, fixtureNS.Name, metav1.GetOptions{})
			if e != nil || n.UID != fixtureNS.UID || n.Labels[mathesarFixtureLabel] != runID {
				t.Error("filesystem helper namespace ownership changed")
				return
			}
			if e = mathesarFilesystemPod(clean, c, fixtureNS.Name, nodeName, runID, true); e != nil {
				t.Error("fixture file cleanup", e)
				return
			}
			if e = c.kube.CoreV1().Namespaces().Delete(clean, n.Name, deleteOptions(n)); e != nil {
				t.Error(e)
			}
			for clean.Err() == nil {
				_, e = c.kube.CoreV1().Namespaces().Get(clean, n.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(e) {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if clean.Err() != nil {
				t.Error("filesystem helper namespace cleanup timed out")
				return
			}
		}
		if fixtureClass != nil {
			sc, e := c.kube.StorageV1().StorageClasses().Get(clean, fixtureClass.Name, metav1.GetOptions{})
			if e != nil || sc.UID != fixtureClass.UID || sc.Labels[mathesarFixtureLabel] != runID {
				t.Error("fixture class ownership changed")
				return
			}
			if e = c.kube.StorageV1().StorageClasses().Delete(clean, sc.Name, deleteOptions(sc)); e != nil {
				t.Error(e)
			}
		}
		for _, name := range dynamic {
			for clean.Err() == nil {
				_, e := c.kube.CoreV1().PersistentVolumes().Get(clean, name, metav1.GetOptions{})
				if apierrors.IsNotFound(e) {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
		if clean.Err() != nil {
			t.Error("dynamic database PV cleanup timed out")
		} else {
			t.Log("owned application namespace, media files, PVCs, PVs and fixture storage class cleaned up")
		}
	}()
	if localFixture {
		fixtureNS, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runID, Labels: map[string]string{mathesarFixtureLabel: runID, "pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err = mathesarFilesystemPod(ctx, c, fixtureNS.Name, nodeName, runID, false); err != nil {
			t.Fatal(err)
		}
		fixtureClass, err = c.kube.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class, Labels: map[string]string{mathesarFixtureLabel: runID}}, Provisioner: "kubernetes.io/no-provisioner", VolumeBindingMode: ptr(storagev1.VolumeBindingImmediate)}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		media := app.Volumes["media"]
		size := resource.MustParse(fmt.Sprintf("%dGi", media.SizeGiB))
		fixturePV, err = c.kube.CoreV1().PersistentVolumes().Create(ctx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: runID, Labels: map[string]string{mathesarFixtureLabel: runID}}, Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: size}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, StorageClassName: class, ClaimRef: &corev1.ObjectReference{Namespace: ns.Name, Name: "hakopod-volume-media"}, PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/hakopod-mathesar-fixtures/" + runID + "/media", Type: ptr(corev1.HostPathDirectory)}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{nodeName}}}}}}}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-volume-media", Namespace: ns.Name, Labels: labelsFor(target, "")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, VolumeName: fixturePV.Name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("explicit single-node hostPath media fixture; all application containers remain non-root; production requires an actual RWX storage class")
	}
	for _, secret := range spec.TemplateSecretNames(app) {
		if err = c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, secret, strings.Repeat("DevelopmentFixture7-", 4)); err != nil {
			t.Fatal(err)
		}
	}
	deploy := func() {
		t.Helper()
		if _, e := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); e != nil {
			t.Fatal("Mathesar deployment", e)
		}
	}
	deploy()
	run := func(script string, input any) string {
		t.Helper()
		payload, _ := json.Marshal(input)
		command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns.Name, "exec", "-i", "deployment/backend", "--", "python", "-c", script)
		command.Stdin = strings.NewReader(string(payload))
		out, e := command.CombinedOutput()
		if e != nil {
			if len(out) > 4000 {
				out = out[:4000]
			}
			t.Fatalf("Mathesar behavior probe: %v; %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	credential := map[string]any{"username": "fixtureadmin", "password": "Development-Admin7!" + runID, "initial": true}
	t.Log(run(mathesarHTTPProbe, credential))
	credential["initial"] = false
	t.Log(run(mathesarSettingsProbe, nil))
	t.Log(run(mathesarTLSRefusalProbe, nil))
	for _, name := range []string{"backend", "main", "db"} {
		service, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err != nil || service.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Fatal("unexpected service exposure", name, err)
		}
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimUIDs := map[string]types.UID{}
	for _, claim := range claims.Items {
		claimUIDs[claim.Name] = claim.UID
	}
	for _, name := range []string{"db", "backend", "main"} {
		service := target.Spec.Services[name]
		service.RestartNonce = "persistence-" + name
		target.Spec.Services[name] = service
		target.Revision++
		target.OperationID = "restart-" + name
		deploy()
	}
	t.Log(run(mathesarHTTPProbe, credential))
	for name, uid := range claimUIDs {
		claim, e := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if e != nil || claim.UID != uid {
			t.Fatal("restart replaced persistent claim", name, e)
		}
	}
	// Exercise the external planner against the real, already initialized fixture
	// database. Only the backend is redeployed; the bundled DB remains an owned
	// fixture so external mode can be tested without a second database workload.
	options.Values["database-mode"] = "external"
	options.Values["database-host"] = "db"
	options.Values["database-port"] = "5432"
	options.Values["database-name"] = "mathesar_django"
	options.Values["database-user"] = "mathesar"
	options.Values["database-sslmode"] = "disable"
	external, err := spec.PlanTemplate("mathesar", options)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := external.Services["db"]; exists {
		t.Fatal("external plan unexpectedly provisions database")
	}
	backend := external.Services["backend"]
	backend.NodeName = nodeName
	backend.Architecture = node.Status.NodeInfo.Architecture
	backend.RestartNonce = "external-reattach"
	target.Spec.Services["backend"] = backend
	target.Revision++
	target.OperationID = "external-reattach"
	deploy()
	t.Log(run(mathesarHTTPProbe, credential))
	t.Log(run(mathesarSettingsProbe, nil))
	// Enable TLS only on this disposable PostgreSQL fixture, using a temporary
	// self-signed certificate. The production template is unchanged. A trusted
	// fixture CA permits a real successful handshake; the system CA store and
	// a wrong hostname must both reject the same server under verify-full.
	var certificate struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	}
	if err := json.Unmarshal([]byte(run(mathesarTLSCertificate, nil)), &certificate); err != nil {
		t.Fatal("could not prepare fixture TLS certificate", err)
	}
	for name, body := range map[string]string{"server.crt": certificate.Certificate, "server.key": certificate.Key} {
		command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns.Name, "exec", "-i", "deployment/db", "--", "sh", "-ec", `umask 077; cat > "$PGDATA/$1"`, "fixture", name)
		command.Stdin = strings.NewReader(body)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture TLS file installation failed: %v; %s", err, out)
		}
	}
	db := target.Spec.Services["db"]
	db.Args = append(db.Args, "-c", "ssl=on", "-c", "ssl_cert_file=/var/lib/postgresql/data/pgdata/server.crt", "-c", "ssl_key_file=/var/lib/postgresql/data/pgdata/server.key")
	target.Spec.Services["db"] = db
	target.Revision++
	target.OperationID = "fixture-postgres-tls"
	deploy()
	t.Log(run(mathesarTLSHandshakeProbe, map[string]string{"certificate": certificate.Certificate}))
	t.Log("actual Mathesar setup, static assets, login, CSV upload/download, all-service restart persistence and external database reattachment passed")
}

func mathesarCapacity(t *testing.T, ctx context.Context, c *Client, node *corev1.Node, app spec.Application) {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: 1000})
	if err != nil || pods.Continue != "" {
		t.Fatal("cannot bound node capacity inspection", err)
	}
	var used int64
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		var request int64
		for _, container := range p.Spec.Containers {
			request += container.Resources.Requests.Memory().Value()
		}
		for _, container := range p.Spec.InitContainers {
			if v := container.Resources.Requests.Memory().Value(); v > request {
				request = v
			}
		}
		used += request + p.Spec.Overhead.Memory().Value()
	}
	var needed int64
	for _, service := range app.Services {
		q := resource.MustParse(spec.EffectiveResources(service).MemoryRequest)
		needed += q.Value()
	}
	free := node.Status.Allocatable.Memory().Value() - used
	t.Logf("named development node %s architecture=%s schedulable memory=%dMi template requests=%dMi", node.Name, node.Status.NodeInfo.Architecture, free>>20, needed>>20)
	if node.Spec.Unschedulable || free < needed+(64<<20) {
		t.Fatalf("insufficient safe development capacity: need template requests plus 64Mi headroom; available %dMi, template %dMi", free>>20, needed>>20)
	}
}

func mathesarFilesystemPod(ctx context.Context, c *Client, namespace, nodeName, runID string, remove bool) error {
	action := "prepare"
	capabilities := &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	script := `set -eu; mkdir /fixtures/"$1"; printf '%s' "$1" > /fixtures/"$1"/.owner; mkdir /fixtures/"$1"/media; chmod 0777 /fixtures/"$1"/media`
	if remove {
		action = "cleanup"
		// Uploaded directories belong to application UID 1000. This narrowly
		// scoped development helper needs DAC override to remove their bytes.
		capabilities.Add = []corev1.Capability{"DAC_OVERRIDE"}
		script = `set -eu; test "$(cat /fixtures/"$1"/.owner)" = "$1"; rm -rf -- /fixtures/"$1"/media; rm -- /fixtures/"$1"/.owner; rmdir -- /fixtures/"$1"`
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: action, Namespace: namespace, Labels: map[string]string{mathesarFixtureLabel: runID}}, Spec: corev1.PodSpec{NodeName: nodeName, RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr(false), ActiveDeadlineSeconds: ptr(int64(60)), Containers: []corev1.Container{{Name: "filesystem", Image: mathesarFixtureImage, Command: []string{"sh", "-ec", script, "fixture", runID}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: capabilities}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("8Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("32Mi")}}, VolumeMounts: []corev1.VolumeMount{{Name: "fixture", MountPath: "/fixtures"}}}}, Volumes: []corev1.Volume{{Name: "fixture", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/hakopod-mathesar-fixtures", Type: ptr(corev1.HostPathDirectoryOrCreate)}}}}}}
	created, err := c.kube.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		current, e := c.kube.CoreV1().Pods(namespace).Get(ctx, created.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		if current.UID != created.UID {
			return fmt.Errorf("filesystem helper identity changed")
		}
		if current.Status.Phase == corev1.PodSucceeded {
			return nil
		}
		if current.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("filesystem helper %s failed", action)
		}
		time.Sleep(time.Second)
	}
	return ctx.Err()
}

const mathesarSettingsProbe = `import os,pathlib,django
os.environ.setdefault('DJANGO_SETTINGS_MODULE','config.settings.production');django.setup()
from django.conf import settings
from config.database_config import get_internal_database_config
expected=os.environ.get('POSTGRES_SSLMODE')
if expected:
 assert settings.DATABASES['default']['OPTIONS']['sslmode']==expected
 assert get_internal_database_config().sslmode==expected
assert pathlib.Path('/etc/ssl/certs/ca-certificates.crt').stat().st_size>0
size=sum(p.stat().st_size for p in pathlib.Path('/code/static').rglob('*') if p.is_file())
assert size < 128*1024*1024
assert os.getuid()==1000
print('non-root backend; static bytes='+str(size)+'; CA bundle present; selected TLS mode retained by Django and internal configuration')`

// The bundled PostgreSQL fixture deliberately has no TLS listener. A fresh
// Django process must reject it under verify-full instead of downgrading.
// This proves enforcement, not successful public-CA certificate validation.
const mathesarTLSRefusalProbe = `import os,django
os.environ.update(DJANGO_SETTINGS_MODULE='config.settings.production',POSTGRES_SSLMODE='verify-full',PGSSLMODE='verify-full',PGSSLROOTCERT='/etc/ssl/certs/ca-certificates.crt')
django.setup()
from django.conf import settings
from django.db import connection
from django.db.utils import OperationalError
from config.database_config import get_internal_database_config
assert settings.DATABASES['default']['OPTIONS']['sslmode']=='verify-full'
assert settings.DATABASES['default']['OPTIONS']['sslrootcert']=='/etc/ssl/certs/ca-certificates.crt'
assert get_internal_database_config().sslmode=='verify-full'
try:
 connection.ensure_connection()
except OperationalError as e:
 assert 'server does not support SSL' in str(e),type(e).__name__
else:
 raise AssertionError('verify-full silently accepted a plaintext PostgreSQL server')
print('verify-full retained by both configuration paths and rejected actual plaintext PostgreSQL')`

const mathesarTLSCertificate = `import json,pathlib,subprocess,tempfile
with tempfile.TemporaryDirectory() as directory:
 cert=pathlib.Path(directory)/'server.crt';key=pathlib.Path(directory)/'server.key'
 subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(key),'-out',str(cert),'-subj','/CN=db','-addext','subjectAltName=DNS:db','-addext','basicConstraints=critical,CA:TRUE'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)
 print(json.dumps({'certificate':cert.read_text(),'key':key.read_text()}))`

const mathesarTLSHandshakeProbe = `import os,json,sys,tempfile,socket,django,psycopg
p=json.load(sys.stdin)
with tempfile.NamedTemporaryFile(mode='w',suffix='.crt') as ca:
 ca.write(p['certificate']);ca.flush()
 os.environ.update(DJANGO_SETTINGS_MODULE='config.settings.production',POSTGRES_SSLMODE='verify-full',PGSSLMODE='verify-full',PGSSLROOTCERT=ca.name)
 django.setup()
 from django.conf import settings
 from django.db import connection
 from config.database_config import get_internal_database_config
 assert settings.DATABASES['default']['OPTIONS']['sslmode']=='verify-full'
 assert settings.DATABASES['default']['OPTIONS']['sslrootcert']==ca.name
 assert get_internal_database_config().sslmode=='verify-full'
 with connection.cursor() as cursor:
  cursor.execute('SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()');assert cursor.fetchone()==(True,)
 kwargs=dict(host='db',port=5432,dbname=os.environ['POSTGRES_DB'],user=os.environ['POSTGRES_USER'],password=os.environ['POSTGRES_PASSWORD'],sslmode='verify-full',connect_timeout=5)
 try:
  psycopg.connect(**kwargs,sslrootcert='/etc/ssl/certs/ca-certificates.crt').close()
 except psycopg.OperationalError as e:
  assert 'certificate verify failed' in str(e)
 else:raise AssertionError('untrusted fixture certificate accepted')
 kwargs['hostaddr']=socket.gethostbyname('db');kwargs['host']='wrong.example.test'
 try:
  psycopg.connect(**kwargs,sslrootcert=ca.name).close()
 except psycopg.OperationalError as e:
  assert 'does not match host name' in str(e)
 else:raise AssertionError('wrong database certificate hostname accepted')
print('real verify-full TLS handshake succeeded with fixture CA; untrusted CA and wrong hostname rejected')`

const mathesarHTTPProbe = `import json,sys,re,requests
p=json.load(sys.stdin)
s=requests.Session();s.headers.update({'Host':'mathesar.example.test','Origin':'https://mathesar.example.test','Referer':'https://mathesar.example.test/'})
base='http://main:8080'
# The development probe reaches Caddy's private HTTP listener. Explicitly
# replay Secure cookies here while verifying their flags; this is not a claim
# that this probe validates the public ingress TLS/browser path.
def request(method,path,**kwargs):
 headers=kwargs.pop('headers',{})
 headers['Cookie']='; '.join(c.name+'='+c.value for c in s.cookies)
 return s.request(method,base+path,headers=headers,timeout=30,**kwargs)
def get(path,**kwargs):return request('GET',path,**kwargs)
assert get('/healthz/ready/').status_code==200
assert get('/healthz/live/').status_code==200
assert get('/',headers={'Host':'unknown.example.test'},allow_redirects=False).status_code==404
page=get('/complete_installation/' if p['initial'] else '/auth/login/')
assert page.status_code==200
assets=re.findall(r'''(?:src|href)=["'](/static/[^"']+)''',page.text)
assert assets,'no static asset references'
for path in assets[:3]:
 r=get(path);assert r.status_code==200 and len(r.content)>0
csrf=re.search(r'name="csrfmiddlewaretoken" value="([^"]+)"',page.text).group(1)
assert any(c.name=='csrftoken' and c.secure for c in s.cookies)
if p['initial']:
 r=request('POST','/complete_installation/',data={'csrfmiddlewaretoken':csrf,'username':p['username'],'password1':p['password'],'password2':p['password']},allow_redirects=False)
 assert r.status_code==302,('setup',r.status_code)
else:
 r=request('POST','/auth/login/',data={'csrfmiddlewaretoken':csrf,'username':p['username'],'password':p['password']},allow_redirects=False)
 assert r.status_code==302,('login',r.status_code)
assert any(c.name=='sessionid' and c.secure for c in s.cookies)
assert get('/complete_installation/',allow_redirects=False).status_code==302
csv=b'name,value\nfixture,42\n'
if p['initial']:
 r=request('POST','/api/db/v0/data_files/',headers={'X-CSRFToken':s.cookies.get('csrftoken')},files={'file':('acceptance.csv',csv,'text/csv')},data={'header':'true'})
 assert r.status_code==201,('upload',r.status_code,r.text[:500])
files=get('/api/db/v0/data_files/').json()['results'];assert len(files)==1
from urllib.parse import urlsplit
url=files[0]['file'];assert url.startswith('https://mathesar.example.test/')
r=get(urlsplit(url).path);assert r.status_code==200 and r.content==csv
print('HTTP health, host rejection, static assets, administrator session and persisted CSV download passed')`
