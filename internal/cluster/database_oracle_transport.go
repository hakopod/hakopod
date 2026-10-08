package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	goora "github.com/sijms/go-ora/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type oracleDialer func(context.Context, string, string) (net.Conn, error)

func (d oracleDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

func oracleHost(d database.Resource) string {
	if oracleEnterprise(d.Spec) {
		return "database-rw." + DatabaseNamespace(d.ID) + ".svc"
	}
	return "database." + DatabaseNamespace(d.ID) + ".svc"
}

// A native Oracle client talks through a bounded stream to the owned member.
// Listener redirects cannot open a connection to another host or namespace.
func (c *Client) oracleStream(ctx context.Context, d database.Resource, m database.Member) (net.Conn, error) {
	if d.Spec.Engine != "oracle" {
		return nil, fmt.Errorf("invalid Oracle transport target")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, m)
	if err != nil {
		return nil, err
	}
	step, cancel := context.WithCancel(ctx)
	script := `set -eu
exec 3<>/dev/tcp/127.0.0.1/2484
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("Oracle transport is unavailable")
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(step, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-step.Done(); closeStream() }()
	return &mongodbStreamConn{Conn: conn, close: closeStream}, nil
}

func (c *Client) oracleTLSConfig(ctx context.Context, d database.Resource) (*tls.Config, error) {
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return nil, err
	}
	config, err := redisTLSConfig(trust, oracleHost(d))
	if err != nil {
		return nil, err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return nil, err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, []string{oracleHost(d)}, time.Now())
	if err != nil {
		return nil, err
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != identity.Fingerprint {
			return fmt.Errorf("Oracle has not loaded its issued identity")
		}
		return nil
	}
	return config, nil
}

func (c *Client) oracleApplicationConnection(ctx context.Context, d database.Resource, m database.Member, secure bool) (*sql.DB, error) {
	return c.oracleApplicationConnectionOptions(ctx, d, m, secure, false)
}

func (c *Client) oracleApplicationConnectionOptions(ctx context.Context, d database.Resource, m database.Member, secure, query bool) (*sql.DB, error) {
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 {
		return nil, fmt.Errorf("Oracle application credentials are unavailable")
	}
	// Negotiate each physical connection. Replaying a cached fast-login cookie
	// is not reliable after an Oracle 26ai listener restart or identity renewal.
	options := map[string]string{"CONNECTION TIMEOUT": "8", "TIMEOUT": "10", "SSL VERIFY": "true", "FAST LOGIN": "false"}
	if query {
		options["PREFETCH_ROWS"] = "1"
	}
	if secure {
		options["SSL"] = "enable"
	}
	connector := goora.NewConnector(goora.BuildUrl(oracleHost(d), 2484, oraclePDB(d), "APP", string(secret.Data["password"]), options)).(*goora.OracleConnector)
	if secure {
		config, err := c.oracleTLSConfig(ctx, d)
		if err != nil {
			return nil, err
		}
		connector.WithTLSConfig(config)
	}
	connector.Dialer(oracleDialer(func(dial context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(oracleHost(d), "2484") {
			return nil, fmt.Errorf("Oracle requested an unexpected endpoint")
		}
		return c.oracleStream(ctx, d, m)
	}))
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(30 * time.Second)
	return db, nil
}

func (c *Client) verifyOracleTLS(ctx context.Context, d database.Resource, o *database.Observation) error {
	if oracleEnterprise(d.Spec) {
		return c.verifyOracleEnterpriseTLS(ctx, d, o)
	}
	if len(o.Members) != 1 {
		return fmt.Errorf("Oracle has no unique owned member")
	}
	config, err := c.oracleTLSConfig(ctx, d)
	if err != nil {
		return err
	}
	raw, err := c.oracleStream(ctx, d, o.Members[0])
	if err != nil {
		return err
	}
	conn := tls.Client(raw, config)
	err = conn.HandshakeContext(ctx)
	_ = conn.Close()
	if err != nil {
		return fmt.Errorf("Oracle TCPS identity could not be verified")
	}
	// Refusal is checked after a successful authenticated TLS connection in
	// observeOracleDatabase, so an offline instance cannot satisfy this check.
	step, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	plain, err := c.oracleApplicationConnection(step, d, o.Members[0], false)
	if err != nil {
		return err
	}
	defer plain.Close()
	if plain.PingContext(step) == nil {
		return fmt.Errorf("Oracle accepted a plaintext connection")
	}
	command := []string{"bash", "-c", `awk 'FNR>1 && $4=="0A" && $2 ~ /:(05F1|157C)$/ {bad=1} END {exit bad}' /proc/net/tcp /proc/net/tcp6`}
	if err = c.DatabaseExec(ctx, d, o.Members[0], command, nil, nil); err != nil {
		return fmt.Errorf("Oracle plaintext listener is still exposed")
	}
	return nil
}
