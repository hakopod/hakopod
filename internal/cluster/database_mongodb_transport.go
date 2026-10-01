package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type mongodbDialer struct {
	dial func(context.Context, string, string) (net.Conn, error)
}

func (d mongodbDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

func mongodbMemberHost(d database.Resource, m database.Member) string {
	return m.Name + ".database-svc." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
}

// Direct connections avoid following an untrusted server-advertised destination.
// Each new connection rechecks the member's namespace, controller, UID and image.
// Drivers used by applications instead discover all advertised replica members.
func (c *Client) mongodbClient(ctx context.Context, d database.Resource, m database.Member, monitor bool) (*mongo.Client, func(), error) {
	role := "app"
	if monitor {
		role = "monitor"
	}
	return c.mongodbClientRole(ctx, d, m, role)
}

func (c *Client) mongodbClientRole(ctx context.Context, d database.Resource, m database.Member, role string) (*mongo.Client, func(), error) {
	noop := func() {}
	if d.Spec.Engine != "mongodb" || !d.Spec.TLSRequired() {
		return nil, noop, fmt.Errorf("MongoDB native transport requires verified TLS")
	}
	if _, _, err := c.databaseExecTarget(ctx, d, m); err != nil {
		return nil, noop, err
	}
	name, username, authDB := "database-credentials", "app", "app"
	if role == "monitor" {
		name, username, authDB = "database-monitor", "hakopod-monitor", "admin"
	} else if role == "recovery" {
		name, username = "database-recovery", "hakopod-recovery"
	} else if role != "app" {
		return nil, noop, fmt.Errorf("MongoDB internal role is invalid")
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 {
		return nil, noop, fmt.Errorf("MongoDB native credentials are unavailable")
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return nil, noop, err
	}
	host := mongodbMemberHost(d, m)
	tlsConfig, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, noop, err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return nil, noop, err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, []string{host}, time.Now())
	if err != nil {
		return nil, noop, err
	}
	tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != identity.Fingerprint {
			return fmt.Errorf("MongoDB has not loaded its current issued certificate")
		}
		return nil
	}
	// This lifetime belongs to the client, not a driver's short-lived dial call.
	// Closing the client terminates all unbuffered exec streams and goroutines.
	lifetime, cancel := context.WithTimeout(ctx, 30*time.Minute)
	dialer := mongodbDialer{dial: func(dial context.Context, network, address string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || address != net.JoinHostPort(host, "27017") {
			return nil, fmt.Errorf("MongoDB requested an unexpected connection target")
		}
		return c.mongodbStream(dial, lifetime, d, m)
	}}
	client, err := mongo.Connect(options.Client().SetHosts([]string{net.JoinHostPort(host, "27017")}).SetDirect(true).SetReplicaSet("database").SetTLSConfig(tlsConfig).SetAuth(options.Credential{Username: username, Password: string(secret.Data["password"]), AuthSource: authDB, AuthMechanism: "SCRAM-SHA-256"}).SetDialer(dialer).SetMaxPoolSize(1).SetMinPoolSize(0).SetMaxConnecting(1).SetConnectTimeout(5 * time.Second).SetServerSelectionTimeout(8 * time.Second).SetHeartbeatInterval(10 * time.Second).SetTimeout(30 * time.Second).SetRetryReads(false).SetRetryWrites(false))
	if err != nil {
		cancel()
		return nil, noop, fmt.Errorf("MongoDB native client could not be configured")
	}
	closeClient := func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = client.Disconnect(cleanup)
	}
	if err = client.Ping(ctx, readpref.Nearest()); err != nil {
		closeClient()
		return nil, noop, fmt.Errorf("MongoDB native authentication or TLS verification failed")
	}
	return client, closeClient, nil
}

// Kubernetes port forwarding cannot enter runsc's userspace network stack.
// A fixed bash TCP bridge in the pinned server carries raw BSON over exec;
// the Go driver performs the end-to-end TLS handshake and SCRAM authentication.
func (c *Client) mongodbStream(ctx, lifetime context.Context, d database.Resource, m database.Member) (net.Conn, error) {
	pod, container, err := c.databaseExecTarget(ctx, d, m)
	if err != nil {
		return nil, err
	}
	if !mongodbPodImagesMatch(*pod, d) || container != "mongod" {
		return nil, fmt.Errorf("MongoDB transport container identity changed")
	}
	ctx, cancel := context.WithCancel(lifetime)
	script := `set -eu
exec 3<>/dev/tcp/127.0.0.1/27017
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("MongoDB native transport is unavailable")
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-ctx.Done(); closeStream() }()
	return &mongodbStreamConn{Conn: conn, close: closeStream}, nil
}

type mongodbStreamConn struct {
	net.Conn
	close func()
}

func (c *mongodbStreamConn) Close() error { c.close(); return nil }
