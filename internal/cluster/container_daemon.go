package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const containerDaemonLabel = "hakopod.io/container-daemon"
const containerDaemonVolume = "hakopod-container-daemon"
const containerDaemonSecretPrefix = "hp-daemon-"

// Files the daemon client reads from DOCKER_CERT_PATH. The names are fixed by
// the Docker client, not by the operator.
const (
	containerDaemonCAFile   = "ca.pem"
	containerDaemonCertFile = "cert.pem"
	containerDaemonKeyFile  = "key.pem"
)

// ContainerDaemonBinding is installation-owned authorization. A container
// daemon grants effective root on the host that runs it, so a binding names one
// exact project, environment, application and service and one network endpoint
// reached with mutual TLS. No host socket, host path or cluster credential is
// ever involved.
type ContainerDaemonBinding struct {
	Name           string `toml:"name" json:"name"`
	Project        string `toml:"project" json:"project"`
	Environment    string `toml:"environment" json:"environment"`
	Application    string `toml:"application" json:"application"`
	Service        string `toml:"service" json:"service"`
	Endpoint       string `toml:"endpoint" json:"endpoint"`
	CAFile         string `toml:"ca_file" json:"ca_file"`
	ClientCertFile string `toml:"client_cert_file" json:"client_cert_file"`
	ClientKeyFile  string `toml:"client_key_file" json:"client_key_file"`
}

// The cluster networks the supported installer owns. A daemon endpoint inside
// them would turn this feature into the privilege escalation the threat model
// forbids, so they are refused exactly as private egress destinations are.
var containerDaemonReservedNetworks = []string{"10.42.0.0/16", "10.43.0.0/16"}

// ValidateContainerDaemonEndpoint is the highest-value check in this feature.
// It runs on every resolve, never once at acceptance. apiHost is the Kubernetes
// API server address, empty when the caller cannot determine it.
func ValidateContainerDaemonEndpoint(endpoint, apiHost string) error {
	if endpoint == "" || len(endpoint) > 128 {
		return fmt.Errorf("endpoint must be a tcps://address:port of at most 128 characters")
	}
	lower := strings.ToLower(endpoint)
	// A socket is a host path, and a host path is exactly what the platform
	// refuses to mount. Reject every local transport before parsing a URL.
	for _, local := range []string{"unix://", "npipe://", "fd://", "/", "./", "file://"} {
		if strings.HasPrefix(lower, local) {
			return fmt.Errorf("endpoint must not be a socket or filesystem path; the platform never mounts a host socket")
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint must be a tcps://address:port URL")
	}
	if parsed.Scheme != "tcps" {
		return fmt.Errorf("endpoint must use the tcps:// scheme; a daemon reached without TLS cannot be authenticated")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("endpoint must be a bare tcps://address:port without credentials, path or query")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || port == "" {
		return fmt.Errorf("endpoint must name an explicit TCP port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("endpoint must name an explicit TCP port between 1 and 65535")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("endpoint must not be loopback or localhost; a daemon on this node is not a granted destination")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.Is4() {
		// The synthesized egress rule names one host, and a NetworkPolicy peer
		// cannot be a name. An IPv4 literal is the only expressible form.
		return fmt.Errorf("endpoint must use an IPv4 address literal so egress can be opened to exactly one host")
	}
	if address.IsLoopback() {
		return fmt.Errorf("endpoint must not be loopback or localhost; a daemon on this node is not a granted destination")
	}
	if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || netip.MustParsePrefix("169.254.0.0/16").Contains(address) {
		return fmt.Errorf("endpoint must not be link-local or the instance metadata address")
	}
	for _, reserved := range containerDaemonReservedNetworks {
		if netip.MustParsePrefix(reserved).Contains(address) {
			return fmt.Errorf("endpoint must not be inside the cluster pod or Service networks")
		}
	}
	if address.IsUnspecified() || address.IsMulticast() {
		return fmt.Errorf("endpoint must be a single routable unicast address")
	}
	if apiHost != "" && containerDaemonMatchesAPIServer(address, apiHost) {
		return fmt.Errorf("endpoint must not be the Kubernetes API server address")
	}
	return nil
}

// The API server address is compared by host only. A daemon answering on
// another port of that address is still the control plane.
func containerDaemonMatchesAPIServer(address netip.Addr, apiHost string) bool {
	if !strings.Contains(apiHost, "//") {
		apiHost = "https://" + apiHost
	}
	parsed, err := url.Parse(apiHost)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if candidate, err := netip.ParseAddr(host); err == nil {
		return candidate == address
	}
	return false
}

// kubeAPIHost reports the configured Kubernetes API server address. It is read
// from the client's own configuration, never from application input.
func (c *Client) kubeAPIHost() string {
	if c.execConfig == nil {
		return ""
	}
	return c.execConfig.Host
}

func ValidateContainerDaemonBindings(bindings []ContainerDaemonBinding) error {
	if len(bindings) > 64 {
		return fmt.Errorf("container daemons: at most 64 bindings are supported")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !awsBindingName.MatchString(b.Name) || seen[b.Name] {
			return fmt.Errorf("container daemons: binding names must be unique lowercase names of at most 63 characters")
		}
		seen[b.Name] = true
		for _, scope := range []string{b.Project, b.Environment, b.Application, b.Service} {
			if !awsScopeName.MatchString(scope) {
				return fmt.Errorf("container daemon %s: project, environment, application and service require exact names", b.Name)
			}
		}
		if err := ValidateContainerDaemonEndpoint(b.Endpoint, ""); err != nil {
			return fmt.Errorf("container daemon %s: %w", b.Name, err)
		}
		for _, path := range []string{b.CAFile, b.ClientCertFile, b.ClientKeyFile} {
			if path == "" || len(path) > 1024 || !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
				return fmt.Errorf("container daemon %s: ca_file, client_cert_file and client_key_file require absolute paths", b.Name)
			}
		}
	}
	return nil
}

func ReadContainerDaemonBindingsFile(path string) ([]ContainerDaemonBinding, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read container daemon bindings file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("container daemon bindings must be a regular file of at most 64 KiB, not writable by group or others")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("cannot read bounded container daemon bindings file")
	}
	var config struct {
		SchemaVersion int                      `toml:"schema_version"`
		Bindings      []ContainerDaemonBinding `toml:"bindings"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("container daemons require valid strict TOML with schema_version = 1")
	}
	if err := ValidateContainerDaemonBindings(config.Bindings); err != nil {
		return nil, err
	}
	return config.Bindings, nil
}

// validateContainerDaemonInstallation gates the capability at startup. An empty
// deployment mode is self-hosted, so both the mode and the workload policy are
// checked; neither alone is the permissive case.
func validateContainerDaemonInstallation(mode, dedicatedNode string, sharedRuntime bool, bindings []ContainerDaemonBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	if mode == DeploymentManagedCloud && dedicatedNode == "" {
		return fmt.Errorf("container daemon bindings require a self-hosted installation or a dedicated BYO node")
	}
	if sharedRuntime && dedicatedNode == "" {
		return fmt.Errorf("container daemon bindings cannot be configured on a shared workload runtime")
	}
	return nil
}

// containerDaemonPolicy reuses the public TCP cluster-shape proof instead of a
// second one: both capabilities need either a self-hosted installation or a
// managed-cloud installation whose cluster holds only the dedicated BYO node.
func (c *Client) containerDaemonPolicy() error {
	if policy := c.PublicTCPPolicy(); !policy.Allowed {
		return fmt.Errorf("%w: container_daemon requires a self-hosted installation or a dedicated BYO node", ErrCloudLimit)
	}
	// Refused outright on a shared workload runtime, not only when no dedicated
	// node is set. Such a runtime owns the network policy, so the egress rule
	// this capability needs is never written and the daemon would be
	// unreachable. Permitting a grant that cannot work is a dead configuration
	// nobody can debug; refusing says so at the point of decision.
	if c.options.WorkloadPolicy != nil {
		return fmt.Errorf("%w: container_daemon is unavailable on a shared workload runtime", ErrCloudLimit)
	}
	return nil
}

// Resolve again on every path. A previously accepted revision is not authority
// to reach a daemon whose grant an operator has since revoked or re-scoped.
func (c *Client) resolveContainerDaemon(project, environment, application, service, ref string) (ContainerDaemonBinding, error) {
	if err := c.containerDaemonPolicy(); err != nil {
		return ContainerDaemonBinding{}, err
	}
	if err := ValidateContainerDaemonBindings(c.options.ContainerDaemonBindings); err != nil {
		return ContainerDaemonBinding{}, err
	}
	for _, binding := range c.options.ContainerDaemonBindings {
		if binding.Name != ref || binding.Project != project || binding.Environment != environment || binding.Application != application || binding.Service != service {
			continue
		}
		// The API server address is only known to the client, so it is checked
		// here rather than in the operator file reader.
		if err := ValidateContainerDaemonEndpoint(binding.Endpoint, c.kubeAPIHost()); err != nil {
			return ContainerDaemonBinding{}, fmt.Errorf("container daemon %s: %w", binding.Name, err)
		}
		return binding, nil
	}
	return ContainerDaemonBinding{}, fmt.Errorf("%s: container_daemon %s is not approved for this project, environment, application and service; ask the installation administrator", service, ref)
}

// ValidateContainerDaemons runs at plan time, at durable acceptance and again
// during reconciliation.
func (c *Client) ValidateContainerDaemons(project, environment string, app spec.Application) error {
	for _, name := range spec.Names(app) {
		svc := app.Services[name]
		if svc.ContainerDaemon == "" {
			continue
		}
		binding, err := c.resolveContainerDaemon(project, environment, app.Name, name, svc.ContainerDaemon)
		if err != nil {
			return err
		}
		if _, _, _, err := containerDaemonTrust(binding); err != nil {
			return fmt.Errorf("container daemon %s: %w", binding.Name, err)
		}
		external := false
		for _, network := range svc.Networks {
			external = external || !app.Networks[network].Internal
		}
		if !external {
			return fmt.Errorf("%s: container_daemon requires an egress-enabled network to reach the daemon", name)
		}
	}
	return nil
}

func (c *Client) resolveContainerDaemons(t Target) (map[string]ContainerDaemonBinding, error) {
	result := map[string]ContainerDaemonBinding{}
	for _, name := range spec.Names(t.Spec) {
		ref := t.Spec.Services[name].ContainerDaemon
		if ref == "" {
			continue
		}
		binding, err := c.resolveContainerDaemon(t.Project, t.Environment, t.Spec.Name, name, ref)
		if err != nil {
			return nil, err
		}
		result[name] = binding
	}
	return result, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the operator-referenced trust file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("trust material must be a regular file within its size bound, not writable by group or others")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("cannot read bounded trust material")
	}
	return data, nil
}

// containerDaemonTrust loads and proves the operator's mutual TLS material.
// Error messages describe the failure; no certificate or key body is included.
func containerDaemonTrust(b ContainerDaemonBinding) (ca, certificate, key []byte, err error) {
	if ca, err = readBoundedFile(b.CAFile, 256<<10); err != nil {
		return nil, nil, nil, err
	}
	if certificate, err = readBoundedFile(b.ClientCertFile, 256<<10); err != nil {
		return nil, nil, nil, err
	}
	if key, err = readBoundedFile(b.ClientKeyFile, 32<<10); err != nil {
		return nil, nil, nil, err
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, nil, fmt.Errorf("ca_file must contain a PEM certificate authority")
	}
	authority, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !authority.IsCA || authority.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, nil, nil, fmt.Errorf("ca_file must be a signing certificate authority")
	}
	roots.AddCert(authority)
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("client_cert_file and client_key_file must be a matching PEM key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return nil, nil, nil, fmt.Errorf("client certificate must be a leaf certificate allowing client authentication")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, nil, nil, fmt.Errorf("client certificate must be current and signed by ca_file")
	}
	return ca, certificate, key, nil
}

// The Secret is content addressed, so rotated material becomes a new immutable
// Secret instead of an in-place edit a running pod would not observe.
func containerDaemonSecretName(service string, ca, certificate []byte) string {
	hasher := sha256.New()
	hasher.Write([]byte(service + "\x00"))
	hasher.Write(ca)
	hasher.Write([]byte{0})
	hasher.Write(certificate)
	hash := hasher.Sum(nil)
	if len(service) > 20 {
		service = service[:20]
	}
	return fmt.Sprintf("%s%s-%x", containerDaemonSecretPrefix, service, hash[:16])
}

func checkContainerDaemonSecret(secret *corev1.Secret, t Target, service, binding string, ca, certificate, key []byte) error {
	if err := owned(secret, t); err != nil {
		return err
	}
	if secret.Type != corev1.SecretTypeTLS || secret.Immutable == nil || !*secret.Immutable || secret.Labels[serviceKey] != service || secret.Labels[containerDaemonLabel] != binding {
		return fmt.Errorf("container daemon trust Secret ownership, type or immutability changed")
	}
	if !bytes.Equal(secret.Data[corev1.TLSCertKey], certificate) || !bytes.Equal(secret.Data[corev1.TLSPrivateKeyKey], key) || !bytes.Equal(secret.Data[corev1.ServiceAccountRootCAKey], ca) {
		return fmt.Errorf("container daemon trust Secret content does not match the operator-declared material")
	}
	return nil
}

func (c *Client) prepareContainerDaemon(ctx context.Context, t Target, name string, svc spec.Service, wanted *appsv1.Deployment) error {
	if svc.ContainerDaemon == "" {
		return nil
	}
	b, err := c.resolveContainerDaemon(t.Project, t.Environment, t.Spec.Name, name, svc.ContainerDaemon)
	if err != nil {
		return err
	}
	ca, certificate, key, err := containerDaemonTrust(b)
	if err != nil {
		return fmt.Errorf("container daemon %s: %w", b.Name, err)
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	secretName := containerDaemonSecretName(name, ca, certificate)
	labels := labelsFor(t, name)
	labels[containerDaemonLabel] = b.Name
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	existing, err := api.Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: Namespace(t.ApplicationID), Labels: labels},
			Immutable:  ptr(true), Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: key, corev1.ServiceAccountRootCAKey: ca},
		}, metav1.CreateOptions{})
	} else if err == nil {
		err = checkContainerDaemonSecret(existing, t, name, b.Name, ca, certificate, key)
	}
	if err != nil {
		return err
	}
	endpoint := strings.Replace(b.Endpoint, "tcps://", "tcp://", 1)
	pod := &wanted.Spec.Template.Spec
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: containerDaemonVolume, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: secretName, DefaultMode: ptr(int32(0440)), Items: []corev1.KeyToPath{
			{Key: corev1.ServiceAccountRootCAKey, Path: containerDaemonCAFile},
			{Key: corev1.TLSCertKey, Path: containerDaemonCertFile},
			{Key: corev1.TLSPrivateKeyKey, Path: containerDaemonKeyFile},
		},
	}}})
	container := &pod.Containers[0]
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: containerDaemonVolume, MountPath: spec.ContainerDaemonDirectory, ReadOnly: true})
	// DOCKER_TLS_VERIFY is what turns the tcp address into a verified mutual
	// TLS connection for every Docker client and SDK.
	container.Env = append(container.Env,
		corev1.EnvVar{Name: "DOCKER_HOST", Value: endpoint},
		corev1.EnvVar{Name: "DOCKER_TLS_VERIFY", Value: "1"},
		corev1.EnvVar{Name: "DOCKER_CERT_PATH", Value: spec.ContainerDaemonDirectory},
	)
	return nil
}

// One host and one port. A CIDR block would grant reach to neighbours of the
// granted daemon.
func containerDaemonEgressRule(b ContainerDaemonBinding) []networkingv1.NetworkPolicyEgressRule {
	parsed, err := url.Parse(b.Endpoint)
	if err != nil {
		return nil
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil
	}
	if address, err := netip.ParseAddr(host); err != nil || !address.Is4() {
		return nil
	}
	return []networkingv1.NetworkPolicyEgressRule{{
		To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: host + "/32"}}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(int32(number)))}},
	}}
}

// Delete trust Secrets no longer referenced by this revision or by a pod that
// is still draining.
func (c *Client) cleanupContainerDaemons(ctx context.Context, t Target) error {
	keep := map[string]bool{}
	relevant := false
	for _, name := range spec.Names(t.Spec) {
		ref := t.Spec.Services[name].ContainerDaemon
		if ref == "" {
			continue
		}
		relevant = true
		binding, err := c.resolveContainerDaemon(t.Project, t.Environment, t.Spec.Name, name, ref)
		if err != nil {
			return err
		}
		ca, certificate, _, err := containerDaemonTrust(binding)
		if err != nil {
			return fmt.Errorf("container daemon %s: %w", binding.Name, err)
		}
		keep[containerDaemonSecretName(name, ca, certificate)] = true
	}
	if t.Previous != nil {
		for _, svc := range t.Previous.Services {
			relevant = relevant || svc.ContainerDaemon != ""
		}
	}
	if !relevant {
		return nil
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID)
	secrets, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector + "," + containerDaemonLabel, Limit: 200})
	if err != nil {
		return err
	}
	if secrets.Continue != "" {
		return fmt.Errorf("too many container daemon trust Secrets for bounded cleanup")
	}
	pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "status.phase!=Succeeded,status.phase!=Failed", Limit: 100})
	if err != nil {
		return err
	}
	if pods.Continue != "" {
		return fmt.Errorf("too many pods for container daemon cleanup")
	}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.Secret != nil {
				keep[volume.Secret.SecretName] = true
			}
		}
	}
	for _, secret := range secrets.Items {
		if keep[secret.Name] || !strings.HasPrefix(secret.Name, containerDaemonSecretPrefix) {
			continue
		}
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		if err := owned(&secret, t); err != nil {
			return err
		}
		if err := c.kube.CoreV1().Secrets(secret.Namespace).Delete(ctx, secret.Name, deleteOptions(&secret)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
