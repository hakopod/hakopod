package cluster

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This changes only the final disposable external-dependency fixtures, after
// the ordinary persistence checks. The backend probes use the actual candidate
// image and secret snapshots. Public-provider CAs and networking are not tested.
func xemTLSAcceptance(t *testing.T, ctx context.Context, c *Client, namespace string) {
	t.Helper()
	backend, err := c.kube.AppsV1().Deployments(namespace).Get(ctx, "backend", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	base := backend.Spec.Template.DeepCopy()
	for _, name := range []string{"backend", "frontend", "main"} {
		d, e := c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		d.Spec.Replicas = ptr(int32(0))
		if _, e = c.kube.AppsV1().Deployments(namespace).Update(ctx, d, metav1.UpdateOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	wait, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	for wait.Err() == nil {
		pods, e := c.kube.CoreV1().Pods(namespace).List(wait, metav1.ListOptions{Limit: 30})
		if e != nil {
			t.Fatal(e)
		}
		remaining := false
		for _, p := range pods.Items {
			for _, name := range []string{"backend-", "frontend-", "main-"} {
				if strings.HasPrefix(p.Name, name) {
					remaining = true
				}
			}
		}
		if !remaining {
			break
		}
		_ = sleepContext(wait, time.Second)
	}
	if wait.Err() != nil {
		t.Fatal("application did not stop before isolated TLS probes")
	}
	ca, cert, key := xemTLSCertificate(t)
	wrong, _, _ := xemTLSCertificate(t)
	_, err = c.kube.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fixture-server-tls"}, Data: map[string][]byte{"tls.crt": cert, "tls.key": key}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"fixture-trust": ca, "fixture-untrusted": wrong} {
		_, err = c.kube.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"ca.pem": string(data)}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	// verify-full must refuse the still-plaintext PostgreSQL fixture.
	xemTLSProbe(t, ctx, c, namespace, base, "plaintext-postgres", "fixture-trust", map[string]string{"POSTGRES_SSLMODE": "verify-full", "REDIS_USE_TLS": "false"}, "postgres-refused")
	for _, name := range []string{"fixture-db", "fixture-redis"} {
		d, e := c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		d.Spec.Template.Spec.Volumes = append(d.Spec.Template.Spec.Volumes, corev1.Volume{Name: "fixture-server-tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "fixture-server-tls", DefaultMode: ptr(int32(0640))}}})
		container := &d.Spec.Template.Spec.Containers[0]
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "fixture-server-tls", MountPath: "/fixture-tls", ReadOnly: true})
		if name == "fixture-db" {
			container.Args = append(container.Args, "-c", "ssl=on", "-c", "ssl_cert_file=/fixture-tls/tls.crt", "-c", "ssl_key_file=/fixture-tls/tls.key")
		} else {
			container.Args = []string{`exec redis-server --appendonly yes --maxmemory 64mb --maxmemory-policy noeviction --requirepass "$REDIS_PASSWORD" --port 0 --tls-port 6379 --tls-auth-clients no --tls-ca-cert-file /fixture-tls/tls.crt --tls-cert-file /fixture-tls/tls.crt --tls-key-file /fixture-tls/tls.key`}
		}
		if _, e = c.kube.AppsV1().Deployments(namespace).Update(ctx, d, metav1.UpdateOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	ready, done := context.WithTimeout(ctx, 2*time.Minute)
	defer done()
	for _, name := range []string{"fixture-db", "fixture-redis"} {
		for ready.Err() == nil {
			d, e := c.kube.AppsV1().Deployments(namespace).Get(ready, name, metav1.GetOptions{})
			if e == nil && d.Status.ObservedGeneration >= d.Generation && d.Status.ReadyReplicas == 1 && d.Status.UpdatedReplicas == 1 && d.Status.Replicas == 1 {
				break
			}
			_ = sleepContext(ready, time.Second)
		}
		if ready.Err() != nil {
			t.Fatal("disposable TLS dependency did not become ready", name)
		}
	}
	xemTLSProbe(t, ctx, c, namespace, base, "trusted", "fixture-trust", map[string]string{"POSTGRES_SSLMODE": "verify-full", "REDIS_USE_TLS": "true"}, "success")
	xemTLSProbe(t, ctx, c, namespace, base, "untrusted-postgres", "fixture-untrusted", map[string]string{"POSTGRES_SSLMODE": "verify-full", "REDIS_USE_TLS": "true"}, "postgres-refused")
	xemTLSProbe(t, ctx, c, namespace, base, "hostname-postgres", "fixture-trust", map[string]string{"POSTGRES_SSLMODE": "verify-full", "POSTGRES_HOST": "fixture-db." + namespace + ".svc", "REDIS_USE_TLS": "true"}, "postgres-refused")
	// PostgreSQL remains encrypted in require mode while the Redis client is
	// tested independently against the wrong CA or hostname. No credentials are
	// sent through a connection whose certificate validation has failed.
	xemTLSProbe(t, ctx, c, namespace, base, "untrusted-redis", "fixture-untrusted", map[string]string{"POSTGRES_SSLMODE": "require", "PGSSLROOTCERT": "", "REDIS_USE_TLS": "true"}, "redis-refused")
	xemTLSProbe(t, ctx, c, namespace, base, "hostname-redis", "fixture-trust", map[string]string{"POSTGRES_SSLMODE": "verify-full", "REDIS_USE_TLS": "true", "REDIS_HOST": "fixture-redis." + namespace + ".svc"}, "redis-refused")
	t.Log("actual backend verified trusted PostgreSQL and Redis TLS, refused plaintext PostgreSQL with verify-full, and refused untrusted certificates and hostname mismatches independently")
}

func xemTLSProbe(t *testing.T, ctx context.Context, c *Client, namespace string, base *corev1.PodTemplateSpec, name, trust string, overrides map[string]string, want string) {
	t.Helper()
	template := base.DeepCopy()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tls-" + name, Namespace: namespace, Labels: template.Labels}, Spec: template.Spec}
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	pod.Spec.TerminationGracePeriodSeconds = ptr(int64(3))
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "fixture-trust", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: trust}}}})
	container := &pod.Spec.Containers[0]
	container.ReadinessProbe = nil
	container.LivenessProbe = nil
	container.StartupProbe = nil
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "fixture-trust", MountPath: "/fixture-trust", ReadOnly: true})
	env := map[string]string{"PGSSLROOTCERT": "/fixture-trust/ca.pem", "SSL_CERT_FILE": "/fixture-trust/ca.pem"}
	for key, value := range overrides {
		env[key] = value
	}
	for key, value := range env {
		found := false
		for i, item := range container.Env {
			if item.Name == key {
				container.Env[i] = corev1.EnvVar{Name: key, Value: value}
				found = true
			}
		}
		if !found {
			container.Env = append(container.Env, corev1.EnvVar{Name: key, Value: value})
		}
	}
	created, err := c.kube.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := c.kube.CoreV1().Pods(namespace).Delete(clean, created.Name, deleteOptions(created)); err != nil {
			t.Error(err)
		}
		for clean.Err() == nil {
			p, e := c.kube.CoreV1().Pods(namespace).Get(clean, created.Name, metav1.GetOptions{})
			if e != nil {
				break
			}
			if p.UID != created.UID {
				t.Error("probe pod ownership changed")
				break
			}
			_ = sleepContext(clean, time.Second)
		}
	}()
	wait, done := context.WithTimeout(ctx, 100*time.Second)
	defer done()
	for wait.Err() == nil {
		data, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{LimitBytes: ptr(int64(128 << 10))}).DoRaw(wait)
		if err == nil {
			logs := strings.ToLower(string(data))
			if want == "success" && strings.Contains(logs, "successfully configured rate limiting with redis") && strings.Contains(logs, "api server started") {
				current, e := c.kube.CoreV1().Pods(namespace).Get(wait, pod.Name, metav1.GetOptions{})
				if e != nil || net.ParseIP(current.Status.PodIP) == nil {
					t.Fatal("TLS probe pod address unavailable")
				}
				sql := "select count(*) filter(where ssl), count(*) from pg_stat_ssl join pg_stat_activity using(pid) where client_addr='" + current.Status.PodIP + "';"
				cmd := exec.CommandContext(wait, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", namespace, "exec", "deployment/fixture-db", "--", "sh", "-ec", `psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "$1"`, "probe", sql)
				out, e := cmd.Output()
				counts := strings.Split(strings.TrimSpace(string(out)), "|")
				if e != nil || len(counts) != 2 {
					t.Fatal("could not observe PostgreSQL TLS sessions")
				}
				encrypted, e1 := strconv.Atoi(counts[0])
				total, e2 := strconv.Atoi(counts[1])
				if e1 != nil || e2 != nil || encrypted < 1 || encrypted != total {
					t.Fatal("backend PostgreSQL sessions are not all encrypted")
				}
				t.Logf("pg_stat_ssl confirms %d encrypted backend connections", encrypted)
				t.Log("backend TLS probe", name, "passed")
				return
			}
			if want == "postgres-refused" && strings.Contains(logs, "failed to connect to database") && (strings.Contains(logs, "tls") || strings.Contains(logs, "certificate") || strings.Contains(logs, "ssl")) {
				t.Log("backend TLS probe", name, "passed")
				return
			}
			if want == "redis-refused" && strings.Contains(logs, "failed to initialize redis/valkey client") && (strings.Contains(logs, "certificate") || strings.Contains(logs, "tls")) {
				if strings.Contains(logs, "successfully configured rate limiting with redis") {
					t.Fatal("Redis accepted a rejected identity")
				}
				t.Log("backend TLS probe", name, "passed")
				return
			}
		}
		_ = sleepContext(wait, time.Second)
	}
	t.Fatal("backend TLS probe did not produce the required connection result", name, want)
}

func xemTLSCertificate(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Disposable Xem acceptance CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fixture-db"}, DNSNames: []string{"fixture-db", "fixture-redis"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
