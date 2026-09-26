package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// containerDaemonTrustFiles writes a certificate authority and a client
// certificate signed by it, as an operator would place them on the host.
func containerDaemonTrustFiles(t *testing.T, clientAuth bool) (caPath, certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "daemon-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	usage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if !clientAuth {
		usage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "hakopod-client"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath, certPath, keyPath = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{
		caPath:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		keyPath:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyDER}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return caPath, certPath, keyPath
}

func containerDaemonBindingFixture(t *testing.T, target Target) ContainerDaemonBinding {
	t.Helper()
	ca, cert, key := containerDaemonTrustFiles(t, true)
	return ContainerDaemonBinding{Name: "build-daemon", Project: target.Project, Environment: target.Environment, Application: target.Spec.Name, Service: "api", Endpoint: "tcps://198.51.100.20:2376", CAFile: ca, ClientCertFile: cert, ClientKeyFile: key}
}

func withContainerDaemonRef(target Target, ref string) Target {
	svc := target.Spec.Services["api"]
	svc.ContainerDaemon = ref
	target.Spec.Services["api"] = svc
	return target
}

// The endpoint validator is the boundary that keeps this feature from becoming
// a privilege escalation, so every refusal is asserted with its own message.
func TestContainerDaemonEndpointRefusals(t *testing.T) {
	const apiHost = "https://203.0.113.9:6443"
	if err := ValidateContainerDaemonEndpoint("tcps://198.51.100.20:2376", apiHost); err != nil {
		t.Fatal("valid network endpoint refused", err)
	}
	for _, testCase := range []struct{ endpoint, expect string }{
		{"unix:///var/run/docker.sock", "socket or filesystem path"},
		{"/var/run/docker.sock", "socket or filesystem path"},
		{"npipe:////./pipe/docker_engine", "socket or filesystem path"},
		{"fd://3", "socket or filesystem path"},
		{"tcp://198.51.100.20:2376", "tcps:// scheme"},
		{"http://198.51.100.20:2375", "tcps:// scheme"},
		{"tcps://127.0.0.1:2376", "loopback or localhost"},
		{"tcps://localhost:2376", "loopback or localhost"},
		{"tcps://169.254.169.254:2376", "link-local or the instance metadata address"},
		{"tcps://169.254.0.1:2376", "link-local or the instance metadata address"},
		{"tcps://10.42.1.9:2376", "cluster pod or Service networks"},
		{"tcps://10.43.0.1:2376", "cluster pod or Service networks"},
		{"tcps://203.0.113.9:2376", "Kubernetes API server address"},
		{"tcps://198.51.100.20", "explicit TCP port"},
		{"tcps://198.51.100.20:0", "explicit TCP port"},
		{"tcps://0.0.0.0:2376", "single routable unicast address"},
		{"tcps://daemon.example.com:2376", "IPv4 address literal"},
		{"tcps://198.51.100.20:2376/build", "without credentials, path or query"},
		{"", "at most 128 characters"},
	} {
		err := ValidateContainerDaemonEndpoint(testCase.endpoint, apiHost)
		if err == nil {
			t.Fatalf("endpoint accepted: %q", testCase.endpoint)
		}
		if !strings.Contains(err.Error(), testCase.expect) {
			t.Fatalf("endpoint %q refused with %q, expected %q", testCase.endpoint, err.Error(), testCase.expect)
		}
		t.Logf("refused %-42q %s", testCase.endpoint, err)
	}
}

func TestContainerDaemonScopeAndRevocation(t *testing.T) {
	target := withContainerDaemonRef(testTarget(t), "build-daemon")
	binding := containerDaemonBindingFixture(t, target)
	c := &Client{options: Options{ContainerDaemonBindings: []ContainerDaemonBinding{binding}}}
	if err := c.ValidateContainerDaemons(target.Project, target.Environment, target.Spec); err != nil {
		t.Fatal("approved grant refused", err)
	}
	for _, values := range [][]string{
		{"other", target.Environment, target.Spec.Name, "api", binding.Name},
		{target.Project, "production", target.Spec.Name, "api", binding.Name},
		{target.Project, target.Environment, "other", "api", binding.Name},
		{target.Project, target.Environment, target.Spec.Name, "web", binding.Name},
		{target.Project, target.Environment, target.Spec.Name, "api", "other"},
	} {
		if _, err := c.resolveContainerDaemon(values[0], values[1], values[2], values[3], values[4]); err == nil {
			t.Fatalf("unapproved grant accepted: %v", values)
		}
	}
	// An ungranted service is refused, and so is a revoked grant.
	c.options.ContainerDaemonBindings = nil
	if err := c.ValidateContainerDaemons(target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("unconfigured grant accepted")
	}
	c.options.ContainerDaemonBindings = []ContainerDaemonBinding{binding}
	target.Spec.Networks["default"] = spec.Network{Internal: true}
	if err := c.ValidateContainerDaemons(target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("service without egress-capable network accepted")
	}
}

func TestContainerDaemonProjectionEnvironmentAndEgress(t *testing.T) {
	ctx := context.Background()
	target := withContainerDaemonRef(testTarget(t), "build-daemon")
	binding := containerDaemonBindingFixture(t, target)
	kube := fake.NewClientset()
	c := &Client{kube: kube, options: Options{ContainerDaemonBindings: []ContainerDaemonBinding{binding}}, execConfig: &rest.Config{Host: "https://203.0.113.9:6443"}}
	svc := target.Spec.Services["api"]
	wanted := deployment(target, "api", svc, 0)
	if err := c.prepareContainerDaemon(ctx, target, "api", svc, wanted); err != nil {
		t.Fatal(err)
	}
	pod := wanted.Spec.Template.Spec
	if pod.ServiceAccountName != "" || len(pod.Volumes) != 1 || pod.Volumes[0].Secret == nil {
		t.Fatal("expected exactly one projected trust Secret and no service account token")
	}
	source := pod.Volumes[0].Secret
	if *source.DefaultMode != 0440 || len(source.Items) != 3 {
		t.Fatal("trust material is not projected read-only as three fixed files")
	}
	mount := pod.Containers[0].VolumeMounts[0]
	if !mount.ReadOnly || mount.SubPath != "" || mount.MountPath != spec.ContainerDaemonDirectory {
		t.Fatal("trust directory is writable or relocated")
	}
	env := map[string]string{}
	for _, value := range pod.Containers[0].Env {
		env[value.Name] = value.Value
	}
	if env["DOCKER_HOST"] != "tcp://198.51.100.20:2376" || env["DOCKER_TLS_VERIFY"] != "1" || env["DOCKER_CERT_PATH"] != spec.ContainerDaemonDirectory {
		t.Fatalf("daemon client is not pinned to the granted endpoint with mutual TLS: %v", env)
	}
	secret, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, source.SecretName, metav1.GetOptions{})
	if err != nil || secret.Type != corev1.SecretTypeTLS || secret.Immutable == nil || !*secret.Immutable || secret.Labels[containerDaemonLabel] != binding.Name {
		t.Fatal("trust Secret is not an owned immutable TLS Secret", err)
	}
	// Repeating the preparation reuses the content-addressed Secret.
	if err := c.prepareContainerDaemon(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err != nil {
		t.Fatal("repeat preparation failed", err)
	}
	target.containerDaemon, err = c.resolveContainerDaemons(target)
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	for _, policy := range policies(target) {
		if policy.Labels[serviceKey] != "api" {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil && peer.IPBlock.CIDR == "198.51.100.20/32" {
					if len(rule.To) != 1 || len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 2376 {
						t.Fatal("daemon egress is broader than one host and one port")
					}
					opened++
				}
			}
		}
	}
	if opened != 1 {
		t.Fatalf("expected exactly one daemon egress rule, found %d", opened)
	}
	// A service that was never granted the daemon keeps its policy unchanged.
	for _, policy := range policies(target) {
		if policy.Labels[serviceKey] != "web" {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil && peer.IPBlock.CIDR == "198.51.100.20/32" {
					t.Fatal("ungranted service received daemon egress")
				}
			}
		}
	}
}

func TestContainerDaemonRefusesRevokedGrantAtPrepare(t *testing.T) {
	ctx := context.Background()
	target := withContainerDaemonRef(testTarget(t), "build-daemon")
	binding := containerDaemonBindingFixture(t, target)
	c := &Client{kube: fake.NewClientset(), options: Options{ContainerDaemonBindings: []ContainerDaemonBinding{binding}}}
	svc := target.Spec.Services["api"]
	if err := c.ValidateContainerDaemons(target.Project, target.Environment, target.Spec); err != nil {
		t.Fatal("plan time refused an approved grant", err)
	}
	// The operator revokes the grant after the revision was accepted.
	c.options.ContainerDaemonBindings = nil
	if err := c.prepareContainerDaemon(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
		t.Fatal("revoked grant injected at reconcile time")
	} else if !strings.Contains(err.Error(), "installation administrator") {
		t.Fatal("refusal does not tell the user who to ask", err)
	}
	if _, err := c.resolveContainerDaemons(target); err == nil {
		t.Fatal("revoked grant produced an egress rule")
	}
	// A re-scoped grant is refused just as a removed one is.
	rescoped := binding
	rescoped.Service = "worker"
	c.options.ContainerDaemonBindings = []ContainerDaemonBinding{rescoped}
	if err := c.prepareContainerDaemon(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
		t.Fatal("re-scoped grant injected")
	}
}

func TestContainerDaemonRefusesSecretDrift(t *testing.T) {
	ctx := context.Background()
	for _, drift := range []string{"owner", "type", "mutable", "content"} {
		target := withContainerDaemonRef(testTarget(t), "build-daemon")
		binding := containerDaemonBindingFixture(t, target)
		ca, certificate, key, err := containerDaemonTrust(binding)
		if err != nil {
			t.Fatal(err)
		}
		labels := labelsFor(target, "api")
		labels[containerDaemonLabel] = binding.Name
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: containerDaemonSecretName("api", ca, certificate), Namespace: Namespace(target.ApplicationID), Labels: labels},
			Immutable:  ptr(true), Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: key, corev1.ServiceAccountRootCAKey: ca},
		}
		switch drift {
		case "owner":
			secret.Labels[ownerKey] = "other"
		case "type":
			secret.Type = corev1.SecretTypeOpaque
		case "mutable":
			secret.Immutable = ptr(false)
		case "content":
			secret.Data[corev1.TLSCertKey] = []byte("-----BEGIN CERTIFICATE-----\nreplaced\n-----END CERTIFICATE-----\n")
		}
		c := &Client{kube: fake.NewClientset(secret), options: Options{ContainerDaemonBindings: []ContainerDaemonBinding{binding}}}
		svc := target.Spec.Services["api"]
		if err := c.prepareContainerDaemon(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
			t.Fatalf("drifted trust Secret accepted: %s", drift)
		}
	}
}

func TestContainerDaemonTrustMaterialValidation(t *testing.T) {
	target := testTarget(t)
	binding := containerDaemonBindingFixture(t, target)
	if _, _, _, err := containerDaemonTrust(binding); err != nil {
		t.Fatal("valid mutual TLS material refused", err)
	}
	// A certificate without client authentication cannot prove this service.
	serverCA, serverCert, serverKey := containerDaemonTrustFiles(t, false)
	wrongUsage := binding
	wrongUsage.CAFile, wrongUsage.ClientCertFile, wrongUsage.ClientKeyFile = serverCA, serverCert, serverKey
	if _, _, _, err := containerDaemonTrust(wrongUsage); err == nil || !strings.Contains(err.Error(), "client authentication") {
		t.Fatal("server-only certificate accepted", err)
	}
	// A certificate signed by a different authority cannot be trusted.
	otherCA, _, _ := containerDaemonTrustFiles(t, true)
	foreign := binding
	foreign.CAFile = otherCA
	if _, _, _, err := containerDaemonTrust(foreign); err == nil {
		t.Fatal("certificate from another authority accepted")
	}
	// The key must belong to the certificate.
	_, _, otherKey := containerDaemonTrustFiles(t, true)
	mismatched := binding
	mismatched.ClientKeyFile = otherKey
	if _, _, _, err := containerDaemonTrust(mismatched); err == nil || !strings.Contains(err.Error(), "matching PEM key pair") {
		t.Fatal("mismatched key pair accepted", err)
	}
	if err := os.Chmod(binding.ClientKeyFile, 0666); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := containerDaemonTrust(binding); err == nil {
		t.Fatal("group-writable client key accepted")
	}
}

func TestContainerDaemonBindingsFileValidation(t *testing.T) {
	ca, cert, key := containerDaemonTrustFiles(t, true)
	dir := t.TempDir()
	path := filepath.Join(dir, "daemons.toml")
	valid := `schema_version = 1
[[bindings]]
name = "build-daemon"
project = "builds"
environment = "production"
application = "runner"
service = "api"
endpoint = "tcps://198.51.100.20:2376"
ca_file = "` + ca + `"
client_cert_file = "` + cert + `"
client_key_file = "` + key + `"
`
	for _, data := range []string{
		valid,
		strings.Replace(valid, `project = "builds"`, `project = "*"`, 1),
		strings.Replace(valid, "tcps://198.51.100.20:2376", "unix:///var/run/docker.sock", 1),
		strings.Replace(valid, "tcps://198.51.100.20:2376", "tcps://10.43.0.1:2376", 1),
		strings.Replace(valid, `ca_file = "`+ca+`"`, `ca_file = "relative/ca.pem"`, 1),
		valid + "unsupported = true\n",
		valid + strings.TrimPrefix(valid, "schema_version = 1\n"),
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		bindings, err := ReadContainerDaemonBindingsFile(path)
		if data == valid && (err != nil || len(bindings) != 1) {
			t.Fatal("valid exact binding rejected", err)
		}
		if data != valid && err == nil {
			t.Fatal("invalid or duplicate binding accepted")
		}
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContainerDaemonBindingsFile(path); err == nil {
		t.Fatal("group-writable authorization file accepted")
	}
}

func TestContainerDaemonInstallationGates(t *testing.T) {
	target := withContainerDaemonRef(testTarget(t), "build-daemon")
	binding := containerDaemonBindingFixture(t, target)
	bindings := []ContainerDaemonBinding{binding}
	// Startup: managed cloud without a dedicated BYO node, and a shared
	// workload runtime, both refuse the bindings outright.
	if err := validateContainerDaemonInstallation(DeploymentManagedCloud, "", false, bindings); err == nil {
		t.Fatal("managed cloud accepted container daemon bindings")
	}
	if err := validateContainerDaemonInstallation(DeploymentSelfHosted, "", true, bindings); err == nil {
		t.Fatal("shared workload runtime accepted container daemon bindings")
	}
	if err := validateContainerDaemonInstallation(DeploymentManagedCloud, "byo-node", false, bindings); err != nil {
		t.Fatal("dedicated BYO node refused", err)
	}
	if err := validateContainerDaemonInstallation(DeploymentSelfHosted, "", false, bindings); err != nil {
		t.Fatal("self-hosted installation refused", err)
	}
	// An empty deployment mode is self-hosted, so a missing variable is the
	// permissive case and the workload policy check must stand on its own.
	if err := validateContainerDaemonInstallation("", "", true, bindings); err == nil {
		t.Fatal("empty deployment mode bypassed the shared runtime refusal")
	}
	// ValidateCloudSpec refuses the field independently of the bindings.
	c := &Client{options: Options{DeploymentMode: DeploymentManagedCloud}}
	if err := c.ValidateCloudSpec(target.Spec); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("Cloud spec accepted container_daemon", err)
	}
	if err := c.containerDaemonPolicy(); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("Cloud runtime allowed a container daemon", err)
	}
	c.options.DedicatedPublicTCPNode = "byo-node"
	if err := c.ValidateCloudSpec(target.Spec); err != nil {
		t.Fatal("dedicated BYO node refused the field", err)
	}
	if err := c.containerDaemonPolicy(); err != nil {
		t.Fatal("dedicated BYO node refused the capability", err)
	}
	// The reported capability follows the dedicated-node cluster shape.
	kube := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "byo-node"}})
	c.kube = kube
	value, err := c.CloudCapabilities(context.Background())
	if err != nil || !value.ContainerDaemon {
		t.Fatal("dedicated BYO node did not report the capability", err, value)
	}
	c.options.DedicatedPublicTCPNode = ""
	value, err = c.CloudCapabilities(context.Background())
	if err != nil || value.ContainerDaemon {
		t.Fatal("shared Cloud runtime reported the capability", err, value)
	}
}

func TestContainerDaemonJobAndScheduledJob(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"job", "scheduled"} {
		target := testTarget(t)
		svc := target.Spec.Services["worker"]
		svc.ContainerDaemon = "build-daemon"
		svc.Job = &spec.Job{Retries: 0, TimeoutSeconds: 60}
		if kind == "scheduled" {
			svc.Job.Schedule = &spec.JobSchedule{Cron: "0 3 * * *", Timezone: "UTC", HistoryLimit: 1}
		}
		target.Spec.Services["worker"] = svc
		binding := containerDaemonBindingFixture(t, target)
		binding.Service = "worker"
		c := &Client{kube: fake.NewClientset(), options: Options{ContainerDaemonBindings: []ContainerDaemonBinding{binding}}}
		if kind == "scheduled" {
			if err := c.applyScheduledJob(ctx, target, "worker", svc); err != nil {
				t.Fatal(err)
			}
			cron, err := c.kube.BatchV1().CronJobs(Namespace(target.ApplicationID)).Get(ctx, scheduledJobName("worker"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			assertContainerDaemonPod(t, cron.Spec.JobTemplate.Spec.Template.Spec)
			continue
		}
		// The job path is asserted through the same preparation the runner uses.
		d := deployment(target, "worker", svc, 0)
		if err := c.prepareContainerDaemon(ctx, target, "worker", svc, d); err != nil {
			t.Fatal(err)
		}
		assertContainerDaemonPod(t, d.Spec.Template.Spec)
		// A job whose grant is missing is refused before any Job is created.
		c.options.ContainerDaemonBindings = nil
		if err := c.prepareContainerDaemon(ctx, target, "worker", svc, deployment(target, "worker", svc, 0)); err == nil {
			t.Fatal("job accepted an unapproved container daemon")
		}
	}
}

func assertContainerDaemonPod(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	mounted := false
	for _, mount := range pod.Containers[0].VolumeMounts {
		mounted = mounted || mount.MountPath == spec.ContainerDaemonDirectory && mount.ReadOnly
	}
	host := ""
	for _, value := range pod.Containers[0].Env {
		if value.Name == "DOCKER_HOST" {
			host = value.Value
		}
	}
	if !mounted || host != "tcp://198.51.100.20:2376" {
		t.Fatal("workload did not receive the daemon trust directory and endpoint")
	}
	if pod.ServiceAccountName != "" {
		t.Fatal("workload received a service account")
	}
	for _, volume := range pod.Volumes {
		if volume.HostPath != nil {
			t.Fatal("workload received a host path")
		}
	}
}
