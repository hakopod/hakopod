package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	goora "github.com/sijms/go-ora/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func oracleFreePublicEndpointSpec(s database.Spec) bool {
	return s.Engine == "oracle" && s.Oracle != nil && s.Oracle.Edition == "free" && s.Mode == "standalone" && s.Replicas == 0 && s.Shards == 1 && s.TLSRequired()
}

func (c *Client) oraclePublicEndpointWorkload(ctx context.Context, d database.Resource) (*corev1.Namespace, *appsv1.StatefulSet, error) {
	if !oracleFreePublicEndpointSpec(d.Spec) {
		return nil, nil, fmt.Errorf("Oracle Free public endpoint workload is unavailable")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, nil, fmt.Errorf("Oracle Free public endpoint namespace ownership changed")
	}
	set, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	selector := map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}
	if err != nil || set.UID == "" || set.DeletionTimestamp != nil || set.Labels[databaseOwner] != d.ID || set.Labels[managedBy] != "hakopod" || set.Annotations["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) || set.Spec.Replicas == nil || *set.Spec.Replicas != 1 || set.Spec.ServiceName != "database" || set.Spec.Selector == nil || !reflect.DeepEqual(set.Spec.Selector.MatchLabels, selector) {
		return nil, nil, fmt.Errorf("Oracle Free public endpoint workload identity or revision changed")
	}
	return ns, set, nil
}

func oraclePublicEndpointServiceOwned(service *corev1.Service, d database.Resource, namespaceUID string, route database.PublicEndpointRoute) bool {
	if service == nil || namespaceUID == "" || service.UID == "" || service.DeletionTimestamp != nil || service.Namespace != DatabaseNamespace(d.ID) || service.Name != route.BackendService || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 || service.Spec.PublishNotReadyAddresses || len(service.Spec.Ports) != 1 || !reflect.DeepEqual(service.Spec.Selector, map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}) {
		return false
	}
	if !mongodbSupportOwned(service, d, typesUID(namespaceUID)) {
		return false
	}
	for _, port := range service.Spec.Ports {
		if port.Name == route.BackendPortName && port.Protocol == corev1.ProtocolTCP && port.Port == int32(route.BackendPort) && port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == int32(route.BackendPort) {
			return true
		}
	}
	return false
}

func oraclePublicEndpointPodReady(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Spec.HostNetwork || pod.Spec.HostPID || len(pod.Spec.Containers) != 1 {
		return false
	}
	container := pod.Spec.Containers[0]
	if container.Name != "oracle" || len(container.Ports) != 1 || container.Ports[0].Name != "tcps" || container.Ports[0].Protocol != corev1.ProtocolTCP || container.Ports[0].ContainerPort != 2484 || container.Ports[0].HostPort != 0 {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func oraclePublicEndpointPVCsOwned(ctx context.Context, c *Client, d database.Resource, pod *corev1.Pod) error {
	_, err := oraclePublicEndpointPVCIdentities(ctx, c, d, pod)
	return err
}

func oraclePublicEndpointPVCIdentities(ctx context.Context, c *Client, d database.Resource, pod *corev1.Pod) ([]database.PublicEndpointTransitionPVC, error) {
	wanted := map[string]string{"data": "data-database-0", "backup": "backup-database-0"}
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && wanted[volume.Name] == volume.PersistentVolumeClaim.ClaimName {
			delete(wanted, volume.Name)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("Oracle Free public endpoint persistent volume attachment changed")
	}
	identities := make([]database.PublicEndpointTransitionPVC, 0, 2)
	for _, name := range []string{"data-database-0", "backup-database-0"} {
		claim, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil || claim.UID == "" || claim.DeletionTimestamp != nil || claim.Labels[databaseOwner] != d.ID || claim.Labels[managedBy] != "hakopod" || claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
			return nil, fmt.Errorf("Oracle Free public endpoint persistent volume identity changed")
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil || volume.UID == "" || volume.DeletionTimestamp != nil || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.APIVersion != "v1" || volume.Spec.ClaimRef.Kind != "PersistentVolumeClaim" || volume.Spec.ClaimRef.Namespace != claim.Namespace || volume.Spec.ClaimRef.Name != claim.Name || volume.Spec.ClaimRef.UID != claim.UID {
			return nil, fmt.Errorf("Oracle Free public endpoint backing volume identity changed")
		}
		encoded, err := json.Marshal(volume.Spec.PersistentVolumeSource)
		if err != nil {
			return nil, fmt.Errorf("Oracle Free public endpoint backing volume identity is invalid")
		}
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
		identities = append(identities, database.PublicEndpointTransitionPVC{Name: name, UID: string(claim.UID), VolumeName: claim.Spec.VolumeName, PersistentVolumeUID: string(volume.UID), BackingVolumeFingerprint: fingerprint})
	}
	return identities, nil
}

func (c *Client) oraclePublicEndpointTarget(ctx context.Context, d database.Resource, route database.PublicEndpointRoute) (database.Member, string, error) {
	if !d.Observation.Fresh(time.Now(), d.Revision) || d.Observation.Status != "ready" || len(d.Observation.Members) != 1 || d.Observation.Primary != d.Observation.Members[0].Name || !d.Observation.Members[0].Ready || d.Observation.Members[0].Role != "primary" || d.Observation.TLS == nil || !d.Observation.TLS.Verified || !d.Observation.TLS.PlaintextRejected || d.Observation.TLS.Fingerprint == "" {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint requires a current verified primary")
	}
	wantedTopology := fmt.Sprintf("%x", sha256.Sum256([]byte(d.Observation.Primary+":"+d.Observation.Members[0].UID)))
	if d.Observation.TopologyFingerprint != wantedTopology {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint topology evidence is invalid")
	}
	foundRoute := false
	for _, endpoint := range d.Observation.Endpoints {
		foundRoute = foundRoute || endpoint.Purpose == route.Purpose && endpoint.Host == oracleHost(d) && endpoint.Port == route.BackendPort
	}
	if !foundRoute {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint is absent from the current observation")
	}
	ns, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil {
		return database.Member{}, "", err
	}
	if set.Status.ObservedGeneration < set.Generation || set.Status.CurrentReplicas != 1 || set.Status.UpdatedReplicas != 1 || set.Status.ReadyReplicas != 1 {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint workload revision has not converged")
	}
	service, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, route.BackendService, metav1.GetOptions{})
	if err != nil || !oraclePublicEndpointServiceOwned(service, d, string(ns.UID), route) {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint Service ownership or routing changed")
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, route)
	if err != nil {
		return database.Member{}, "", err
	}
	member := d.Observation.Members[0]
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil || container != "oracle" || pod.Namespace != ns.Name || pod.Name != "database-0" || pod.OwnerReferences == nil || !oraclePublicEndpointPodReady(pod) || member.Node != pod.Spec.NodeName || !databasePodMatches(*pod, d) {
		return database.Member{}, "", fmt.Errorf("Oracle Free public endpoint member identity or readiness changed")
	}
	if err = oraclePublicEndpointPVCsOwned(ctx, c, d, pod); err != nil {
		return database.Member{}, "", err
	}
	return member, address, nil
}

func (c *Client) reconcileOraclePublicNames(ctx context.Context, d database.Resource, before func() error) error {
	if _, _, err := c.oraclePublicEndpointWorkload(ctx, d); err != nil {
		return err
	}
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	// Oracle Free currently loads its server wallet during container startup.
	// This changes the owned StatefulSet template and replaces its only pod. The
	// publication worker must keep the route closed and use a durable, reviewed
	// identity-transition phase before calling this path; it must never overwrite
	// the old topology fingerprint with the replacement member's fingerprint.
	if err := c.prepareOracleSecurity(ctx, d, before); err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	if len(d.PublicEndpointNames) == 0 {
		return nil
	}
	if _, err = database.VerifyServerCertificate(certificate, ca, databaseIdentityNames(d), time.Now()); err != nil {
		return fmt.Errorf("Oracle Free public certificate names are pending: %w", err)
	}
	return nil
}

const oraclePublicEndpointTCPRelay = `set -eu
exec 3<>/dev/tcp/"$1"/2484
exec 4<&0
cat <&3 & reader=$!
cat <&4 >&3 & writer=$!
trap 'kill "$reader" "$writer" 2>/dev/null || true; wait "$reader" "$writer" 2>/dev/null || true' EXIT
wait -n "$reader" "$writer" || true`

func (c *Client) oraclePublicEndpointStream(ctx context.Context, d database.Resource, member database.Member, address string) (net.Conn, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
		return nil, fmt.Errorf("Oracle Free public probe target is invalid")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil || !oracleFreePublicEndpointSpec(d.Spec) || container != "oracle" {
		return nil, fmt.Errorf("Oracle Free public probe member changed")
	}
	command := []string{"bash", "-c", oraclePublicEndpointTCPRelay, "verify-oracle-public-tcps", ip.String()}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return nil, fmt.Errorf("Oracle Free public probe transport is unavailable")
	}
	step, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, stream := net.Pipe()
	var once sync.Once
	finish := func(remoteEOF bool) {
		once.Do(func() {
			if !remoteEOF {
				_ = conn.Close()
			}
			_ = stream.Close()
			cancel()
		})
	}
	go func() {
		err := executor.StreamWithContext(step, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
		finish(err == nil && step.Err() == nil)
	}()
	go func() {
		<-step.Done()
		finish(false)
	}()
	return &mongodbStreamConn{Conn: conn, close: func() { finish(false); _ = conn.Close() }}, nil
}

func (c *Client) oraclePublicEndpointConnection(ctx context.Context, d database.Resource, member database.Member, route database.PublicEndpointRoute, host, address, password string, secure bool, fingerprint string, trust database.PublicTrust) (*sql.DB, error) {
	options := map[string]string{"CONNECTION TIMEOUT": "5", "TIMEOUT": "6", "SSL VERIFY": "true", "FAST LOGIN": "false"}
	if secure {
		options["SSL"] = "enable"
	}
	connector := goora.NewConnector(goora.BuildUrl(host, route.BackendPort, route.BackendDatabase, route.BackendUser, password, options)).(*goora.OracleConnector)
	if secure {
		config, err := redisTLSConfig(trust, host)
		if err != nil {
			return nil, err
		}
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != fingerprint {
				return fmt.Errorf("Oracle Free has not loaded its issued public identity")
			}
			return nil
		}
		connector.WithTLSConfig(config)
	}
	connector.Dialer(oracleDialer(func(dial context.Context, network, requested string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || requested != net.JoinHostPort(host, strconv.Itoa(route.BackendPort)) {
			return nil, fmt.Errorf("Oracle Free requested an unexpected public probe endpoint")
		}
		return c.oraclePublicEndpointStream(dial, d, member, address)
	}))
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(15 * time.Second)
	return db, nil
}

func oraclePublicEndpointIdentity(ctx context.Context, client *sql.DB, route database.PublicEndpointRoute) error {
	var identity string
	if err := client.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','SESSION_USER')||'|'||SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&identity); err != nil || identity != route.BackendUser+"|"+route.BackendDatabase {
		return fmt.Errorf("Oracle Free public APP authentication or PDB selection failed")
	}
	return nil
}

func (c *Client) verifyOraclePublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || !oracleFreePublicEndpointSpec(d.Spec) || route.Purpose != "read_write" || route.Protocol != "oracle_tcps" || route.Routing != "direct" || route.BackendService != "database" || route.BackendPort != 2484 || route.BackendPortName != "tcps" || route.BackendUser != "APP" || route.BackendDatabase != "FREEPDB1" {
		return fmt.Errorf("Oracle Free public route is unavailable")
	}
	if names, err := database.NormalizePublicEndpointNames([]string{endpoint.Allocation.Host}); err != nil || len(names) != 1 {
		return fmt.Errorf("Oracle Free public hostname is invalid")
	}
	member, address, err := c.oraclePublicEndpointTarget(ctx, d, route)
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || string(secret.Data["username"]) != "app" || len(secret.Data["password"]) != 64 {
		return fmt.Errorf("Oracle Free public probe credentials are unavailable")
	}
	if decoded, decodeErr := hex.DecodeString(string(secret.Data["password"])); decodeErr != nil || len(decoded) != 32 {
		return fmt.Errorf("Oracle Free public probe credentials are invalid")
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, databaseIdentityNames(d), time.Now())
	if err != nil {
		return fmt.Errorf("Oracle Free public issued identity is invalid")
	}
	if d.Observation.TLS.Fingerprint != identity.Fingerprint || d.Observation.TLS.CAFingerprint != trust.Fingerprint {
		return fmt.Errorf("Oracle Free public served identity differs from the current observation")
	}
	verified := func(password string) error {
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		client, err := c.oraclePublicEndpointConnection(step, d, member, route, endpoint.Allocation.Host, address, password, true, identity.Fingerprint, trust)
		if err != nil {
			return err
		}
		defer client.Close()
		return oraclePublicEndpointIdentity(step, client, route)
	}
	if err = verified(string(secret.Data["password"])); err != nil {
		return err
	}
	step, cancel := context.WithTimeout(ctx, 8*time.Second)
	wrong, err := c.oraclePublicEndpointConnection(step, d, member, route, endpoint.Allocation.Host, address, "invalid-public-probe-password", true, identity.Fingerprint, trust)
	if err == nil {
		err = wrong.PingContext(step)
		_ = wrong.Close()
	}
	cancel()
	if err == nil {
		return fmt.Errorf("Oracle Free public endpoint accepted invalid credentials")
	}
	step, cancel = context.WithTimeout(ctx, 5*time.Second)
	plain, err := c.oraclePublicEndpointConnection(step, d, member, route, endpoint.Allocation.Host, address, string(secret.Data["password"]), false, "", trust)
	if err == nil {
		err = plain.PingContext(step)
		_ = plain.Close()
	}
	cancel()
	if err == nil {
		return fmt.Errorf("Oracle Free public endpoint accepted plaintext")
	}
	listenerCheck := []string{"bash", "-c", `awk 'FNR>1 && $4=="0A" && $2 ~ /:(05F1|157C)$/ {bad=1} END {exit bad}' /proc/net/tcp /proc/net/tcp6`}
	if err = c.DatabaseExec(ctx, d, member, listenerCheck, nil, nil); err != nil {
		return fmt.Errorf("Oracle Free plaintext or management listener is exposed")
	}
	return verified(string(secret.Data["password"]))
}
