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
	"strings"
	"sync"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type vitessStreamConn struct {
	net.Conn
	close func()
}

func vitessPlaintextRefused(err error) bool {
	var native *mysqlclient.MySQLError
	if !errors.As(err, &native) {
		return false
	}
	if native.Number == 1045 || native.Number == 3159 {
		return true
	}
	// The pinned Vitess listener maps its secure-transport refusal to the
	// generic MySQL code. Require its exact message as well as that code.
	return native.Number == 1105 && native.Message == "unknown error: Code: UNAVAILABLE\nserver does not allow insecure connections, client must use SSL/TLS\n"
}

func (c *vitessStreamConn) Close() error { c.close(); return nil }

// A fixed localhost bridge supports runsc's userspace network. The Go driver
// performs MySQL TLS and authentication over this stream; exec sees no password.
func (c *Client) vitessGatewayStream(ctx context.Context, d database.Resource, m database.Member, lifetime time.Duration) (net.Conn, error) {
	pod, container, err := c.vitessExecTarget(ctx, d, m)
	if err != nil || container != "vtgate" {
		return nil, fmt.Errorf("Vitess gateway transport identity changed")
	}
	streamCtx, cancel := context.WithTimeout(ctx, lifetime)
	script := `set -eu
exec 3<>/dev/tcp/127.0.0.1/3306
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("Vitess native transport is unavailable")
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-streamCtx.Done(); closeStream() }()
	return &vitessStreamConn{Conn: conn, close: closeStream}, nil
}

func (c *Client) vitessGatewayClient(ctx context.Context, d database.Resource, m database.Member, target string, password []byte, config *tls.Config) (*sql.DB, error) {
	return c.vitessGatewayClientWithReadTimeout(ctx, d, m, target, password, config, 5*time.Second)
}

// Health probes keep their short deadline. Native application acceptance also
// uses this transport, but must allow the gateway's 30-second query budget.
func (c *Client) vitessGatewayClientWithReadTimeout(ctx context.Context, d database.Resource, m database.Member, target string, password []byte, config *tls.Config, readTimeout time.Duration) (*sql.DB, error) {
	if readTimeout <= 0 || readTimeout > 35*time.Second {
		return nil, fmt.Errorf("Vitess gateway read timeout is outside its bound")
	}
	if target != "app@primary" && target != "app@replica" {
		return nil, fmt.Errorf("Vitess route target is invalid")
	}
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	settings := mysqlclient.NewConfig()
	settings.User, settings.Passwd, settings.DBName = "app", string(password), target
	settings.Net, settings.Addr = "tcp", net.JoinHostPort(host, "3306")
	settings.TLS = config
	settings.Timeout, settings.ReadTimeout, settings.WriteTimeout = 5*time.Second, readTimeout, 5*time.Second
	settings.MaxAllowedPacket = 1 << 20
	settings.Logger = log.New(io.Discard, "", 0)
	settings.DialFunc = func(dial context.Context, network, address string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || address != settings.Addr {
			return nil, fmt.Errorf("Vitess requested an unexpected gateway")
		}
		return c.vitessGatewayStream(ctx, d, m, readTimeout+15*time.Second)
	}
	connector, err := mysqlclient.NewConnector(settings)
	if err != nil {
		return nil, fmt.Errorf("Vitess native client configuration failed")
	}
	client := sql.OpenDB(connector)
	client.SetMaxOpenConns(1)
	client.SetMaxIdleConns(0)
	client.SetConnMaxLifetime(15 * time.Second)
	return client, nil
}

func (c *Client) verifyVitessTLS(ctx context.Context, d database.Resource, o *database.Observation, trust database.PublicTrust, status *database.TLSObservation, inventory *vitessObservationInventory) error {
	if o.Routing == nil || len(o.Routing.Members) != d.Spec.VitessGateways() {
		return fmt.Errorf("Vitess gateway inventory is unavailable")
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 || strings.ContainsAny(string(secret.Data["password"]), "\r\n\x00") {
		return fmt.Errorf("Vitess gateway credentials are unavailable")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(2)
	for _, member := range o.Routing.Members {
		group.Go(func() error {
			config, err := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
			if err != nil {
				return err
			}
			config.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != status.Fingerprint {
					return fmt.Errorf("Vitess gateway has not loaded its current certificate")
				}
				return nil
			}
			client, err := c.vitessGatewayClient(step, d, member, "app@primary", secret.Data["password"], config)
			if err != nil {
				return err
			}
			if err = client.PingContext(step); err != nil {
				_ = client.Close()
				return fmt.Errorf("Vitess gateway TLS or authentication failed")
			}
			if err = c.verifyVitessGatewayTablets(step, client, d, o.Members, inventory); err != nil {
				_ = client.Close()
				return err
			}
			_ = client.Close()
			plain, err := c.vitessGatewayClient(step, d, member, "app@primary", secret.Data["password"], nil)
			if err != nil {
				return err
			}
			err = plain.PingContext(step)
			_ = plain.Close()
			if !vitessPlaintextRefused(err) {
				return fmt.Errorf("Vitess plaintext refusal could not be verified")
			}
			return nil
		})
	}
	return group.Wait()
}
