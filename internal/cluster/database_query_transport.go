package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// Use the container's network stack. Node-default sandbox runtimes can hide
// loopback listeners from Kubernetes port forwarding without a RuntimeClassName.
// The fixed relay carries encrypted protocol bytes. It does not receive SQL or
// credentials through command arguments, environment variables or log output.
func (c *Client) postgresQueryStream(ctx context.Context, d database.Resource, member database.Member) (net.Conn, error) {
	return c.queryOwnedRelay(ctx, d, member, 5432)
}

func (c *Client) queryOwnedRelay(ctx context.Context, d database.Resource, member database.Member, port int) (net.Conn, error) {
	if d.Spec.Engine != "postgresql" && d.Spec.Engine != "mysql" {
		return nil, queryUnavailable()
	}
	if port != 5432 && port != 3306 {
		return nil, queryUnavailable()
	}
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil {
		return nil, queryUnavailable()
	}
	lifetime, cancel := context.WithTimeout(ctx, 20*time.Second)
	relay := fmt.Sprintf(`set -eu
exec 3<>/dev/tcp/127.0.0.1/%d
cat <&3 & reader=$!
trap 'kill "$reader" 2>/dev/null || true' EXIT
cat >&3`, port)
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: container, Command: []string{"/bin/bash", "-c", relay}, Stdin: true, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, queryUnavailable()
	}
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() { once.Do(func() { cancel(); _ = conn.Close(); _ = stream.Close() }) }
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(lifetime, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() { <-lifetime.Done(); closeStream() }()
	return &mongodbStreamConn{Conn: conn, close: closeStream}, nil
}
