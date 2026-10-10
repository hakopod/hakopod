package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReconcileDatabasePublicEndpointAccess updates only the database certificate
// names and ingress network-policy peer. The public TCP frontend remains closed
// until both the issued leaf and the selected backend pass a native TLS probe.
func (c *Client) ReconcileDatabasePublicEndpointAccess(ctx context.Context, d database.Resource, names []string, allowIngress bool, before func() error) error {
	if (d.Spec.Engine != "postgresql" && d.Spec.Engine != "mysql" && d.Spec.Engine != "mongodb" && d.Spec.Engine != "clickhouse" && d.Spec.Engine != "oracle" && d.Spec.Engine != "redis" && d.Spec.Engine != "vitess" && d.Spec.Engine != "duckdb") || !d.Spec.TLSRequired() {
		return fmt.Errorf("database public endpoint identity requires a supported TLS database")
	}
	validated, err := database.NormalizePublicEndpointNames(names)
	if err != nil {
		return err
	}
	d.PublicEndpointNames = validated
	d.PublicEndpointAccess = allowIngress
	if d.Spec.Engine == "duckdb" {
		err = c.reconcileMyDuckPublicNames(ctx, d, before)
	} else if d.Spec.Engine == "vitess" {
		err = c.reconcileVitessPublicNames(ctx, d, before)
	} else if d.Spec.Engine == "redis" {
		err = c.reconcileRedisPublicNames(ctx, d, before)
	} else if d.Spec.Engine == "mongodb" {
		err = c.reconcileMongoDBPublicNames(ctx, d, before)
	} else if d.Spec.Engine == "oracle" {
		err = c.reconcileOraclePublicNames(ctx, d, before)
	} else if d.Spec.Engine == "clickhouse" {
		err = c.reconcileClickHousePublicNames(ctx, d, before)
	} else if d.Spec.Engine == "mysql" {
		err = c.reconcileMySQLPublicNames(ctx, d, before)
	} else {
		err = c.reconcilePostgresPublicNames(ctx, d, before)
	}
	if err != nil {
		return err
	}
	return c.databaseNetworkPolicy(ctx, d, before)
}

func (c *Client) reconcilePostgresPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database certificate namespace ownership changed")
	}
	api := c.dynamic.Resource(pgDatabaseResource).Namespace(ns.Name)
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return fmt.Errorf("database certificate controller ownership changed")
	}
	if object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("database certificate controller revision changed")
	}
	desired := databasePostgresIdentityNames(d)
	current, _, _ := unstructured.NestedSlice(object.Object, "spec", "certificates", "serverAltDNSNames")
	if !reflect.DeepEqual(current, desired) {
		updated := object.DeepCopy()
		if err = unstructured.SetNestedSlice(updated.Object, desired, "spec", "certificates", "serverAltDNSNames"); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		if _, err = api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	hosts := append([]string(nil), d.PublicEndpointNames...)
	if len(hosts) == 0 {
		return nil
	}
	if _, err = database.VerifyServerCertificate(certificate, ca, hosts, time.Now()); err != nil {
		return fmt.Errorf("database public certificate names are pending: %w", err)
	}
	return nil
}

const postgresPublicEndpointTLSProbe = `set -eu
export LC_ALL=C PGCONNECT_TIMEOUT=3
IFS= read -r PGPASSWORD
export PGPASSWORD
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
export PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt" PGHOST="$1" PGHOSTADDR="$2" PGPORT=5432 PGUSER="$4" PGDATABASE="$5"
result=$(psql -XAtw -v ON_ERROR_STOP=1 -c "SELECT ssl::text || '|' || version || '|' || pg_is_in_recovery() FROM pg_stat_ssl WHERE pid=pg_backend_pid()")
case "$3:$result" in 'read_write:true|TLSv1.2|false'|'read_write:true|TLSv1.3|false'|'pooled_read_write:true|TLSv1.2|false'|'pooled_read_write:true|TLSv1.3|false'|'read_only:true|TLSv1.2|true'|'read_only:true|TLSv1.3|true'|'pooled_read_only:true|TLSv1.2|true'|'pooled_read_only:true|TLSv1.3|true') ;; *) exit 1;; esac
if PGSSLMODE=disable psql -XAtw -h "$2" -p 5432 -U "$4" -d "$5" -c 'SELECT 1' >/dev/null 2> "$work/plaintext-error"; then exit 1; fi
openssl s_client -starttls postgres -connect "$2:5432" -servername "$1" -verify_hostname "$1" -verify_return_error -CAfile "$work/ca.crt" < /dev/null >/dev/null 2>&1
`

func databasePublicEndpointBackendServiceAddress(service *corev1.Service, d database.Resource, route database.PublicEndpointRoute) (string, error) {
	if service == nil || service.DeletionTimestamp != nil || service.Name != route.BackendService || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 {
		return "", fmt.Errorf("database public endpoint backend service ownership changed")
	}
	// MySQL and ClickHouse operator Services are checked against their exact
	// controller owner, selector and target port before reaching this helper.
	if d.Spec.Engine != "mysql" && d.Spec.Engine != "clickhouse" && (service.Labels[databaseOwner] != d.ID || service.Labels[managedBy] != "hakopod") {
		return "", fmt.Errorf("database public endpoint backend service ownership changed")
	}
	address, err := netip.ParseAddr(service.Spec.ClusterIP)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() || address.IsLoopback() {
		return "", fmt.Errorf("database public endpoint backend service has no usable IPv4 address")
	}
	for _, port := range service.Spec.Ports {
		if port.Protocol == corev1.ProtocolTCP && port.Port == int32(route.BackendPort) && port.Name == route.BackendPortName {
			return address.String(), nil
		}
	}
	return "", fmt.Errorf("database public endpoint backend service does not expose the reviewed protocol port")
}

// VerifyDatabasePublicEndpointBackend checks the certificate actually served
// by the selected engine route, preserving its native wire protocol.
func (c *Client) VerifyDatabasePublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	if err := database.PublicEndpointAvailability(d.Spec); err != nil {
		return err
	}
	if d.Spec.Engine == "vitess" {
		return c.verifyVitessPublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "duckdb" {
		return c.verifyMyDuckPublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "mysql" {
		return c.verifyMySQLPublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "redis" {
		return c.verifyRedisPublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "clickhouse" {
		return c.verifyClickHousePublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "oracle" {
		return c.verifyOraclePublicEndpointBackend(ctx, d, endpoint)
	}
	if d.Spec.Engine == "mongodb" {
		return c.verifyMongoDBPublicEndpointBackend(ctx, d, endpoint)
	}
	if len(d.Observation.Members) == 0 {
		return fmt.Errorf("database public endpoint TLS probe has no owned member")
	}
	backend, err := databasePublicEndpointBackend(d, endpoint.Spec.Purpose)
	if err != nil {
		return err
	}
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, backend.BackendService, metav1.GetOptions{})
	if err != nil {
		return err
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, backend)
	if err != nil {
		return err
	}
	trust, _, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || bytes.ContainsAny(secret.Data["password"], "\r\n") {
		return fmt.Errorf("database public endpoint TLS probe credentials are unavailable")
	}
	input := bytes.Join([][]byte{secret.Data["password"], []byte(trust.CertificatePEM)}, []byte{'\n'})
	output := &databaseBoundedWriter{limit: 1024}
	if err = c.DatabaseExec(ctx, d, d.Observation.Members[0], []string{"sh", "-c", postgresPublicEndpointTLSProbe, "verify-public-database-tls", endpoint.Allocation.Host, address, endpoint.Spec.Purpose, d.Spec.CredentialUsername(), d.Spec.LogicalDatabase()}, bytes.NewReader(input), output); err != nil {
		return fmt.Errorf("database public endpoint did not pass native TLS, hostname and plaintext-refusal checks")
	}
	return nil
}
