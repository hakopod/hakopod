package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// The application container runs the packaged helper against disposable real
// database servers. The resolver below supplies development fixture values;
// this is not a managed-engine lifecycle or external-provider qualification.
func TestLiveApplicationBindingConnection(t *testing.T) {
	if os.Getenv("HAKOPOD_BINDING_PROBE_TEST") != "1" {
		t.Skip("requires the named development cluster and an imported probe image")
	}
	path, image := os.Getenv("HAKOPOD_TEST_KUBECONFIG"), os.Getenv("HAKOPOD_TEST_READINESS_IMAGE")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" || image == "" || ValidateReadinessProbeImage(image) != nil {
		t.Fatal("binding probe acceptance requires k3d-hakopod-dev and a digest-pinned helper")
	}
	c, err := New(path, Options{RolloutTimeout: 150 * time.Second, ReadinessProbeImage: image})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	ca, certificate, privateKey := bindingProbeFixtureCertificate(t)
	wrongCA, _, _ := bindingProbeFixtureCertificate(t)
	const dbID = "1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a"
	const badTrustID = "2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b"
	const fixturePassword = "disposable-binding-probe-password"
	const pythonImage = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	bindings := map[string]spec.Binding{}
	connections := map[string]DatabaseConnection{}
	add := func(variable, protocol, host, database, user, password, trustID string, port int32, trust []byte) {
		bindings[variable] = spec.Binding{ManagedDatabase: trustID, Protocol: protocol, Endpoint: "read_write"}
		scheme := protocol
		if protocol == "redis" {
			scheme = "rediss"
		}
		value := (&url.URL{Scheme: scheme, Host: fmt.Sprintf("%s:%d", host, port), Path: "/" + database, User: url.UserPassword(user, password)}).String()
		connections[variable] = DatabaseConnection{URL: value, Port: port, CA: string(trust)}
	}
	add("PG_OK", "postgres", "postgres", "fixture", "fixture", fixturePassword, dbID, 5432, ca)
	add("PG_AUTH", "postgres", "postgres", "fixture", "fixture", "wrong-fixture-password", dbID, 5432, ca)
	add("PG_DATABASE", "postgres", "postgres", "absent", "fixture", fixturePassword, dbID, 5432, ca)
	add("PG_HOSTNAME", "postgres", "placeholder", "fixture", "fixture", fixturePassword, dbID, 5432, ca)
	add("PG_CA", "postgres", "postgres", "fixture", "fixture", fixturePassword, badTrustID, 5432, wrongCA)
	add("PG_DNS", "postgres", "missing-database.invalid", "fixture", "fixture", fixturePassword, dbID, 5432, ca)
	add("PG_NETWORK", "postgres", "no-listener", "fixture", "fixture", fixturePassword, dbID, 5432, ca)
	add("REDIS_OK", "redis", "redis", "0", "default", fixturePassword, dbID, 6379, ca)
	add("REDIS_AUTH", "redis", "redis", "0", "default", "wrong-fixture-password", dbID, 6379, ca)
	add("REDIS_PERMISSION", "redis", "redis", "0", "restricted", fixturePassword, dbID, 6379, ca)
	add("MYSQL_OK", "mysql", "mysql", "fixture", "fixture", fixturePassword, dbID, 3306, ca)
	add("MYSQL_AUTH", "mysql", "mysql", "fixture", "fixture", "wrong-fixture-password", dbID, 3306, ca)
	add("MYSQL_DATABASE", "mysql", "mysql", "absent", "fixture", fixturePassword, dbID, 3306, ca)
	client := spec.Service{Image: pythonImage, Command: []string{"python", "-B", "-c", "import time; time.sleep(1200)"}, Bindings: bindings, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true, Size: "small"}
	app, err := spec.Normalize(spec.Application{Name: "binding-probe-development-fixture", Services: map[string]spec.Service{
		"client": client,
		"postgres": {Image: "docker.io/library/postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73", Port: 5432, Size: "medium", RunAsUser: 70, RunAsGroup: 70, FSGroup: 70,
			Volume: &spec.Volume{MountPath: "/var/lib/postgresql/data", SizeGiB: 1}, Env: map[string]string{"PGDATA": "/var/lib/postgresql/data/pgdata", "POSTGRES_DB": "fixture", "POSTGRES_USER": "fixture", "POSTGRES_INITDB_ARGS": "--auth-host=scram-sha-256"}, Secrets: map[string]spec.SecretRef{"POSTGRES_PASSWORD": {Ref: "fixture-password"}},
			Args: []string{"postgres", "-c", "shared_buffers=32MB", "-c", "max_connections=20", "-c", "ssl=on", "-c", "ssl_cert_file=/certificates/database/tls.crt", "-c", "ssl_key_file=/certificates/database/tls.key"}},
		"redis": {Image: "docker.io/library/redis:8.6.6-alpine@sha256:75934ddb37bfaebe3b4082ba673cac39f66495244134f33dd0a502ce03cdcd36", Port: 6379, Size: "small", RunAsUser: 999, RunAsGroup: 999, FSGroup: 999,
			Secrets: map[string]spec.SecretRef{"REDIS_PASSWORD": {Ref: "fixture-password"}}, Command: []string{"sh", "-ec"},
			Args: []string{`exec redis-server --save '' --appendonly no --maxmemory 32mb --requirepass "$REDIS_PASSWORD" --port 0 --tls-port 6379 --tls-auth-clients no --tls-cert-file /certificates/database/tls.crt --tls-key-file /certificates/database/tls.key --user restricted on ">$REDIS_PASSWORD" '~*' '+auth' '-ping'`}},
		"mysql": {Image: "docker.io/library/mysql:8.4.11@sha256:85b9bf2e29cf836ecb8c2a15a935d4ba0c606631dff1dd79531a11983c638f2a", Port: 3306, Size: "large", RunAsUser: 999, RunAsGroup: 999, FSGroup: 999,
			Volume: &spec.Volume{MountPath: "/var/lib/mysql", SizeGiB: 1}, Env: map[string]string{"MYSQL_DATABASE": "fixture", "MYSQL_USER": "fixture"}, Secrets: map[string]spec.SecretRef{"MYSQL_PASSWORD": {Ref: "fixture-password"}, "MYSQL_ROOT_PASSWORD": {Ref: "fixture-password"}},
			Args: []string{"--socket=/tmp/mysql.sock", "--pid-file=/tmp/mysql.pid", "--innodb-buffer-pool-size=32M", "--max-connections=12", "--performance-schema=OFF", "--mysqlx=0", "--require-secure-transport=ON", "--ssl-cert=/certificates/database/tls.crt", "--ssl-key=/certificates/database/tls.key"}},
		"no-listener": {Image: pythonImage, Port: 5432, Command: []string{"python", "-B", "-c", "import time; time.sleep(1200)"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("binding-probe-%d", time.Now().UnixNano()), Project: "binding-probe-fixture", Environment: "development", OperationID: "binding-probe-fixture", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	badHost := connections["PG_HOSTNAME"]
	badHost.URL = strings.Replace(badHost.URL, "placeholder", "postgres."+ns+".svc", 1)
	connections["PG_HOSTNAME"] = badHost
	c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return map[string]map[string]DatabaseConnection{"client": connections}, nil
	})
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 150*time.Second)
		defer done()
		namespace, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(namespace)); e != nil {
				t.Error("owned binding fixture cleanup failed")
			}
		}
		if e = c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, "fixture-password"); e != nil {
			t.Error("owned fixture credential cleanup failed")
		}
		for clean.Err() == nil {
			_, e = c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				volumes, listErr := c.kube.CoreV1().PersistentVolumes().List(clean, metav1.ListOptions{Limit: 512})
				if listErr != nil || volumes.Continue != "" {
					t.Error("could not verify bounded fixture volume cleanup")
					return
				}
				retained := false
				for _, volume := range volumes.Items {
					retained = retained || volume.Spec.ClaimRef != nil && volume.Spec.ClaimRef.Namespace == ns
				}
				if !retained {
					return
				}
			}
			_ = sleepContext(clean, time.Second)
		}
		t.Error("binding fixture namespace or persistent volumes did not finish cleanup")
	})
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "fixture-password", fixturePassword); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres", "redis", "mysql"} {
		uploaded, e := c.PutBackendCertificate(ctx, target, name, "binding.fixture.example.com", certificate, privateKey)
		if e != nil {
			t.Fatal("could not prepare fixture server certificate", e)
		}
		svc := target.Spec.Services[name]
		svc.CertificateMounts = []spec.CertificateMount{{Certificate: uploaded.Certificate, Hostname: "binding.fixture.example.com", MountPath: "/certificates/database"}}
		target.Spec.Services[name] = svc
	}
	// Create the deliberately unreachable Service without waiting for it to
	// become ready. All other resources use the ordinary deployment path.
	if err = c.applyService(ctx, target, "no-listener", target.Spec.Services["no-listener"]); err != nil {
		t.Fatal(err)
	}
	delete(target.Spec.Services, "no-listener")
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal("binding fixture deployment failed", err)
	}
	// Deploy prunes removed services. Recreate only this owned no-endpoint
	// Service for a deterministic connection-refused check.
	_, err = c.kube.CoreV1().Services(ns).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "no-listener", Labels: labelsFor(target, "no-listener")}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 5432}}, Selector: map[string]string{"development-fixture": "no-such-pod"}}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	authorize := func(context.Context) error { return nil }
	var successful BindingTestResult
	for _, tc := range []struct{ variable, outcome, code string }{
		{"PG_OK", "passed", "read_query_succeeded"}, {"PG_AUTH", "failed", "authentication_rejected"}, {"PG_DATABASE", "failed", "database_access_rejected"},
		{"PG_HOSTNAME", "failed", "tls_verification_failed"}, {"PG_CA", "failed", "tls_verification_failed"}, {"PG_DNS", "failed", "dns_lookup_failed"}, {"PG_NETWORK", "failed", "network_unreachable"},
		{"REDIS_OK", "passed", "read_query_succeeded"}, {"REDIS_AUTH", "failed", "authentication_rejected"}, {"REDIS_PERMISSION", "failed", "read_query_rejected"},
		{"MYSQL_OK", "passed", "read_query_succeeded"}, {"MYSQL_AUTH", "failed", "authentication_rejected"}, {"MYSQL_DATABASE", "failed", "database_access_rejected"},
	} {
		t.Run(tc.variable, func(t *testing.T) {
			result, e := c.TestServiceBinding(ctx, target, "client", tc.variable, "", authorize)
			if e != nil || result.Outcome != tc.outcome || len(result.Stages) == 0 || result.Stages[len(result.Stages)-1].Code != tc.code {
				t.Fatalf("unexpected sanitized connection result: outcome=%s stages=%v error=%v", result.Outcome, result.Stages, e)
			}
			if result.PodUID == "" || result.Pod == "" || result.LoadedMatchesSnapshot == nil || !*result.LoadedMatchesSnapshot {
				t.Fatal("result did not verify the exact container environment snapshot")
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), fixturePassword) || strings.Contains(string(encoded), "wrong-fixture-password") || strings.Contains(string(encoded), "loaded_fingerprint") {
				t.Fatal("public result exposed a credential or fingerprint")
			}
			if tc.variable == "PG_OK" {
				successful = result
			}
		})
	}
	if successful.Pod == "" {
		t.Fatal("successful in-container test unavailable")
	}
	pod, err := c.kube.CoreV1().Pods(ns).Get(ctx, successful.Pod, metav1.GetOptions{})
	if err != nil || len(pod.Spec.Containers) != 1 || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || *pod.Spec.SecurityContext.RunAsUser != 12345 || pod.Status.ContainerStatuses[0].RestartCount != 0 {
		t.Fatal("probe changed workload isolation or restarted the application")
	}
	measurement := `import json,os,resource,subprocess
path='/var/run/secrets/hakopod-probe/hakopod-probe'
request={'schema_version':1,'protocol':'postgres','variable':'PG_OK','ca_file':'` + DatabaseTrustPath(dbID) + `','timeout_ms':10000}
r=subprocess.run([path,'connection'],input=json.dumps(request),capture_output=True,text=True,timeout=15)
assert r.returncode==0 and json.loads(r.stdout)['stages'][-1]['code']=='read_query_succeeded'
peak=resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
assert peak<65536
print(json.dumps({'binary_bytes':os.path.getsize(path),'peak_rss_kib':peak}))`
	measured, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", successful.Pod, "-c", "app", "--", "python", "-B", "-c", measurement).Output()
	if err != nil {
		t.Fatal("could not measure the installed helper within the 64 MiB bound")
	}
	var footprint struct {
		BinaryBytes int64 `json:"binary_bytes"`
		PeakRSSKiB  int64 `json:"peak_rss_kib"`
	}
	if json.Unmarshal(measured, &footprint) != nil || footprint.BinaryBytes < 1 || footprint.PeakRSSKiB < 1 {
		t.Fatal("helper footprint result was invalid")
	}
	t.Logf("Installed helper: %d bytes; measured peak resident memory: %d KiB", footprint.BinaryBytes, footprint.PeakRSSKiB)
	// A saved value can change before its replacement pod starts. Successful
	// authentication from the old pod must not be reported as current.
	nextConnection := connections["PG_OK"]
	nextConnection.URL = strings.Replace(nextConnection.URL, fixturePassword, "new-saved-value", 1)
	connections["PG_OK"] = nextConnection
	stale, err := c.TestServiceBinding(ctx, target, "client", "PG_OK", successful.Pod, authorize)
	if err != nil || stale.Outcome != "stale" || stale.LoadedMatchesSnapshot == nil || *stale.LoadedMatchesSnapshot {
		t.Fatal("an old container verified newer saved settings")
	}
	denied := errors.New("fixture authorization revoked")
	if _, err = c.TestServiceBinding(ctx, target, "client", "PG_OK", "", func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal("runtime test ignored revoked authorization")
	}
	foreign, err := c.TestServiceBinding(ctx, target, "client", "PG_OK", "foreign-pod", authorize)
	if err != nil || foreign.Outcome != "unavailable" || foreign.Pod != "" {
		t.Fatal("runtime test selected a pod outside its requested target")
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	start := time.Now()
	_, _ = c.TestServiceBinding(stopped, target, "client", "PG_OK", "", authorize)
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancelled runtime test did not stop promptly")
	}
	t.Log("Actual application container verified PostgreSQL, Redis and MySQL TLS/authentication/read queries; rejected wrong CA, hostname, password, permissions, missing database, DNS and network; detected stale settings and enforced ownership/cancellation. No credentials or fingerprints left the cluster API.")
}

func bindingProbeFixtureCertificate(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "Disposable binding probe CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano() + 1), DNSNames: []string{"postgres", "redis", "mysql", "binding.fixture.example.com"}, Subject: pkix.Name{CommonName: "binding.fixture.example.com"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
