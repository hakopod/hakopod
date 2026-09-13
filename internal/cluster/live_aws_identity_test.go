package cluster

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveAWSIdentityTokenIsolation(t *testing.T) {
	if os.Getenv("HAKOPOD_AWS_IDENTITY_TEST") != "1" {
		t.Skip("set HAKOPOD_AWS_IDENTITY_TEST=1 for isolated development identity acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("AWS identity acceptance requires the named k3d-hakopod-dev context", err)
	}
	app, err := spec.Normalize(spec.Application{Name: "aws-identity-fixture", Services: map[string]spec.Service{"api": {
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", AWSIdentity: "sender", ReadOnlyRootFilesystem: true,
		RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, Command: []string{"python", "-B", "-c", "import time; time.sleep(600)"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "aws-identity-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "aws-fixture", Environment: "test", OperationID: "aws-initial", Revision: 1, Spec: app}
	binding := awsBindingFixture(target)
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second, AWSIdentityBindings: []AWSIdentityBinding{binding}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		namespace, err := c.kube.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
		if err == nil {
			if err := owned(namespace, target); err != nil {
				t.Error(err)
				return
			}
			if err := c.kube.CoreV1().Namespaces().Delete(cleanup, ns, deleteOptions(namespace)); err != nil {
				t.Error(err)
			}
		}
	})
	if observed, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	} else if observed.Status != "healthy" {
		t.Fatalf("ready workload was not observed healthy: %+v", observed)
	}
	code := `import base64,json,os,socket,sys,errno
p=os.environ['AWS_WEB_IDENTITY_TOKEN_FILE']
assert os.getuid()==12345 and os.getgid()==23456
assert p=='/var/run/secrets/hakopod/aws/token'
token=open(p).read().split('.')[1]
claims=json.loads(base64.urlsafe_b64decode(token+'='*((4-len(token)%4)%4)))
assert claims['aud']==['sts.amazonaws.com']
assert claims['sub']==sys.argv[1]
assert os.environ['AWS_EC2_METADATA_DISABLED']=='true'
assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token')
try:
 open(p,'w');raise AssertionError('token mount writable')
except OSError as e: assert e.errno in (errno.EROFS,errno.EACCES)
s=socket.socket();s.settimeout(2)
try:
 s.connect(('169.254.169.254',80));raise AssertionError('metadata endpoint reachable')
except OSError: pass
finally: s.close()
print('AWS token projection, filesystem permissions and metadata isolation verified')
`
	args := []string{"--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/api", "--", "python", "-B", "-c", code, "system:serviceaccount:" + ns + ":" + AWSIdentityServiceAccount(binding)}
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("identity projection acceptance: %v %s", err, out)
	}
	if !strings.Contains(string(out), "isolation verified") {
		t.Fatal("projection proof missing")
	}
	previous := target.Spec
	svc := target.Spec.Services["api"]
	svc.AWSIdentity = ""
	target.Spec.Services = map[string]spec.Service{"api": svc}
	target.Previous = &previous
	target.Revision++
	target.OperationID = "aws-remove"
	if observed, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	} else if observed.Status != "healthy" {
		t.Fatalf("ready workload was not observed healthy: %+v", observed)
	}
	deployment, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || deployment.Spec.Template.Spec.ServiceAccountName != "" || len(deployment.Spec.Template.Spec.Volumes) != 0 {
		t.Fatal("removing identity retained its token or account on new pods", err)
	}
	t.Log("Real projected AWS token readable by configured UID/GID, read-only mount, exact subject/audience, Kubernetes API audience rejection, metadata network block and identity removal verified; no AWS access attempted")
}
