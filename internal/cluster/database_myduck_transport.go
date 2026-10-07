package cluster

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// The driver authenticates through a fixed loopback stream to the exact owned
// pod. TLS, credentials and query text never enter the helper command's argv.
func (c *Client) myduckStream(ctx context.Context, d database.Resource, member database.Member, port int) (net.Conn, error) {
	if d.Spec.Engine != "duckdb" || port != 3306 && port != 5432 {
		return nil, fmt.Errorf("invalid MyDuck protocol")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil {
		return nil, err
	}
	step, cancel := context.WithTimeout(ctx, 2*time.Minute)
	script := `set -eu
exec 3<>/dev/tcp/127.0.0.1/"$1"
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script, "myduck-loopback", strconv.Itoa(port)}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("MyDuck native transport is unavailable")
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

func (c *Client) myduckClientIdentity(ctx context.Context, d database.Resource) ([]byte, *tls.Config, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, nil, fmt.Errorf("MyDuck identity namespace changed")
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || databaseIdentityOwned(secret, d, ns.UID) != nil || len(secret.Data["password"]) != 64 {
		return nil, nil, fmt.Errorf("MyDuck credentials are unavailable")
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return nil, nil, err
	}
	host := "database." + ns.Name + ".svc"
	issued, err := database.VerifyServerCertificate(certificate, ca, []string{host}, time.Now())
	if err != nil {
		return nil, nil, err
	}
	config, err := myduckPublicEndpointTLSConfig(trust, host, issued.Fingerprint)
	return secret.Data["password"], config, err
}

func (c *Client) myduckMySQLClient(ctx context.Context, d database.Resource, member database.Member, password []byte, identity *tls.Config) (*sql.DB, error) {
	config := mysqlclient.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = "root", string(password), "tcp", "database."+DatabaseNamespace(d.ID)+".svc:3306", "app"
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 65*time.Second, 5*time.Second
	config.TLS = identity
	config.DialFunc = func(step context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != config.Addr || step.Err() != nil {
			return nil, fmt.Errorf("MyDuck requested an unexpected connection")
		}
		return c.myduckStream(ctx, d, member, 3306)
	}
	connector, err := mysqlclient.NewConnector(config)
	if err != nil {
		return nil, err
	}
	client := sql.OpenDB(connector)
	client.SetMaxOpenConns(1)
	client.SetMaxIdleConns(0)
	client.SetConnMaxLifetime(90 * time.Second)
	return client, nil
}

func (c *Client) myduckPostgresClient(ctx context.Context, d database.Resource, member database.Member, password []byte, identity *tls.Config) (*pgx.Conn, error) {
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	config, err := pgx.ParseConfig("host=" + host + " port=5432 user=postgres dbname=app connect_timeout=5")
	if err != nil {
		return nil, err
	}
	config.Password, config.TLSConfig, config.Fallbacks = string(password), identity, nil
	config.DialFunc = func(step context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || step.Err() != nil {
			return nil, fmt.Errorf("MyDuck requested an invalid connection")
		}
		return c.myduckStream(ctx, d, member, 5432)
	}
	// The connection target is fixed by the pod stream, including when pgx tries
	// to resolve a service name outside the cluster.
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	return pgx.ConnectConfig(ctx, config)
}

func (c *Client) myduckEngineMetrics(ctx context.Context, d database.Resource, observed database.Observation) (database.EngineMetrics, error) {
	if len(observed.Members) != 1 {
		return database.EngineMetrics{}, fmt.Errorf("MyDuck metrics require one current member")
	}
	password, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		return database.EngineMetrics{}, err
	}
	client, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		return database.EngineMetrics{}, err
	}
	defer client.Close(ctx)
	var used int64
	if err = client.QueryRow(ctx, "SELECT CAST(used_blocks * block_size AS BIGINT) FROM pragma_database_size() WHERE database_name = 'app'").Scan(&used); err != nil || used < 0 {
		return database.EngineMetrics{}, fmt.Errorf("MyDuck storage statistics are unavailable")
	}
	now := time.Now().UTC()
	// DuckDB reports its allocated persistent blocks. Protocol session counts,
	// replication and query counters are absent rather than invented zeros.
	return database.EngineMetrics{Available: true, SampledAt: &now, DataBytes: &used}, nil
}
