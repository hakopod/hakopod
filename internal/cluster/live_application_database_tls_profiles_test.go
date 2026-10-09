package cluster

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
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

func TestLiveApplicationDatabaseTLSProfiles(t *testing.T) {
	if os.Getenv("HAKOPOD_APPLICATION_TLS_PROFILE_TEST") != "1" {
		t.Skip("opt-in application database TLS profile acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("application TLS acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 12 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	ca, certificate, key := applicationTLSProfileCertificate(t)
	const postgresID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const redisID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("infisical", func(t *testing.T) {
		app, err := spec.PlanTemplate("infisical", spec.TemplateOptions{Name: "tls-profile-infisical", StorageGiB: 1, SiteURL: "https://catalog.example.test"})
		if err != nil {
			t.Fatal(err)
		}
		applicationTLSProfileServers(t, &app, ca, certificate)
		main := app.Services["main"]
		main.Mounts = nil
		delete(main.Secrets, "DB_CONNECTION_URI")
		delete(main.Secrets, "REDIS_URL")
		main.Bindings = map[string]spec.Binding{
			"DB_CONNECTION_URI": {ManagedDatabase: postgresID, Protocol: "postgres", Endpoint: "read_write"},
			"REDIS_URL":         {ManagedDatabase: redisID, Protocol: "redis", Endpoint: "read_write"},
		}
		app.Services["main"] = main
		app, err = spec.Normalize(app)
		if err != nil {
			t.Fatal(err)
		}
		applicationTLSProfileDeploy(t, ctx, c, app, "tls-profile-infisical", map[string]string{
			"auth-secret":       base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
			"database-password": "InfisicalFixturePassword123",
			"encryption-key":    "0123456789abcdef0123456789abcdef",
			"redis-password":    "InfisicalRedisFixturePassword123",
			"tls-server-key":    string(key),
		}, map[string]map[string]DatabaseConnection{"main": {
			"DB_CONNECTION_URI": {URL: "postgres://hakopod:InfisicalFixturePassword123@db:5432/app?sslmode=verify-full&sslrootcert=" + DatabaseTrustPath(postgresID), Port: 5432, CA: string(ca)},
			"REDIS_URL":         {URL: "rediss://:InfisicalRedisFixturePassword123@redis:6379/0", Port: 6379, CA: string(ca)},
		}}, func(namespace string) {
			applicationTLSProfileExec(t, ctx, namespace, "node", "-e", `const fs=require('fs'); const root=Buffer.from(process.env.DB_ROOT_CERT,'base64').toString('utf8'); if(!root.includes('BEGIN CERTIFICATE')) throw Error('missing DB_ROOT_CERT'); if(process.env.NODE_EXTRA_CA_CERTS!='/var/run/secrets/hakopod-database/node-extra-ca-v1.pem'||!fs.existsSync(process.env.NODE_EXTRA_CA_CERTS)) throw Error('missing NODE_EXTRA_CA_CERTS'); fetch('http://127.0.0.1:8080/api/status').then(r=>{if(!r.ok) throw Error('health status '+r.status); console.log('healthy')}).catch(e=>{console.error(e.message);process.exit(4)})`)
		})
	})

	t.Run("glitchtip", func(t *testing.T) {
		app, err := spec.PlanTemplate("glitchtip", spec.TemplateOptions{Name: "tls-profile-glitchtip", StorageGiB: 1, SiteURL: "https://catalog.example.test", Values: map[string]string{"email": "fixture@example.test"}})
		if err != nil {
			t.Fatal(err)
		}
		applicationTLSProfileServers(t, &app, ca, certificate)
		main := app.Services["main"]
		main.Mounts = nil
		delete(main.Secrets, "DATABASE_URL")
		delete(main.Env, "VALKEY_URL")
		main.Bindings = map[string]spec.Binding{
			"DATABASE_URL": {ManagedDatabase: postgresID, Protocol: "postgres", Endpoint: "read_write"},
			"VALKEY_URL":   {ManagedDatabase: redisID, Protocol: "redis", Endpoint: "read_write"},
		}
		app.Services["main"] = main
		app, err = spec.Normalize(app)
		if err != nil {
			t.Fatal(err)
		}
		applicationTLSProfileDeploy(t, ctx, c, app, "tls-profile-glitchtip", map[string]string{"secret-key": "glitchtip-fixture-secret-key-0123456789", "tls-server-key": string(key)}, map[string]map[string]DatabaseConnection{"main": {
			"DATABASE_URL": {URL: "postgres://postgres@postgres:5432/postgres?sslmode=verify-full&sslrootcert=" + DatabaseTrustPath(postgresID), Port: 5432, CA: string(ca)},
			"VALKEY_URL":   {URL: "rediss://valkey:6379/0", Port: 6379, CA: string(ca)},
		}}, func(namespace string) {
			applicationTLSProfileExec(t, ctx, namespace, "python", "-c", `import os,urllib.request; assert os.environ['SSL_CERT_FILE']=='/etc/ssl/certs/ca-certificates.crt'; assert os.environ['SSL_CERT_DIR']=='/var/run/secrets/hakopod-database'; assert os.path.isfile(os.environ['SSL_CERT_FILE']); assert os.path.isdir(os.environ['SSL_CERT_DIR']); request=urllib.request.Request('http://127.0.0.1:8080/',headers={'Host':'catalog.example.test'}); response=urllib.request.urlopen(request,timeout=10); assert response.status in (200,302); print('healthy')`)
		})
	})
}

func applicationTLSProfileServers(t *testing.T, app *spec.Application, ca, certificate []byte) {
	t.Helper()
	app.Volumes = nil
	for _, name := range []string{"db", "postgres", "redis", "valkey"} {
		service, exists := app.Services[name]
		if !exists {
			continue
		}
		service.Volume = nil
		service.Mounts = nil
		service.Files = map[string]spec.File{
			"tls-ca":          {MountPath: "/fixture-tls/ca.crt", Content: ptr(string(ca)), Mode: 0444},
			"tls-certificate": {MountPath: "/fixture-tls/server.crt", Content: ptr(string(certificate)), Mode: 0444},
			"tls-key":         {MountPath: "/fixture-tls/server.key", Secret: &spec.SecretRef{Ref: "tls-server-key"}, Mode: 0440},
		}
		switch name {
		case "db", "postgres":
			service.Args = append(service.Args, "-c", "ssl=on", "-c", "ssl_cert_file=/fixture-tls/server.crt", "-c", "ssl_key_file=/fixture-tls/server.key", "-c", "ssl_ca_file=/fixture-tls/ca.crt")
		case "redis":
			service.Args[0] += ` --port 0 --tls-port 6379 --tls-auth-clients no --tls-ca-cert-file /fixture-tls/ca.crt --tls-cert-file /fixture-tls/server.crt --tls-key-file /fixture-tls/server.key`
		case "valkey":
			service.Command = []string{"sh", "-ec"}
			service.Args = []string{`exec valkey-server --port 0 --tls-port 6379 --tls-auth-clients no --tls-ca-cert-file /fixture-tls/ca.crt --tls-cert-file /fixture-tls/server.crt --tls-key-file /fixture-tls/server.key`}
		}
		app.Services[name] = service
	}
}

func applicationTLSProfileCertificate(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Disposable application TLS profile CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "application TLS fixture"}, DNSNames: []string{"db", "postgres", "redis", "valkey"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func applicationTLSProfileDeploy(t *testing.T, ctx context.Context, c *Client, app spec.Application, applicationID string, secrets map[string]string, connections map[string]map[string]DatabaseConnection, verify func(string)) {
	t.Helper()
	resolved, err := c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Project: "demo", Environment: "development", ApplicationID: applicationID, OperationID: "application-tls-profile", Revision: 1, Spec: resolved}
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "application-tls-profile"
	namespace, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		for name := range secrets {
			if err := c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, name); err != nil {
				t.Error(err)
			}
		}
		current, err := c.kube.CoreV1().Namespaces().Get(clean, namespace.Name, metav1.GetOptions{})
		if err != nil || current.UID != namespace.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "application-tls-profile" {
			t.Errorf("could not verify application TLS fixture ownership; retaining namespace: %v", err)
			return
		}
		if err := c.kube.CoreV1().Namespaces().Delete(clean, current.Name, deleteOptions(current)); err != nil {
			t.Error(err)
			return
		}
		for clean.Err() == nil {
			remaining, err := c.kube.CoreV1().Namespaces().Get(clean, current.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return
			}
			if err != nil || remaining.UID != current.UID {
				t.Errorf("could not confirm application TLS fixture cleanup: %v", err)
				return
			}
			_ = sleepContext(clean, time.Second)
		}
		t.Error("application TLS fixture cleanup timed out")
	}()
	for name, value := range secrets {
		if err := c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, name, value); err != nil {
			t.Fatal(err)
		}
	}
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, application spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != target.Project || environment != target.Environment || application.Name != app.Name {
			return nil, fmt.Errorf("application TLS profile binding scope changed")
		}
		return connections, nil
	})
	if _, err := c.Deploy(ctx, target, func(event Event) { t.Log(event.Type, event.Message) }); err != nil {
		logs, _ := exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", namespace.Name, "logs", "deployment/main", "--tail=40").CombinedOutput()
		t.Fatalf("application TLS deployment failed: %v; sanitized log tail: %s", err, sanitizeApplicationTLSProfileLog(string(logs)))
	}
	verify(namespace.Name)
	t.Log("pinned application reached health after migrations with automatically injected verified database TLS settings")
}

func applicationTLSProfileExec(t *testing.T, ctx context.Context, namespace string, command ...string) {
	t.Helper()
	args := []string{"--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", namespace, "exec", "deployment/main", "--"}
	output, err := exec.CommandContext(ctx, "kubectl", append(args, command...)...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "healthy" {
		t.Fatalf("application health verification failed: %v; sanitized output: %s", err, sanitizeApplicationTLSProfileLog(string(output)))
	}
}

func sanitizeApplicationTLSProfileLog(value string) string {
	for _, marker := range []string{"InfisicalFixturePassword123", "InfisicalRedisFixturePassword123", "0123456789abcdef0123456789abcdef", "glitchtip-fixture-secret-key-0123456789"} {
		value = strings.ReplaceAll(value, marker, "[redacted]")
	}
	if len(value) > 8192 {
		value = value[len(value)-8192:]
	}
	return value
}
