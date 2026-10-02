package cluster

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"go.mongodb.org/mongo-driver/v2/bson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedMongoDBBindingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MONGODB_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MONGODB_TEST=1")
	}
	// Initial replica-set provisioning, application rollout and certificate
	// renewal each need their own bounded convergence time.
	c, ctx := liveRecoveryClient(t, 30*time.Minute)
	d, password := newMongoDBFixture(t, ctx, c, "cluster")
	health := waitMongoDBFixture(t, ctx, c, d, password)
	endpoint := health.Endpoints[0]
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	ca := trust.CertificatePEM
	connection := url.URL{Scheme: "mongodb", Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), Path: "/app", User: url.UserPassword("app", string(password))}
	query := connection.Query()
	for key, value := range map[string]string{"tls": "true", "tlsCAFile": DatabaseTrustPath(d.ID), "replicaSet": "database", "authSource": "app", "w": "majority", "readConcernLevel": "majority", "readPreference": "primary", "retryWrites": "true"} {
		query.Set(key, value)
	}
	connection.RawQuery = query.Encode()
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, _ spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || environment != d.Environment {
			return nil, fmt.Errorf("wrong MongoDB binding scope")
		}
		return map[string]map[string]DatabaseConnection{"allowed": {"DATABASE_URL": {URL: connection.String(), Port: int32(endpoint.Port), CA: ca}}}, nil
	})
	service := spec.Service{Image: mongodbServerImage, Command: []string{"sleep", "1200"}, ReadOnlyRootFilesystem: true, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "300m", MemoryRequest: "32Mi", MemoryLimit: "256Mi"}}
	allowed := service
	allowed.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "mongodb", Endpoint: "cluster", ClusterAware: true}}
	app, err := spec.Normalize(spec.Application{Name: "mongodb-binding-development-fixture", Services: map[string]spec.Service{"allowed": allowed, "unbound": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "mongodb-binding-fixture-" + d.ID, Project: d.Project, Environment: d.Environment, Spec: app, Revision: 1, OperationID: "mongodb-binding-create"}
	t.Log("Development MongoDB application namespace", Namespace(target.ApplicationID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		ns, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe := func(service, script, want string) {
		t.Helper()
		step, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(step, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/"+service, "--", "bash", "-c", script).Output()
		actual := strings.TrimSpace(string(out))
		// mongosh can write startup warnings on stdout when its application
		// container has a read-only filesystem. Require one explicit result.
		if strings.Contains(script, "probe-result:") {
			actual = ""
			results := 0
			for _, line := range strings.Split(string(out), "\n") {
				if value, ok := strings.CutPrefix(line, "probe-result:"); ok {
					actual = strings.TrimSpace(value)
					results++
				}
			}
			if results != 1 {
				actual = "missing or duplicate result"
			}
		}
		if err != nil || actual != want {
			reason := "no classified client error"
			for _, category := range []string{"discovery", "authentication", "authorization", "native"} {
				if strings.Contains(string(out), "probe-error:"+category) {
					reason = category
				}
			}
			t.Fatalf("MongoDB %s binding probe failed; expected %s (%v; %s)", service, want, err, reason)
		}
	}
	// The URI remains in the workload environment. Exceptions are deliberately
	// suppressed because drivers can include connection details in errors.
	native := func(js string) string {
		return `MONGOSH_DISABLE_TELEMETRY=1 mongosh --nodb --norc --quiet --eval 'try {const result=(value)=>print("probe-result:"+value);const c=new Mongo(process.env.DATABASE_URL);const db=c.getDB("app");` + js + `} catch(e) {print("probe-error:"+(e.name==="MongoServerSelectionError"?"discovery":e.code===18?"authentication":e.code===13?"authorization":"native"));quit(2)}'`
	}
	// Reusing a retained fixture must not collide with an earlier probe's write.
	recordID := bson.NewObjectID().Hex()
	readRecord := fmt.Sprintf(`const value=db.records.findOne({_id:%q}).value;result(value.sub_type===128?value.toString("base64"):"wrong-subtype")`, recordID)
	probe("allowed", native(fmt.Sprintf(`const r=db.records.insertOne({_id:%q,value:BinData(128,"AAr/gA==")});result(r.acknowledged?"ok":"failed")`, recordID)), "ok")
	probe("allowed", native(readRecord), "AAr/gA==")
	probe("allowed", native(`const h=db.adminCommand({hello:1});result(h.setName==="database"&&h.hosts.length===3?"discovered":"failed")`), "discovered")
	for _, member := range health.Members {
		blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/27017' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, mongodbMemberHost(d, member))
		probe("allowed", blocked, "reachable")
		probe("unbound", blocked, "blocked")
	}
	secret, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "allowed-environment", metav1.GetOptions{})
	if err != nil || len(secret.Data) != 1 || string(secret.Data["DATABASE_URL"]) != connection.String() {
		t.Fatal("MongoDB binding received unexpected credentials")
	}
	health = testMongoDBRenewal(t, ctx, c, d, health)
	trust, err = c.DatabaseTrust(ctx, d)
	if err != nil || trust.CertificatePEM == ca {
		t.Fatal("MongoDB binding trust did not renew")
	}
	ca = trust.CertificatePEM
	if err = c.RenewDatabaseTrust(ctx, target, nil, "allowed"); err != nil {
		t.Fatal(err)
	}
	if err = exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "rollout", "status", "deployment/allowed", "--timeout=120s").Run(); err != nil {
		t.Fatal("MongoDB application trust rollout failed")
	}
	probe("allowed", native(readRecord), "AAr/gA==")
	target.Previous = &app
	next := app
	next.Services = map[string]spec.Service{"allowed": service, "unbound": service}
	target.Spec = next
	target.Revision = 2
	target.OperationID = "mongodb-binding-remove"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/27017' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, endpoint.Host)
	probe("allowed", blocked, "blocked")
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/database-access-"+d.ID] != "" {
		t.Fatal("MongoDB binding revocation retained its network grant")
	}
	testMongoDBCredentialLogs(t, ctx, c, d)
	t.Log("MongoDB application driver discovered every member with verified TLS, scoped credentials, public trust renewal and network revocation")
}
