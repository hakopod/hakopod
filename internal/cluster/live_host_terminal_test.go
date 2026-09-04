package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveHostTerminal(t *testing.T) {
	if os.Getenv("HAKOPOD_HOST_TERMINAL_TEST") != "1" {
		t.Skip("set HAKOPOD_HOST_TERMINAL_TEST=1 for a disposable host shell on k3d-hakopod-dev")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("explicit k3d-hakopod-dev context required")
	}
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const node = "k3d-hakopod-dev-server-0"
	uid, err := c.HostNode(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("%032x", time.Now().UnixNano())
	if err = c.HostTerminal(ctx, node, session, types.UID("replaced"), strings.NewReader(""), io.Discard, nil); err == nil {
		t.Fatal("replaced node accepted")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		_, _ = io.WriteString(writer, "printf 'host-%s-verified\\n' shell; test -d /var/lib/rancher/k3s && printf 'node-%s-files-verified\\n' k3s; exit 0\n")
	}()
	var output bytes.Buffer
	if err = c.HostTerminal(ctx, node, session, uid, reader, &output, &liveTerminalSize{}); err != nil {
		t.Fatal(err, output.String())
	}
	if !strings.Contains(output.String(), "host-shell-verified") || !strings.Contains(output.String(), "node-k3s-files-verified") {
		t.Fatal("host filesystem shell missing", output.String())
	}
	clean, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	for clean.Err() == nil {
		jobs, err := c.kube.BatchV1().Jobs(HostAccessNamespace).List(clean, metav1.ListOptions{LabelSelector: hostSessionLabel + "=" + session, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		pods, err := c.kube.CoreV1().Pods(HostAccessNamespace).List(clean, metav1.ListOptions{LabelSelector: hostSessionLabel + "=" + session, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs.Items) == 0 && len(pods.Items) == 0 {
			t.Log("real node shell input/output and exact Job/pod cleanup verified")
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("host terminal workload cleanup did not complete")
}
