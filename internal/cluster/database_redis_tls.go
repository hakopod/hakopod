package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The local socket is accessible only to the database container's UID. It lets
// the controller renew even an expired server certificate without ever opening
// a plaintext TCP listener or disabling client certificate verification.
const redisSecurityConfig = "tls-protocols \"TLSv1.2 TLSv1.3\"\nunixsocket /tmp/hakopod-redis.sock\nunixsocketperm 700\npidfile /tmp/hakopod-redis.pid\n"

func (c *Client) prepareRedisSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "redis" || !d.Spec.TLSRequired() {
		return nil
	}
	api := c.kube.CoreV1().ConfigMaps(DatabaseNamespace(d.ID))
	existing, err := api.Get(ctx, "database-security", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "database-security", Namespace: DatabaseNamespace(d.ID), Labels: databaseLabels(d)}, Immutable: ptr(true), Data: map[string]string{"redis-additional.conf": redisSecurityConfig}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.DeletionTimestamp != nil || existing.Labels[databaseOwner] != d.ID || existing.Labels[managedBy] != "hakopod" || existing.Immutable == nil || !*existing.Immutable || !reflect.DeepEqual(existing.Data, map[string]string{"redis-additional.conf": redisSecurityConfig}) {
		return fmt.Errorf("Redis security configuration ownership or content changed")
	}
	return nil
}

func redisMemberHostname(d database.Resource, m database.Member) string {
	service := "database-headless"
	if strings.HasPrefix(m.Name, "database-leader-") {
		service = "database-leader-headless"
	}
	if strings.HasPrefix(m.Name, "database-follower-") {
		service = "database-follower-headless"
	}
	return m.Name + "." + service + "." + DatabaseNamespace(d.ID) + ".svc"
}

func redisTLSConfig(trust database.PublicTrust, host string) (*tls.Config, error) {
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: roots}, nil
}

func (c *Client) databaseRedisTLSStream(ctx context.Context, d database.Resource, m database.Member, host string, trust database.PublicTrust) (*tls.Conn, func(), error) {
	noop := func() {}
	// Recheck the complete ownership chain immediately before opening transport.
	if _, _, err := c.databaseExecTarget(ctx, d, m); err != nil {
		return nil, noop, err
	}
	config, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, noop, err
	}
	raw, closeStream, err := c.databaseRedisStream(ctx, DatabaseNamespace(d.ID), m.Name, types.UID(m.UID), host)
	if err != nil {
		return nil, closeStream, err
	}
	conn := tls.Client(raw, config)
	handshake, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err = conn.HandshakeContext(handshake); err != nil {
		closeStream()
		return nil, noop, fmt.Errorf("Redis TLS certificate or endpoint verification failed: %w", err)
	}
	return conn, closeStream, nil
}

func (c *Client) databaseRedisMemberConnection(ctx context.Context, d database.Resource, m database.Member) (*database.RedisWire, func(), error) {
	if !d.Spec.TLSRequired() {
		return c.databaseRedisConnection(ctx, DatabaseNamespace(d.ID), m.Name, types.UID(m.UID))
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return nil, func() {}, err
	}
	conn, closeStream, err := c.databaseRedisTLSStream(ctx, d, m, redisMemberHostname(d, m), trust)
	if err != nil {
		return nil, closeStream, err
	}
	return database.NewRedisWire(conn), closeStream, nil
}

func redisConfigValue(wire *database.RedisWire, key string) (string, error) {
	result, err := wire.Command([]byte("CONFIG"), []byte("GET"), []byte(key))
	if err != nil {
		return "", err
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return "", fmt.Errorf("Redis security setting was not returned")
	}
	name, nameOK := values[0].([]byte)
	value, valueOK := values[1].([]byte)
	if !nameOK || !valueOK || string(name) != key {
		return "", fmt.Errorf("Redis security setting is invalid")
	}
	return string(value), nil
}

func (c *Client) verifyRedisTLS(ctx context.Context, d database.Resource, o *database.Observation, trust database.PublicTrust, status *database.TLSObservation) error {
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Redis TLS probe credentials are unavailable")
	}
	// Check each member, not just whichever pod a Service happened to choose.
	targets := make([]struct {
		member database.Member
		host   string
	}, 0, len(o.Members)+len(o.Endpoints))
	for _, member := range o.Members {
		targets = append(targets, struct {
			member database.Member
			host   string
		}{member, redisMemberHostname(d, member)})
	}
	for _, endpoint := range o.Endpoints {
		targets = append(targets, struct {
			member database.Member
			host   string
		}{o.Members[0], endpoint.Host})
	}
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, target := range targets {
		group.Go(func() error {
			conn, closeStream, err := c.databaseRedisTLSStream(ctx, d, target.member, target.host, trust)
			if err != nil {
				return err
			}
			state := conn.ConnectionState()
			hash := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(hash[:]) != status.Fingerprint {
				closeStream()
				return fmt.Errorf("Redis is still serving a previous certificate")
			}
			wire := database.NewRedisWire(conn)
			if _, err = wire.Command([]byte("AUTH"), secret.Data["password"]); err == nil {
				for _, setting := range []struct{ key, value string }{{"port", "0"}, {"tls-port", "6379"}, {"tls-protocols", "TLSv1.2 TLSv1.3"}} {
					value, e := redisConfigValue(wire, setting.key)
					if e != nil || value != setting.value {
						err = fmt.Errorf("Redis network TLS policy differs from the required policy")
						break
					}
				}
			}
			closeStream()
			if err != nil {
				return err
			}
			// The native client reports a TCP rejection explicitly. A timeout, DNS
			// failure or refused port cannot pass this credential-free negative probe.
			output := &databaseBoundedWriter{limit: 1024}
			probe := `set -eu
export LC_ALL=C
if response=$(redis-cli -h "$1" -p 6379 PING 2>&1); then exit 1; fi
case "$response" in *"Server closed the connection"*|*"Connection reset by peer"*) printf 'plaintext-rejected';; *) exit 1;; esac`
			check, stop := context.WithTimeout(ctx, 3*time.Second)
			err = c.DatabaseExec(check, d, target.member, []string{"sh", "-c", probe, "plaintext-probe", target.host}, nil, output)
			stop()
			if err != nil || output.String() != "plaintext-rejected" {
				return fmt.Errorf("Redis plaintext rejection was not observed")
			}

			return nil
		})
	}
	return group.Wait()
}

// RenewDatabaseIdentity is called under a durable database maintenance claim.
// Each member is fenced and checked again before applying its projected identity.
func (c *Client) RenewDatabaseIdentity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine == "duckdb" && d.Spec.TLSRequired() {
		if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
			return err
		}
		return c.prepareMyDuckSecurity(ctx, d, before)
	}
	if d.Spec.Engine == "vitess" && d.Spec.TLSRequired() {
		return c.renewVitessIdentity(ctx, d, before)
	}
	if d.Spec.Engine == "oracle" && d.Spec.TLSRequired() {
		if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
			return err
		}
		return c.prepareOracleSecurity(ctx, d, before)
	}
	if d.Spec.Engine == "clickhouse" && d.Spec.TLSRequired() {
		return c.renewClickHouseIdentity(ctx, d, before)
	}
	if d.Spec.Engine == "mongodb" && d.Spec.TLSRequired() {
		if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
			return err
		}
		return c.prepareMongoDBSecurity(ctx, d, before)
	}
	if d.Spec.Engine == "mysql" && d.Spec.TLSRequired() {
		// The MySQL operator watches both secret projections, reloads server
		// contexts and rolls routers. Observation verifies the served certificate.
		return c.prepareDatabaseIdentity(ctx, d, before)
	}
	if d.Spec.Engine != "redis" || !d.Spec.TLSRequired() {
		return nil
	}
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 1, LabelSelector: "!" + databaseRecoveryHelper})
	if err != nil {
		return err
	}
	if len(pods.Items) > database.MaxMembers || pods.Continue != "" {
		return fmt.Errorf("Redis renewal members exceed the limit")
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || bytes.ContainsAny(secret.Data["password"], "\r\n") {
		return fmt.Errorf("Redis renewal credentials are unavailable")
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		member := database.Member{Name: pod.Name, UID: string(pod.UID)}
		// Secret projection can lag. Read only public material and wait for the
		// exact issued identity instead of loading a partial or previous key pair.
		projected := &databaseBoundedWriter{limit: 64 << 10}
		if err = c.DatabaseExec(ctx, d, member, []string{"sh", "-c", "cat /tls/tls.crt /tls/ca.crt"}, nil, projected); err != nil {
			return err
		}
		expected := append(append([]byte(nil), certificate...), []byte(trust.CertificatePEM)...)
		if !bytes.Equal(projected.Bytes(), expected) {
			continue
		}
		conn, closeStream, handshakeErr := c.databaseRedisTLSStream(ctx, d, member, redisMemberHostname(d, member), trust)
		if handshakeErr == nil {
			active := conn.ConnectionState().PeerCertificates[0]
			hash := sha256.Sum256(active.Raw)
			_, ca, _ := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
			issued, e := database.VerifyServerCertificate(certificate, ca, []string{redisMemberHostname(d, member)}, time.Now())
			closeStream()
			if e == nil && hex.EncodeToString(hash[:]) == issued.Fingerprint {
				continue
			}
		}
		if err = before(); err != nil {
			return err
		}
		input := append(append([]byte(nil), secret.Data["password"]...), '\n')
		output := &databaseBoundedWriter{limit: 1024}
		script := `set -eu; IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --raw -s /tmp/hakopod-redis.sock CONFIG SET tls-cert-file /tls/tls.crt tls-key-file /tls/tls.key tls-ca-cert-file /tls/ca.crt`
		if err = c.DatabaseExec(ctx, d, member, []string{"sh", "-c", script}, bytes.NewReader(input), output); err != nil {
			return err
		}
		if strings.TrimSpace(output.String()) != "OK" {
			return fmt.Errorf("Redis certificate reload was not acknowledged")
		}
	}
	return nil
}

func (c *Client) captureRedisTLSSnapshot(ctx context.Context, d database.Resource, m database.Member, password []byte, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wire, closeStream, err := c.databaseRedisMemberConnection(ctx, d, m)
	if err != nil {
		return err
	}
	defer closeStream()
	if _, err = wire.Command([]byte("AUTH"), password); err != nil {
		return err
	}
	input, output := io.Pipe()
	defer input.Close()
	done := make(chan error, 1)
	go func() {
		err := wire.DownloadSnapshot(ctx, output, d.Spec.StorageGiB<<30)
		_ = output.CloseWithError(err)
		done <- err
	}()
	// The official checker validates the complete file before any bytes are sent
	// to the archive writer. The private staging file is removed on every exit.
	script := `set -eu
umask 077
directory=$(mktemp -d /tmp/hakopod-snapshot.XXXXXXXX)
trap 'rm -rf "$directory"' EXIT HUP INT TERM
cat > "$directory/snapshot.rdb"
ln -s "$(command -v redis-server)" "$directory/redis-check-rdb"
"$directory/redis-check-rdb" "$directory/snapshot.rdb" >/dev/null
cat "$directory/snapshot.rdb"`
	err = c.DatabaseExec(ctx, d, m, []string{"sh", "-c", script}, input, out)
	cancel()
	_ = input.Close()
	downloadErr := <-done
	if err != nil {
		return err
	}
	return downloadErr
}
