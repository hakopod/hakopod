package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func clickhouseMemberHost(d database.Resource, m database.Member) string {
	return strings.TrimSuffix(m.Name, "-0") + "." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
}

func (c *Client) clickhouseStream(ctx context.Context, d database.Resource, m database.Member, port int, keeper bool) (net.Conn, error) {
	if d.Spec.Engine != "clickhouse" || (keeper && port != 9281) || (!keeper && port != 8443 && port != 9440) {
		return nil, fmt.Errorf("invalid ClickHouse transport target")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, m)
	if keeper {
		ns, e := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
		if e != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
			return nil, fmt.Errorf("Keeper namespace ownership changed")
		}
		set, e := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, "database-keeper", metav1.GetOptions{})
		if e != nil || !mongodbSupportOwned(set, d, ns.UID) {
			return nil, fmt.Errorf("Keeper workload ownership changed")
		}
		pod, err = c.kube.CoreV1().Pods(ns.Name).Get(ctx, m.Name, metav1.GetOptions{})
		container = "keeper"
		owned := false
		if err == nil {
			for _, o := range pod.OwnerReferences {
				owned = owned || (o.UID == set.UID && o.Name == set.Name && o.Kind == "StatefulSet")
			}
		}
		if err != nil || !owned || pod.UID != types.UID(m.UID) || pod.DeletionTimestamp != nil || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != clickhouseKeeperImage {
			return nil, fmt.Errorf("Keeper transport identity changed")
		}
	}
	if err != nil {
		return nil, err
	}
	if c.execConfig == nil || c.restClient() == nil {
		return nil, fmt.Errorf("ClickHouse transport is unavailable")
	}
	script := `set -eu
exec 3<>/dev/tcp/127.0.0.1/` + strconv.Itoa(port) + `
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`
	step, cancel := context.WithCancel(ctx)
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: []string{"bash", "-c", script}, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ClickHouse transport is unavailable")
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

func (c *Client) clickhouseTLSConfig(ctx context.Context, d database.Resource, host string, keeper bool) (*tls.Config, error) {
	identityName := clickhouseClientTLSSecret
	if keeper {
		identityName = "database-tls"
	}
	trust, certificate, err := c.databaseCertificatesFromIdentity(ctx, d, identityName)
	if err != nil {
		return nil, err
	}
	config, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return nil, err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, []string{host}, time.Now())
	if err != nil {
		return nil, err
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != identity.Fingerprint {
			return fmt.Errorf("ClickHouse has not loaded its issued certificate")
		}
		return nil
	}
	if keeper {
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-tls", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
		if err != nil {
			return nil, fmt.Errorf("Keeper client certificate is invalid")
		}
		config.Certificates = []tls.Certificate{pair}
	}
	return config, nil
}

func (c *Client) clickhouseQuery(ctx context.Context, d database.Resource, m database.Member, role, query string) (string, error) {
	username, password := "", ""
	if role == "app" {
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
			return "", fmt.Errorf("ClickHouse credentials are unavailable")
		}
		username, password = "app", string(secret.Data["password"])
	} else if role == "monitor" || role == "recovery" || role == "bootstrap" {
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-clickhouse-access", metav1.GetOptions{})
		if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
			return "", fmt.Errorf("ClickHouse internal credentials are unavailable")
		}
		var credentials struct {
			User     string `xml:"user"`
			Password string `xml:"password"`
		}
		if xml.Unmarshal(secret.Data[role+".xml"], &credentials) != nil {
			return "", fmt.Errorf("ClickHouse internal credentials are invalid")
		}
		username, password = credentials.User, credentials.Password
	} else {
		return "", fmt.Errorf("ClickHouse internal role is invalid")
	}
	if len(password) < 32 || len(password) > 128 {
		return "", fmt.Errorf("ClickHouse credentials are invalid")
	}
	host := clickhouseMemberHost(d, m)
	config, err := c.clickhouseTLSConfig(ctx, d, host, false)
	if err != nil {
		return "", err
	}
	transport := &http.Transport{TLSClientConfig: config, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DialContext: func(dial context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(host, "8443") {
			return nil, fmt.Errorf("ClickHouse requested an unexpected host")
		}
		return c.clickhouseStream(ctx, d, m, 8443, false)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(host, "8443")+"/?wait_end_of_query=1&max_result_bytes=262144&result_overflow_mode=throw", strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("ClickHouse query could not be created")
	}
	request.SetBasicAuth(username, password)
	response, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return "", fmt.Errorf("ClickHouse native authentication or TLS request failed")
	}
	defer response.Body.Close()
	output, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if response.StatusCode != http.StatusOK {
		code, _ := strconv.Atoi(response.Header.Get("X-ClickHouse-Exception-Code"))
		return "", fmt.Errorf("ClickHouse native query failed (HTTP %d, engine code %d)", response.StatusCode, code)
	}
	if err != nil || len(output) > 256<<10 {
		return "", fmt.Errorf("ClickHouse native query failed or exceeded its response limit")
	}
	return string(output), nil
}

func (c *Client) verifyClickHouseTLS(ctx context.Context, d database.Resource, o *database.Observation) error {
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, m := range o.Members {
		group.Go(func() error {
			step, stop := context.WithTimeout(ctx, 8*time.Second)
			defer stop()
			host := clickhouseMemberHost(d, m)
			config, err := c.clickhouseTLSConfig(step, d, host, false)
			if err != nil {
				return err
			}
			raw, err := c.clickhouseStream(step, d, m, 9440, false)
			if err != nil {
				return err
			}
			conn := tls.Client(raw, config)
			err = conn.HandshakeContext(step)
			_ = conn.Close()
			if err != nil {
				return fmt.Errorf("ClickHouse native endpoint failed verified TLS")
			}
			// Inspect the actual listeners, then try the native protocol without TLS.
			// Probing filtered service ports would measure a firewall timeout instead
			// of proving that the database stopped its plaintext listeners.
			command := []string{"bash", "-c", `set -eu
awk '$4 == "0A" { split($2,a,":"); if (a[2] == "1FBB" || a[2] == "2331" || (a[2] == "2328" && a[1] != "0100007F")) bad=1 } END { exit bad }' /proc/net/tcp /proc/net/tcp6
if clickhouse-client --config-file=/etc/hakopod/monitor.xml --port=9440 --connect_timeout=1 --receive_timeout=1 --send_timeout=1 --query 'SELECT 1' >/dev/null 2>&1; then exit 1; fi`}
			if err = c.DatabaseExec(step, d, m, command, nil, io.Discard); err != nil {
				return fmt.Errorf("ClickHouse plaintext rejection could not be verified")
			}
			return nil
		})
	}
	return group.Wait()
}
