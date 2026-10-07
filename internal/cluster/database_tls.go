package cluster

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Read only public certificate material from the controller-owned identity.
// A request never supplies a Secret name or namespace.
func (c *Client) databaseCertificates(ctx context.Context, d database.Resource) (database.PublicTrust, []byte, error) {
	secretName := "database-tls"
	if d.Spec.Engine == "clickhouse" {
		secretName = clickhouseClientTLSSecret
	}
	return c.databaseCertificatesFromIdentity(ctx, d, secretName)
}

// Only engine-owned identity names reach this helper. It is never an API input.
func (c *Client) databaseCertificatesFromIdentity(ctx context.Context, d database.Resource, secretName string) (database.PublicTrust, []byte, error) {
	if secretName != "database-tls" && (d.Spec.Engine != "clickhouse" || secretName != clickhouseClientTLSSecret) {
		return database.PublicTrust{}, nil, fmt.Errorf("database identity is unsupported")
	}
	var empty database.PublicTrust
	if !d.Spec.TLSRequired() {
		return empty, nil, fmt.Errorf("verified database TLS is unavailable")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return empty, nil, fmt.Errorf("read database certificate namespace: %w", err)
	}
	if ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return empty, nil, fmt.Errorf("database certificate namespace ownership changed")
	}

	gvr, kind := databaseGVR(d.Spec)
	object, err := c.dynamic.Resource(gvr).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return empty, nil, fmt.Errorf("read database certificate controller: %w", err)
	}
	if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return empty, nil, fmt.Errorf("database certificate controller ownership changed")
	}
	if d.Spec.Engine == "redis" || d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb" || d.Spec.Engine == "clickhouse" || d.Spec.Engine == "oracle" || d.Spec.Engine == "vitess" {
		secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return empty, nil, fmt.Errorf("database certificate is unavailable")
		}
		if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
			return empty, nil, err
		}
		trust, _, err := database.ParsePublicTrust(secret.Data["ca.crt"], time.Now())
		if len(secret.Data[corev1.TLSCertKey]) > 32<<10 {
			return empty, nil, fmt.Errorf("database certificate exceeds its size limit")
		}
		return trust, secret.Data[corev1.TLSCertKey], err
	}
	read := func(field, key string) ([]byte, error) {
		name, _, _ := unstructured.NestedString(object.Object, "status", "certificates", field)
		if name == "" {
			return nil, fmt.Errorf("database certificate has not been issued")
		}
		secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err != nil || secret.DeletionTimestamp != nil {
			return nil, fmt.Errorf("database certificate is unavailable")
		}
		owned := false
		for _, owner := range secret.OwnerReferences {
			owned = owned || owner.UID == object.GetUID() && owner.Kind == kind && owner.APIVersion == gvr.Group+"/"+gvr.Version
		}
		if !owned || len(secret.Data[key]) == 0 || len(secret.Data[key]) > 32<<10 {
			return nil, fmt.Errorf("database certificate ownership or size is invalid")
		}
		return secret.Data[key], nil
	}
	ca, err := read("serverCASecret", "ca.crt")
	if err != nil {
		return empty, nil, err
	}
	trust, _, err := database.ParsePublicTrust(ca, time.Now())
	if err != nil {
		return empty, nil, err
	}
	server, err := read("serverTLSSecret", corev1.TLSCertKey)
	return trust, server, err
}

func (c *Client) DatabaseTrust(ctx context.Context, d database.Resource) (database.PublicTrust, error) {
	trust, _, err := c.databaseCertificates(ctx, d)
	return trust, err
}

// The password and CA travel over the authenticated exec stream, never argv.
// Check a verified client connection, an explicit plaintext rejection, then the
// certificate actually served by the endpoint. Configuration alone is not proof.
const postgresTLSProbe = `set -eu
export LC_ALL=C PGCONNECT_TIMEOUT=3
IFS= read -r PGPASSWORD
export PGPASSWORD
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
export PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt"
minimum=$(psql -XAtw -U postgres -d postgres -v ON_ERROR_STOP=1 -c "SHOW ssl_min_protocol_version")
[ "$minimum" = 'TLSv1.2' ]
result=$(psql -XAtw -h "$1" -p "$2" -U app -d app -v ON_ERROR_STOP=1 -c "SELECT ssl::text || '|' || version || '|' || pg_is_in_recovery() FROM pg_stat_ssl WHERE pid=pg_backend_pid()")
case "$3:$result" in 'read_write:true|TLSv1.2|false'|'read_write:true|TLSv1.3|false'|'pooled_read_write:true|TLSv1.2|false'|'pooled_read_write:true|TLSv1.3|false'|'read_only:true|TLSv1.2|true'|'read_only:true|TLSv1.3|true'|'pooled_read_only:true|TLSv1.2|true'|'pooled_read_only:true|TLSv1.3|true') ;; *) exit 1;; esac
if PGSSLMODE=disable psql -XAtw -h "$1" -p "$2" -U app -d app -c 'SELECT 1' > /dev/null 2> "$work/plaintext-error"; then exit 1; fi
if [ "$3" = 'pooled_read_write' ] || [ "$3" = 'pooled_read_only' ]; then
  grep -E 'SSL required|TLS required|no pg_hba.conf entry|pg_hba.conf rejects connection' "$work/plaintext-error" >/dev/null
else
  grep -F 'pg_hba.conf rejects connection' "$work/plaintext-error" > /dev/null
  grep -F 'no encryption' "$work/plaintext-error" > /dev/null
fi
openssl s_client -starttls postgres -connect "$1:$2" -servername "$1" -verify_hostname "$1" -verify_return_error -CAfile "$work/ca.crt" -showcerts < /dev/null 2>/dev/null
`

func (c *Client) observeDatabaseTLS(ctx context.Context, d database.Resource, o *database.Observation, vitessInventory *vitessObservationInventory) error {
	o.TLS = &database.TLSObservation{Required: d.Spec.TLSRequired()}
	if !d.Spec.TLSRequired() {
		o.TLS.Message = "Legacy database: enforced client TLS has not been configured. Migrate to a secure database before production use."
		return nil
	}
	step, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	trust, certificate, err := c.databaseCertificates(step, d)
	if err != nil {
		o.TLS.Message = err.Error()
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(o.Endpoints))
	for _, endpoint := range o.Endpoints {
		hosts = append(hosts, endpoint.Host)
	}
	status, err := database.VerifyServerCertificate(certificate, ca, hosts, time.Now())
	o.TLS = &status
	if err != nil {
		status.Message = err.Error()
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(step, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || bytes.ContainsAny(secret.Data["password"], "\r\n") || len(secret.Data["password"]) < 32 {
		return fmt.Errorf("database TLS probe credentials are unavailable")
	}
	if len(o.Members) == 0 {
		return fmt.Errorf("database TLS probe has no owned member")
	}
	if d.Spec.Engine == "vitess" {
		if err = c.verifyVitessTLS(step, d, o, trust, &status, vitessInventory); err != nil {
			status.Message = "Vitess did not pass verified TLS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	if d.Spec.Engine == "oracle" {
		if err = c.verifyOracleTLS(step, d, o); err != nil {
			status.Message = "Oracle did not pass verified TCPS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	if d.Spec.Engine == "mongodb" {
		if err = c.verifyMongoDBTLS(step, d, o); err != nil {
			status.Message = "MongoDB did not pass verified TLS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	if d.Spec.Engine == "clickhouse" {
		if err = c.verifyClickHouseTLS(step, d, o); err != nil {
			status.Message = "ClickHouse did not pass verified TLS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	if d.Spec.Engine == "mysql" {
		if err = c.verifyMySQLTLS(step, d, o, trust, &status); err != nil {
			status.Message = "MySQL did not pass verified TLS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	if d.Spec.Engine == "redis" {
		if err = c.verifyRedisTLS(step, d, o, trust, &status); err != nil {
			status.Message = "The Redis endpoint did not pass verified TLS and plaintext rejection checks."
			return err
		}
		stamp := time.Now().UTC()
		status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
		return nil
	}
	for _, endpoint := range o.Endpoints {
		output := &databaseBoundedWriter{limit: 32 << 10}
		input := bytes.Join([][]byte{secret.Data["password"], []byte(trust.CertificatePEM)}, []byte{'\n'})
		if err = c.DatabaseExec(step, d, o.Members[0], []string{"sh", "-c", postgresTLSProbe, "verify-database-tls", endpoint.Host, strconv.Itoa(endpoint.Port), endpoint.Purpose}, bytes.NewReader(input), output); err != nil {
			status.Message = "The endpoint did not pass verified TLS and plaintext rejection checks."
			return err
		}
		block, _ := pem.Decode(output.Bytes())
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("the active database certificate could not be read")
		}
		active, err := database.VerifyServerCertificate(pem.EncodeToMemory(block), ca, []string{endpoint.Host}, time.Now())
		if err != nil || !strings.EqualFold(active.Fingerprint, status.Fingerprint) {
			return fmt.Errorf("the active database certificate differs from its issued certificate")
		}
	}
	stamp := time.Now().UTC()
	status.Verified, status.PlaintextRejected, status.CheckedAt = true, true, &stamp
	return nil
}
