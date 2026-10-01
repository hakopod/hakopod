package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (c *Client) mysqlPublicEndpointController(ctx context.Context, d database.Resource) (string, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return "", fmt.Errorf("MySQL public endpoint namespace ownership changed")
	}
	object, err := c.dynamic.Resource(mysqlDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return "", fmt.Errorf("MySQL public endpoint controller identity or revision changed")
	}
	return string(object.GetUID()), nil
}

func (c *Client) reconcileMySQLPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	if _, err := c.mysqlPublicEndpointController(ctx, d); err != nil {
		return err
	}
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
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
	if _, err = database.VerifyServerCertificate(certificate, ca, databaseIdentityNames(d), time.Now()); err != nil {
		return fmt.Errorf("MySQL public certificate names are pending: %w", err)
	}
	// The pinned operator reloads server TLS and rolls Router when this Secret
	// changes. Publication separately verifies every ready Router's served leaf.
	return nil
}

func mysqlPublicRouterServiceOwned(service *corev1.Service, d database.Resource, controllerUID string, route database.PublicEndpointRoute) bool {
	if service == nil || controllerUID == "" || service.Namespace != DatabaseNamespace(d.ID) || service.Name != "database" || service.DeletionTimestamp != nil {
		return false
	}
	selector := map[string]string{"component": "mysqlrouter", "tier": "mysql", "mysql.oracle.com/cluster": "database"}
	if !reflect.DeepEqual(service.Spec.Selector, selector) {
		return false
	}
	owned := false
	for _, ref := range service.OwnerReferences {
		owned = owned || string(ref.UID) == controllerUID && ref.Name == "database" && ref.Kind == "InnoDBCluster" && ref.APIVersion == "mysql.oracle.com/v2"
	}
	if !owned {
		return false
	}
	for _, port := range service.Spec.Ports {
		if port.Name == route.BackendPortName && port.Port == int32(route.BackendPort) && port.Protocol == corev1.ProtocolTCP && port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == int32(route.BackendPort) {
			return true
		}
	}
	return false
}

func (c *Client) mysqlPublicEndpointTargets(ctx context.Context, d database.Resource, route database.PublicEndpointRoute) ([]string, error) {
	if d.Spec.Engine != "mysql" || d.Observation.Routing == nil || !d.Observation.Routing.Ready || d.Observation.Routing.Kind != "mysql-router" || len(d.Observation.Routing.Members) != d.Spec.RouterInstances() || !d.Observation.Fresh(time.Now(), d.Revision) {
		return nil, fmt.Errorf("MySQL public endpoint requires a current verified Router inventory")
	}
	controllerUID, err := c.mysqlPublicEndpointController(ctx, d)
	if err != nil {
		return nil, err
	}
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, route.BackendService, metav1.GetOptions{})
	if err != nil || !mysqlPublicRouterServiceOwned(service, d, controllerUID, route) {
		return nil, fmt.Errorf("MySQL public endpoint Router service ownership or routing changed")
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, route)
	if err != nil {
		return nil, err
	}
	targets := []string{address}
	observed := map[string]database.Member{}
	for _, member := range d.Observation.Routing.Members {
		if !member.Ready || member.UID == "" || member.Name == "" || observed[member.Name].UID != "" {
			return nil, fmt.Errorf("MySQL public endpoint Router inventory is invalid")
		}
		observed[member.Name] = member
	}
	pods, err := c.kube.CoreV1().Pods(service.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String(), Limit: 5, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) != len(observed) || len(pods.Items) > 4 {
		return nil, fmt.Errorf("MySQL public endpoint Router targets changed or exceeded their bound")
	}
	deployment, err := c.kube.AppsV1().Deployments(service.Namespace).Get(ctx, "database-router", metav1.GetOptions{})
	if err != nil || deployment.UID == "" || deployment.DeletionTimestamp != nil {
		return nil, fmt.Errorf("MySQL public endpoint Router deployment is unavailable")
	}
	owned := false
	for _, ref := range deployment.OwnerReferences {
		owned = owned || string(ref.UID) == controllerUID && ref.Name == "database" && ref.Kind == "InnoDBCluster" && ref.APIVersion == "mysql.oracle.com/v2"
	}
	if !owned {
		return nil, fmt.Errorf("MySQL public endpoint Router deployment ownership changed")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		current, err := c.mysqlRouterMember(ctx, d, deployment, pod, policy)
		if err != nil || !current.Ready {
			return nil, fmt.Errorf("MySQL public endpoint Router is not currently ready and owned")
		}
		member, ok := observed[pod.Name]
		ip, parseErr := netip.ParseAddr(pod.Status.PodIP)
		if !ok || member.UID != string(pod.UID) || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Labels[databaseOwner] != d.ID || parseErr != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("MySQL public endpoint Router target identity changed")
		}
		targets = append(targets, ip.String())
	}
	return targets, nil
}

// The bridge accepts only a validated numeric Router destination. The native
// Go driver performs TLS, hostname verification and authentication; no password
// is passed to Kubernetes exec or command arguments.
func (c *Client) mysqlPublicEndpointStream(ctx context.Context, d database.Resource, member database.Member, address string, port int) (net.Conn, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || (port != 6446 && port != 6447) {
		return nil, fmt.Errorf("MySQL public endpoint probe destination is invalid")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil || d.Spec.Engine != "mysql" || container != "mysql" {
		return nil, fmt.Errorf("MySQL public endpoint probe member changed")
	}
	step, cancel := context.WithTimeout(ctx, 20*time.Second)
	script := `set -eu
exec 3<>/dev/tcp/"$1"/"$2"
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	command := []string{"bash", "-c", script, "verify-mysql-public-endpoint", ip.String(), strconv.Itoa(port)}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("MySQL public endpoint probe transport is unavailable")
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(step, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-step.Done(); closeStream() }()
	return &vitessStreamConn{Conn: conn, close: closeStream}, nil
}

func (c *Client) mysqlPublicEndpointClient(ctx context.Context, d database.Resource, member database.Member, host, address string, port int, password []byte, config *tls.Config) (*sql.DB, error) {
	settings := mysqlclient.NewConfig()
	settings.User, settings.Passwd, settings.DBName = "app", string(password), "app"
	settings.Net, settings.Addr, settings.TLS = "tcp", net.JoinHostPort(host, strconv.Itoa(port)), config
	settings.Timeout, settings.ReadTimeout, settings.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	settings.MaxAllowedPacket = 1 << 20
	settings.Logger = log.New(io.Discard, "", 0)
	settings.DialFunc = func(dial context.Context, network, requested string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || requested != settings.Addr {
			return nil, fmt.Errorf("MySQL public endpoint probe requested another destination")
		}
		// The driver cancels its dial context after authentication. Keep the
		// established exec stream attached to the bounded probe lifetime.
		return c.mysqlPublicEndpointStream(ctx, d, member, address, port)
	}
	connector, err := mysqlclient.NewConnector(settings)
	if err != nil {
		return nil, fmt.Errorf("MySQL public endpoint probe configuration is invalid")
	}
	client := sql.OpenDB(connector)
	client.SetMaxOpenConns(1)
	client.SetMaxIdleConns(0)
	client.SetConnMaxLifetime(20 * time.Second)
	return client, nil
}

func (c *Client) verifyMySQLPublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || route.Protocol != "mysql" || len(d.Observation.Members) == 0 {
		return fmt.Errorf("MySQL public endpoint probe route is unavailable")
	}
	if _, err = database.NormalizePublicEndpointNames([]string{endpoint.Allocation.Host}); err != nil {
		return err
	}
	targets, err := c.mysqlPublicEndpointTargets(ctx, d, route)
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
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 || strings.ContainsAny(string(secret.Data["password"]), "\r\n\x00") {
		return fmt.Errorf("MySQL public endpoint probe credentials are unavailable")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(2)
	for _, address := range targets {
		group.Go(func() error {
			config, err := redisTLSConfig(trust, endpoint.Allocation.Host)
			if err != nil {
				return err
			}
			config.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != issued.Fingerprint {
					return fmt.Errorf("MySQL Router has not loaded its current public certificate")
				}
				return nil
			}
			client, err := c.mysqlPublicEndpointClient(step, d, d.Observation.Members[0], endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], config)
			if err != nil {
				return err
			}
			defer client.Close()
			connection, err := client.Conn(step)
			if err != nil {
				return fmt.Errorf("MySQL Router public hostname or authentication verification failed")
			}
			var readOnly int
			roleErr := connection.QueryRowContext(step, "SELECT @@super_read_only").Scan(&readOnly)
			var name, version string
			tlsErr := connection.QueryRowContext(step, "SHOW SESSION STATUS LIKE 'Ssl_version'").Scan(&name, &version)
			_ = connection.Close()
			if roleErr != nil || tlsErr != nil || (readOnly != 0 && readOnly != 1) || (readOnly == 1) != route.ReadOnly || name != "Ssl_version" || (version != "TLSv1.2" && version != "TLSv1.3") {
				return fmt.Errorf("MySQL Router public route did not prove its role and encrypted backend")
			}
			plain, err := c.mysqlPublicEndpointClient(step, d, d.Observation.Members[0], endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], nil)
			if err != nil {
				return err
			}
			err = plain.PingContext(step)
			_ = plain.Close()
			var native *mysqlclient.MySQLError
			if err == nil || !errors.As(err, &native) || (native.Number != 1045 && native.Number != 3159) {
				return fmt.Errorf("MySQL Router public route did not prove plaintext refusal")
			}
			return nil
		})
	}
	return group.Wait()
}
