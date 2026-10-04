package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type PlatformTLSCertificateObservation struct {
	Component   string    `json:"component"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expires_at"`
	Verified    bool      `json:"verified"`
}

type PlatformTLSObservation struct {
	Mode              string                              `json:"mode"`
	IssuerFingerprint string                              `json:"issuer_fingerprint"`
	Certificates      []PlatformTLSCertificateObservation `json:"certificates"`
	VerifiedAt        time.Time                           `json:"verified_at"`
}

// ManagedPlatformTrust returns public trust only after the durable namespace
// identity agrees with the live resource. Caller authorization belongs to API.
func (r *ManagedPlatformRuntime) ManagedPlatformTrust(ctx context.Context, item store.ManagedPlatform) (database.PublicTrust, error) {
	if r == nil || r.Cluster == nil || item.Spec.TLSMode != "managed" || item.DeletedAt != nil {
		return database.PublicTrust{}, fmt.Errorf("managed platform trust is unavailable")
	}
	expectedUID, _ := item.Observation["namespace_uid"].(string)
	if len(item.ID) != 32 || expectedUID == "" {
		return database.PublicTrust{}, fmt.Errorf("managed platform namespace identity is not observed")
	}
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, "managed-platform-"+item.ID, metav1.GetOptions{})
	if err != nil || string(ns.UID) != expectedUID || ns.DeletionTimestamp != nil || ns.Labels["hakopod.io/managed-platform-id"] != item.ID || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" {
		return database.PublicTrust{}, fmt.Errorf("managed platform namespace ownership changed")
	}
	root, err := r.Cluster.kube.CoreV1().Secrets(ns.Name).Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	if err != nil {
		return database.PublicTrust{}, fmt.Errorf("managed platform trust is unavailable")
	}
	if err = verifySupabaseOwned(root, item.ID, ns.UID); err != nil {
		return database.PublicTrust{}, err
	}
	trust, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil || ca.Subject.CommonName != "Hakopod platform "+item.ID {
		return database.PublicTrust{}, fmt.Errorf("managed platform trust is invalid")
	}
	var observed PlatformTLSObservation
	encoded, encodeErr := json.Marshal(item.Observation["tls"])
	if encodeErr != nil || json.Unmarshal(encoded, &observed) != nil || observed.Mode != "managed" || observed.IssuerFingerprint != trust.Fingerprint || observed.VerifiedAt.IsZero() {
		return database.PublicTrust{}, fmt.Errorf("managed platform trust has not been verified for the current issuer")
	}
	return trust, nil
}

type platformTLSProbeTarget struct {
	component, logical, serverName string
	port                           int
	postgres                       bool
}

func platformTLSProbeTargets(spec managedplatform.Spec, namespace string) []platformTLSProbeTarget {
	target := func(component, logical, service string, port int, postgres bool) platformTLSProbeTarget {
		return platformTLSProbeTarget{component, logical, service + "." + namespace + ".svc", port, postgres}
	}
	if spec.Kind == "supabase" {
		return []platformTLSProbeTarget{target("database", "database-tls-certificate", "db", 5432, true), target("api-gateway", "gateway-tls-certificate", "api-gw", 8443, false)}
	}
	result := []platformTLSProbeTarget{target("broker", "broker-auth", "neon-broker", 50051, false), target("storage-controller", "controller-auth", "neon-storage-controller", 6699, false), target("controller-database", "controller-database-password", "neon-controller-database", 5432, true), target("proxy", "proxy-auth", "neon-proxy", 5432, true)}
	if spec.Neon == nil {
		return nil
	}
	for i := 0; i < spec.Neon.ComputeReplicas; i++ {
		name := "compute-" + strconv.Itoa(i)
		result = append(result, target(name, "compute-auth", "neon-"+name+"-control", 3081, false))
	}
	for i := 0; i < spec.Neon.Pageservers; i++ {
		name := "pageserver-" + strconv.Itoa(i)
		result = append(result, target(name, "pageserver-auth", "neon-"+name, 9898, false))
	}
	for i := 0; i < spec.Neon.Safekeepers; i++ {
		name := "safekeeper-" + strconv.Itoa(i)
		result = append(result, target(name, "safekeeper-auth", "neon-"+name, 7676, false))
	}
	return result
}

func (c *Client) platformTLSProbePod(ctx context.Context, op store.ManagedPlatformOperation, ns *corev1.Namespace, component string, current map[string]store.PlatformResourceClaim) (*corev1.Pod, error) {
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + op.PlatformID + ",app.kubernetes.io/component=" + component, Limit: 3})
	if err != nil {
		return nil, err
	}
	if pods.Continue != "" || len(pods.Items) != 1 {
		return nil, fmt.Errorf("managed platform TLS probe requires exactly one current member")
	}
	pod := &pods.Items[0]
	if pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || len(pod.OwnerReferences) != 1 {
		return nil, fmt.Errorf("managed platform TLS probe member is unstable")
	}
	owner := pod.OwnerReferences[0]
	if owner.Kind == "ReplicaSet" && owner.APIVersion == "apps/v1" {
		rs, err := c.kube.AppsV1().ReplicaSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil || rs.UID != owner.UID || rs.DeletionTimestamp != nil || len(rs.OwnerReferences) != 1 {
			return nil, fmt.Errorf("managed platform TLS probe replica ownership changed")
		}
		owner = rs.OwnerReferences[0]
	}
	var object metav1.Object
	kind := ""
	switch owner.Kind {
	case "Deployment":
		item, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		object, kind = item, "deployment"
	case "StatefulSet":
		item, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		object, kind = item, "statefulset"
	default:
		return nil, fmt.Errorf("managed platform TLS probe workload kind changed")
	}
	if owner.APIVersion != "apps/v1" || object.GetUID() != owner.UID {
		return nil, fmt.Errorf("managed platform TLS probe workload identity changed")
	}
	if err = verifySupabaseOwned(object, op.PlatformID, ns.UID); err != nil {
		return nil, err
	}
	if err = verifySupabaseClaimedUID(kind, object, current); err != nil {
		return nil, err
	}
	return pod, nil
}

// Port forwarding reaches an exact owned pod without publishing a listener or
// requiring unqualified client tools inside application images. The only local
// listener is loopback, ephemeral and closed when this bounded probe completes.
func (c *Client) platformTLSStream(ctx context.Context, pod *corev1.Pod, port int) (net.Conn, func(), error) {
	noop := func() {}
	if c.execConfig == nil {
		return nil, noop, fmt.Errorf("managed platform TLS probe transport is unavailable")
	}
	transport, upgrader, err := spdy.RoundTripperFor(c.execConfig)
	if err != nil {
		return nil, noop, err
	}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport, Timeout: 3 * time.Second}, http.MethodPost, u)
	stop, ready := make(chan struct{}), make(chan struct{})
	var once sync.Once
	closeStream := func() { once.Do(func() { close(stop) }) }
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, noop, err
	}
	done := make(chan error, 1)
	go func() { done <- forwarder.ForwardPorts() }()
	go func() {
		select {
		case <-ctx.Done():
			closeStream()
		case <-stop:
		}
	}()
	select {
	case <-ctx.Done():
		closeStream()
		return nil, noop, ctx.Err()
	case err = <-done:
		closeStream()
		return nil, noop, fmt.Errorf("managed platform TLS probe transport closed")
	case <-ready:
	}
	current, err := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil || current.UID != pod.UID || current.DeletionTimestamp != nil {
		closeStream()
		return nil, noop, fmt.Errorf("managed platform TLS probe member changed during transport setup")
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) != 1 {
		closeStream()
		return nil, noop, fmt.Errorf("managed platform TLS probe port was not allocated")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ports[0].Local))))
	if err != nil {
		closeStream()
		return nil, noop, err
	}
	cleanup := func() { _ = conn.Close(); closeStream() }
	return conn, cleanup, nil
}

func verifyPlatformTLSConnection(ctx context.Context, conn net.Conn, target platformTLSProbeTarget, expected []byte, roots *x509.CertPool) (*x509.Certificate, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if target.postgres {
		if _, err := conn.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47}); err != nil {
			return nil, err
		}
		response := make([]byte, 1)
		if _, err := io.ReadFull(conn, response); err != nil || response[0] != 'S' {
			return nil, fmt.Errorf("managed platform PostgreSQL listener did not accept TLS")
		}
	}
	secured := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.serverName, RootCAs: roots})
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("managed platform served TLS certificate could not be verified")
	}
	chain := secured.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, fmt.Errorf("managed platform TLS peer certificate is missing")
	}
	// expected is a DER certificate, obtained from the validated snapshot.
	if sha256.Sum256(chain[0].Raw) != sha256.Sum256(expected) {
		return nil, fmt.Errorf("managed platform member is still serving a previous certificate")
	}
	return chain[0], nil
}

func (c *Client) observeManagedPlatformTLS(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, spec managedplatform.Spec, snapshots map[string]map[string][]byte, current map[string]store.PlatformResourceClaim, before func() error) (PlatformTLSObservation, error) {
	result := PlatformTLSObservation{Mode: "managed", Certificates: []PlatformTLSCertificateObservation{}}
	root, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if err = verifySupabaseOwned(root, op.PlatformID, ns.UID); err != nil {
		return result, err
	}
	if err = state.VerifyPlatformResourceClaim(ctx, op, current[supabaseClaimKey("secret", root.Name)]); err != nil {
		return result, err
	}
	trust, _, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		return result, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root.Data["ca.crt"]) {
		return result, fmt.Errorf("managed platform TLS trust is invalid")
	}
	result.IssuerFingerprint = trust.Fingerprint
	targets := platformTLSProbeTargets(spec, ns.Name)
	if len(targets) == 0 || len(targets) > managedplatform.MaxComponents {
		return result, fmt.Errorf("managed platform TLS endpoint inventory is invalid")
	}
	for _, target := range targets {
		if err = before(); err != nil {
			return result, err
		}
		data := snapshots[secretSnapshotNameForCluster(spec.Secrets[target.logical])]
		pair, err := tls.X509KeyPair(data["tls.crt"], data["tls.key"])
		if err != nil || len(pair.Certificate) != 1 {
			return result, fmt.Errorf("managed platform TLS snapshot is invalid")
		}
		pod, err := c.platformTLSProbePod(ctx, op, ns, target.component, current)
		if err != nil {
			return result, err
		}
		probe, stop := context.WithTimeout(ctx, 3*time.Second)
		conn, closeStream, err := c.platformTLSStream(probe, pod, target.port)
		if err != nil {
			stop()
			return result, err
		}
		leaf, err := verifyPlatformTLSConnection(probe, conn, target, pair.Certificate[0], roots)
		closeStream()
		stop()
		if err != nil {
			return result, err
		}
		digest := sha256.Sum256(leaf.Raw)
		result.Certificates = append(result.Certificates, PlatformTLSCertificateObservation{Component: target.component, Fingerprint: hex.EncodeToString(digest[:]), ExpiresAt: leaf.NotAfter, Verified: true})
	}
	result.VerifiedAt = time.Now().UTC()
	return result, nil
}
