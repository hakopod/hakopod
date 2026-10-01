package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (c *Client) reconcileVitessPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess public endpoint namespace ownership changed")
	}
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name)
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("Vitess public endpoint controller identity or revision changed")
	}
	controllerUID := object.GetUID()
	// Vitess mounts one shared leaf into its topology, control, tablet and
	// gateway components. Rotate that identity through the existing bounded,
	// quorum-safe path instead of restarting only the gateways.
	if err = c.renewVitessIdentity(ctx, d, before); err != nil {
		return err
	}
	object, err = api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() != controllerUID || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("Vitess public endpoint controller identity or revision changed")
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	if _, err = database.VerifyServerCertificate(certificate, ca, databaseIdentityNames(d), time.Now()); err != nil {
		return fmt.Errorf("Vitess public certificate names are pending: %w", err)
	}
	if len(d.PublicEndpointNames) > 0 {
		if _, err = database.VerifyServerCertificate(certificate, ca, d.PublicEndpointNames, time.Now()); err != nil {
			return fmt.Errorf("Vitess public certificate names are pending: %w", err)
		}
	}
	return nil
}

func vitessPublicTemplateIdentity(certificate []byte) (string, error) {
	if len(certificate) == 0 || len(certificate) > 64<<10 {
		return "", fmt.Errorf("Vitess public endpoint identity certificate is invalid")
	}
	return fmt.Sprintf("%x", sha256.Sum256(certificate)), nil
}

func vitessPublicEndpointPassword(secret *corev1.Secret, d database.Resource, namespaceUID types.UID) ([]byte, error) {
	if secret == nil || secret.Name != "database-credentials" || secret.UID == "" || secret.DeletionTimestamp != nil || secret.Type != corev1.SecretTypeBasicAuth || secret.Immutable == nil || !*secret.Immutable || len(secret.OwnerReferences) != 1 || string(secret.Data[corev1.BasicAuthUsernameKey]) != "app" {
		return nil, fmt.Errorf("Vitess public endpoint credentials are unavailable")
	}
	if err := databaseIdentityOwned(secret, d, namespaceUID); err != nil {
		return nil, fmt.Errorf("Vitess public endpoint credentials are unavailable")
	}
	password := secret.Data[corev1.BasicAuthPasswordKey]
	if len(password) < 32 || len(password) > 128 || strings.ContainsAny(string(password), "\r\n\x00") {
		return nil, fmt.Errorf("Vitess public endpoint credentials are unavailable")
	}
	return password, nil
}

func vitessPublicGatewayServiceOwned(service *corev1.Service, d database.Resource, namespaceUID types.UID, route database.PublicEndpointRoute) bool {
	if service == nil || service.UID == "" || namespaceUID == "" || service.DeletionTimestamp != nil || service.Namespace != DatabaseNamespace(d.ID) || service.Name != route.BackendService || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ClusterIP == corev1.ClusterIPNone || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 || service.Labels[databaseOwner] != d.ID || service.Labels[managedBy] != "hakopod" || !reflect.DeepEqual(service.Spec.Selector, map[string]string{databaseOwner: d.ID, vitessComponentLabel: "gateway"}) || len(service.OwnerReferences) != 1 || len(service.Spec.Ports) != 1 {
		return false
	}
	owner := service.OwnerReferences[0]
	if owner.APIVersion != "v1" || owner.Kind != "Namespace" || owner.Name != service.Namespace || owner.UID != namespaceUID {
		return false
	}
	port := service.Spec.Ports[0]
	return port.Name == route.BackendPortName && port.Protocol == corev1.ProtocolTCP && port.Port == int32(route.BackendPort) && port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == int32(route.BackendPort)
}

type vitessPublicGateway struct {
	Member  database.Member
	Address string
	Direct  bool
}

func (c *Client) verifyVitessPublicGatewayInventory(ctx context.Context, d database.Resource, namespaceUID, databaseUID types.UID, expectedIdentity string) ([]vitessPublicGateway, error) {
	observed := d.Observation.Routing.Members
	if namespaceUID == "" || databaseUID == "" || expectedIdentity == "" || len(observed) != d.Spec.VitessGateways() {
		return nil, fmt.Errorf("Vitess public endpoint gateway inventory is unavailable")
	}
	expected := make(map[string]database.Member, len(observed))
	uids := make(map[string]bool, len(observed))
	for _, member := range observed {
		_, duplicateName := expected[member.Name]
		if member.Name == "" || member.UID == "" || member.Role != "gateway" || !member.Ready || duplicateName || uids[member.UID] {
			return nil, fmt.Errorf("Vitess public endpoint gateway inventory is invalid")
		}
		expected[member.Name] = member
		uids[member.UID] = true
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=gateway", Limit: int64(d.Spec.VitessGateways() + 1), FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) != len(expected) {
		return nil, fmt.Errorf("Vitess public endpoint gateway pod inventory changed")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]corev1.Pod, len(pods.Items))
	addresses := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		member, ok := expected[pod.Name]
		ip := net.ParseIP(pod.Status.PodIP)
		if !ok || pod.Namespace != DatabaseNamespace(d.ID) || member.UID != string(pod.UID) || member.Node != pod.Spec.NodeName || member.Phase != string(pod.Status.Phase) || pod.Labels[databaseOwner] != d.ID || pod.Labels[managedBy] != "hakopod" || pod.Labels[vitessComponentLabel] != "gateway" || !c.vitessPodOwned(ctx, pod, databaseUID) || !vitessPodReady(pod) || !vitessPodMatches(pod, d) || !databasePodPolicyMatches(pod, policy) || !vitessPodIdentityMatches(pod, expectedIdentity) || ip == nil || ip.To4() == nil || addresses[pod.Status.PodIP] {
			return nil, fmt.Errorf("Vitess public endpoint gateway pod ownership changed")
		}
		byName[pod.Name] = pod
		addresses[pod.Status.PodIP] = true
	}
	gateways := make([]vitessPublicGateway, 0, len(observed))
	for _, member := range observed {
		pod, ok := byName[member.Name]
		if !ok {
			return nil, fmt.Errorf("Vitess public endpoint gateway pod inventory changed")
		}
		gateways = append(gateways, vitessPublicGateway{Member: member, Address: pod.Status.PodIP, Direct: true})
	}
	return gateways, nil
}

func vitessPublicGatewayEndpointsOwned(endpointSlices []discoveryv1.EndpointSlice, service *corev1.Service, gateways []vitessPublicGateway, route database.PublicEndpointRoute) bool {
	if service == nil || service.UID == "" || len(endpointSlices) != 1 || len(gateways) == 0 {
		return false
	}
	slice := endpointSlices[0]
	if slice.Namespace != service.Namespace || slice.DeletionTimestamp != nil || slice.AddressType != discoveryv1.AddressTypeIPv4 || slice.Labels[discoveryv1.LabelServiceName] != service.Name || len(slice.OwnerReferences) != 1 || len(slice.Ports) != 1 || !databaseEndpointSlicePortMatches(slice.Ports, route.BackendPortName, int32(route.BackendPort)) || len(slice.Endpoints) != len(gateways) {
		return false
	}
	owner := slice.OwnerReferences[0]
	if owner.APIVersion != "v1" || owner.Kind != "Service" || owner.Name != service.Name || owner.UID != service.UID || owner.Controller == nil || !*owner.Controller {
		return false
	}
	expected := make(map[string]vitessPublicGateway, len(gateways))
	for _, gateway := range gateways {
		_, duplicateName := expected[gateway.Member.Name]
		ip := net.ParseIP(gateway.Address)
		if gateway.Member.Name == "" || gateway.Member.UID == "" || ip == nil || ip.To4() == nil || duplicateName {
			return false
		}
		expected[gateway.Member.Name] = gateway
	}
	seen := make(map[string]bool, len(slice.Endpoints))
	for _, endpoint := range slice.Endpoints {
		if endpoint.TargetRef == nil || endpoint.TargetRef.APIVersion != "v1" || endpoint.TargetRef.Kind != "Pod" || endpoint.TargetRef.Name == "" || endpoint.TargetRef.UID == "" || endpoint.TargetRef.Namespace != "" && endpoint.TargetRef.Namespace != service.Namespace || endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready || endpoint.Conditions.Serving != nil && !*endpoint.Conditions.Serving || endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating || len(endpoint.Addresses) != 1 || seen[endpoint.TargetRef.Name] {
			return false
		}
		gateway, ok := expected[endpoint.TargetRef.Name]
		if !ok || string(endpoint.TargetRef.UID) != gateway.Member.UID || endpoint.Addresses[0] != gateway.Address {
			return false
		}
		seen[endpoint.TargetRef.Name] = true
	}
	return len(seen) == len(expected)
}

type vitessPublicStream struct {
	net.Conn
	close func()
}

func verifyVitessPublicProbeResults(target, expected string, targetErr, plaintextErr error) error {
	if targetErr != nil || target != expected {
		return fmt.Errorf("Vitess public endpoint did not prove its server-owned target")
	}
	if !vitessPlaintextRefused(plaintextErr) {
		return fmt.Errorf("Vitess public endpoint did not prove plaintext refusal")
	}
	return nil
}

func vitessPublicProbeTargets(gateways []vitessPublicGateway, serviceAddress string) ([]vitessPublicGateway, error) {
	serviceIP := net.ParseIP(serviceAddress)
	if len(gateways) == 0 || serviceIP == nil || serviceIP.To4() == nil {
		return nil, fmt.Errorf("Vitess public endpoint probe inventory is invalid")
	}
	targets := make([]vitessPublicGateway, 0, len(gateways)+1)
	seen := make(map[string]bool, len(gateways)+1)
	for _, gateway := range gateways {
		ip := net.ParseIP(gateway.Address)
		if gateway.Member.Name == "" || gateway.Member.UID == "" || ip == nil || ip.To4() == nil || seen[gateway.Address] {
			return nil, fmt.Errorf("Vitess public endpoint probe inventory is invalid")
		}
		seen[gateway.Address] = true
		gateway.Direct = true
		targets = append(targets, gateway)
	}
	if seen[serviceAddress] {
		return nil, fmt.Errorf("Vitess public endpoint probe inventory is invalid")
	}
	targets = append(targets, vitessPublicGateway{Member: gateways[0].Member, Address: serviceAddress})
	return targets, nil
}

func (s *vitessPublicStream) Close() error { s.close(); return nil }

func (c *Client) vitessPublicEndpointStream(ctx context.Context, d database.Resource, member database.Member, address string, port int, direct bool) (net.Conn, error) {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil || port != 3306 {
		return nil, fmt.Errorf("Vitess public endpoint probe destination is invalid")
	}
	pod, container, err := c.vitessExecTarget(ctx, d, member)
	if err != nil || container != "vtgate" || direct && pod.Status.PodIP != address {
		return nil, fmt.Errorf("Vitess public endpoint gateway identity changed")
	}
	streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	script := `set -eu
exec 3<>/dev/tcp/$1/$2
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script, "vitess-public", address, strconv.Itoa(port)}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, "POST", u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("Vitess public endpoint transport is unavailable")
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-streamCtx.Done(); closeStream() }()
	return &vitessPublicStream{Conn: conn, close: closeStream}, nil
}

func (c *Client) vitessPublicEndpointClient(ctx context.Context, d database.Resource, member database.Member, route database.PublicEndpointRoute, host, address string, password []byte, config *tls.Config, direct bool) (*sql.DB, error) {
	if route.Purpose != "read_write" || route.ReadOnly || route.BackendUser != "app" || route.BackendDatabase != "app@primary" {
		return nil, fmt.Errorf("Vitess public endpoint target is invalid")
	}
	settings := mysqlclient.NewConfig()
	settings.User = route.BackendUser
	settings.Passwd = string(password)
	settings.DBName = route.BackendDatabase
	settings.Net = "tcp"
	settings.Addr = net.JoinHostPort(host, strconv.Itoa(route.BackendPort))
	settings.TLS = config
	settings.Timeout = 5 * time.Second
	settings.ReadTimeout = 5 * time.Second
	settings.WriteTimeout = 5 * time.Second
	settings.MaxAllowedPacket = 1 << 20
	settings.Logger = log.New(io.Discard, "", 0)
	settings.DialFunc = func(dial context.Context, network, destination string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || destination != settings.Addr {
			return nil, fmt.Errorf("Vitess requested an unexpected public gateway")
		}
		// The driver cancels its dial context after authentication. Keep the
		// established exec stream attached to the bounded probe lifetime.
		return c.vitessPublicEndpointStream(ctx, d, member, address, route.BackendPort, direct)
	}
	connector, err := mysqlclient.NewConnector(settings)
	if err != nil {
		return nil, fmt.Errorf("Vitess public endpoint client configuration failed")
	}
	client := sql.OpenDB(connector)
	client.SetMaxOpenConns(1)
	client.SetMaxIdleConns(0)
	client.SetConnMaxLifetime(15 * time.Second)
	return client, nil
}

func (c *Client) verifyVitessPublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	route, err := databasePublicEndpointBackend(d, endpoint.Spec.Purpose)
	if err != nil || route.Routing != "vitess_gateway" || d.Observation.Routing == nil || !d.Observation.Routing.Ready || d.Observation.Routing.Kind != "vtgate" || len(d.Observation.Routing.Members) != d.Spec.VitessGateways() || !d.Observation.Fresh(time.Now(), d.Revision) {
		return fmt.Errorf("Vitess public endpoint gateway inventory is unavailable")
	}
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess public endpoint namespace ownership changed")
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(namespace.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("Vitess public endpoint controller identity or revision changed")
	}
	service, err := c.kube.CoreV1().Services(namespace.Name).Get(ctx, route.BackendService, metav1.GetOptions{})
	if err != nil || !vitessPublicGatewayServiceOwned(service, d, namespace.UID, route) {
		return fmt.Errorf("Vitess public endpoint gateway service ownership changed")
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, route)
	if err != nil {
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
	issued, err := database.VerifyServerCertificate(certificate, ca, []string{endpoint.Allocation.Host}, time.Now())
	if err != nil {
		return fmt.Errorf("Vitess public endpoint certificate is unavailable: %w", err)
	}
	templateIdentity, err := vitessPublicTemplateIdentity(certificate)
	if err != nil {
		return err
	}
	gateways, err := c.verifyVitessPublicGatewayInventory(ctx, d, namespace.UID, object.GetUID(), templateIdentity)
	if err != nil {
		return err
	}
	slices, err := c.kube.DiscoveryV1().EndpointSlices(service.Namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + service.Name, Limit: 2})
	if err != nil || slices.Continue != "" || !vitessPublicGatewayEndpointsOwned(slices.Items, service, gateways, route) {
		return fmt.Errorf("Vitess public endpoint gateway EndpointSlice ownership changed")
	}
	targets, err := vitessPublicProbeTargets(gateways, address)
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Vitess public endpoint credentials are unavailable")
	}
	password, err := vitessPublicEndpointPassword(secret, d, namespace.UID)
	if err != nil {
		return err
	}
	groupCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, probe := range targets {
		config, err := redisTLSConfig(trust, endpoint.Allocation.Host)
		if err != nil {
			return err
		}
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != issued.Fingerprint {
				return fmt.Errorf("Vitess gateway has not loaded its current public certificate")
			}
			return nil
		}
		client, err := c.vitessPublicEndpointClient(groupCtx, d, probe.Member, route, endpoint.Allocation.Host, probe.Address, password, config, probe.Direct)
		if err != nil {
			return err
		}
		var target string
		targetErr := client.QueryRowContext(groupCtx, "SHOW VITESS_TARGET").Scan(&target)
		_ = client.Close()
		plain, err := c.vitessPublicEndpointClient(groupCtx, d, probe.Member, route, endpoint.Allocation.Host, probe.Address, password, nil, probe.Direct)
		if err != nil {
			return err
		}
		plaintextErr := plain.PingContext(groupCtx)
		_ = plain.Close()
		if err = verifyVitessPublicProbeResults(target, route.BackendDatabase, targetErr, plaintextErr); err != nil {
			return err
		}
	}
	return nil
}
