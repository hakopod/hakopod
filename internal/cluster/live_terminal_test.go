package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/logquery"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

type liveTerminalSize struct{ sent bool }

func (s *liveTerminalSize) Next() *remotecommand.TerminalSize {
	if s.sent {
		return nil
	}
	s.sent = true
	return &remotecommand.TerminalSize{Width: 88, Height: 27}
}

// The notification is delivered by the actual exec output stream, not by a
// timer; cancellation is tested only after the remote shell has started.
type liveTerminalOutput struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	started chan struct{}
	once    sync.Once
}

func (w *liveTerminalOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len()+len(p) > 64<<10 {
		return 0, fmt.Errorf("fixture output exceeded 64 KiB")
	}
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), "terminal-ready") {
		w.once.Do(func() { close(w.started) })
	}
	return n, err
}

func TestLiveTerminalAndStructuredLogs(t *testing.T) {
	if os.Getenv("HAKOPOD_TERMINAL_TEST") != "1" {
		t.Skip("set HAKOPOD_TERMINAL_TEST=1 for disposable terminal/log acceptance on k3d-hakopod-dev")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("terminal acceptance requires explicit named k3d-hakopod-dev context")
	}
	client, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "terminal-acceptance", Services: map[string]spec.Service{
		"worker": {Image: "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0", Command: []string{"sh", "-c"}, Args: []string{
			`printf '%s\n' '{"level":"info","status":200,"message":"fixture ready"}' '{"level":"error","status":503,"message":"fixture failed","request":{"method":"POST"}}' 'plain fixture log'; exec sleep 3600`,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("terminal-live-%d", time.Now().UnixNano()), Project: "terminal-test", Environment: "test", OperationID: "terminal-initial", Revision: 1, Spec: app}
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "terminal"
	namespace, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		current, err := client.kube.CoreV1().Namespaces().Get(clean, namespace.Name, metav1.GetOptions{})
		if err != nil || current.UID != namespace.UID || current.Labels["hakopod.io/acceptance"] != "terminal" || owned(current, target) != nil {
			t.Error("fixture ownership changed; retaining namespace for diagnosis")
			return
		}
		if err := client.kube.CoreV1().Namespaces().Delete(clean, current.Name, deleteOptions(current)); err != nil {
			t.Error(err)
			return
		}
		for clean.Err() == nil {
			if _, err := client.kube.CoreV1().Namespaces().Get(clean, current.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				t.Log("disposable terminal fixture namespace removed")
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Error("fixture namespace deletion did not complete")
	})
	if _, err = client.Deploy(ctx, target, func(event Event) { t.Log(event.Type, event.Message) }); err != nil {
		t.Fatal(err)
	}
	pods, err := client.kube.CoreV1().Pods(namespace.Name).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=worker", Limit: 5})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("expected one real fixture pod: %v", err)
	}
	pod := pods.Items[0]
	options := TerminalOptions{Pod: pod.Name}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	uid, err := client.TerminalPod(ctx, target, "worker", options)
	if err != nil || uid != pod.UID {
		t.Fatalf("real running pod selection failed: %v", err)
	}

	t.Run("interactive-input-output-resize-exit", func(t *testing.T) {
		command := options
		command.Command = []string{"/bin/sh", "-c", `read -r value; printf 'received:%s\n' "$value"; sleep 1; stty size; exit 7`}
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		// A live terminal keeps stdin open while output/exit arrive. An EOF-only
		// fixture can hang up the PTY before its final output is drained.
		go func() { _, _ = io.WriteString(writer, "fixture-input\n") }()
		var output bytes.Buffer
		err := client.Terminal(ctx, target, "worker", command, uid, reader, &output, &liveTerminalSize{})
		var status interface{ ExitStatus() int }
		if !errors.As(err, &status) || status.ExitStatus() != 7 {
			t.Fatalf("remote exit status was not preserved: %v; output=%q", err, output.String())
		}
		if !strings.Contains(output.String(), "received:fixture-input") || !strings.Contains(output.String(), "27 88") {
			t.Fatalf("shell input/output or actual PTY resize missing: %q", output.String())
		}
		t.Log("actual container shell received input, reported27x88 PTY size and exited7")
	})

	t.Run("ownership-and-pod-uid", func(t *testing.T) {
		if err := client.Terminal(ctx, target, "worker", options, types.UID("replaced-uid"), strings.NewReader(""), io.Discard, nil); err == nil || !strings.Contains(err.Error(), "replaced") {
			t.Fatalf("wrong pod UID accepted: %v", err)
		}
		wrong := options
		wrong.Container = "unrelated"
		if _, err := client.TerminalPod(ctx, target, "worker", wrong); err == nil {
			t.Fatal("absent container accepted")
		}
		if _, err := client.TerminalPod(ctx, target, "unrelated", options); err == nil {
			t.Fatal("unrelated service accepted")
		}
		// Relabel only this uniquely named fixture pod; restore before cleanup.
		current, err := client.kube.CoreV1().Pods(namespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		original := current.Labels[ownerKey]
		current.Labels[ownerKey] = "foreign-fixture"
		if _, err = client.kube.CoreV1().Pods(namespace.Name).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		_, denied := client.TerminalPod(ctx, target, "worker", options)
		current, err = client.kube.CoreV1().Pods(namespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		current.Labels[ownerKey] = original
		if _, err = client.kube.CoreV1().Pods(namespace.Name).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if denied == nil {
			t.Fatal("pod with foreign ownership accepted")
		}
		// Namespace ownership is independently checked for both observations.
		ns, err := client.kube.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ns.Labels[ownerKey] = "foreign-fixture"
		if _, err = client.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		_, terminalErr := client.TerminalPod(ctx, target, "worker", options)
		query := LogQueryOptions{Service: "worker"}
		_ = query.Validate()
		_, logErr := client.QueryLogs(ctx, target, query, func(logquery.Entry) bool { return true })
		ns, err = client.kube.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ns.Labels[ownerKey] = original
		if _, err = client.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if terminalErr == nil || logErr == nil {
			t.Fatal("foreign namespace accepted by terminal or log query")
		}
		t.Log("namespace, pod owner, service, container and stale UID refused")
	})

	t.Run("disconnect-cancels-exec", func(t *testing.T) {
		session, end := context.WithCancel(ctx)
		defer end()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		output := &liveTerminalOutput{started: make(chan struct{})}
		command := options
		command.Command = []string{"/bin/sh", "-c", "echo terminal-ready; read -r input"}
		result := make(chan error, 1)
		go func() { result <- client.Terminal(session, target, "worker", command, uid, reader, output, nil) }()
		select {
		case <-output.started:
		case err := <-result:
			t.Fatalf("exec stopped before cancellation: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("remote shell did not start")
		}
		end()
		writer.Close()
		select {
		case <-result:
			t.Log("disconnect returned the live exec transport within5s")
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled exec leaked its transport")
		}
	})

	t.Run("structured-query-and-sampled-histogram", func(t *testing.T) {
		query := LogQueryOptions{Service: "worker", Pod: pod.Name, Query: "severity >= ERROR AND json.status >= 500 AND json.request.method = 'POST'", SinceSeconds: 120, Limit: 10}
		if err := query.Validate(); err != nil {
			t.Fatal(err)
		}
		match, err := logquery.Compile(query.Query)
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.QueryLogs(ctx, target, query, match)
		if err != nil {
			t.Fatal(err)
		}
		if result.Pods != 1 || result.Scanned != 3 || result.Matched != 1 || len(result.Entries) != 1 || result.Truncated {
			t.Fatalf("actual log sample mismatch: %+v", result)
		}
		entry := result.Entries[0]
		if entry.Timestamp.IsZero() || entry.Pod != pod.Name || entry.Service != "worker" || entry.Container != "app" || entry.Severity != "ERROR" || !strings.Contains(entry.Message, "fixture failed") {
			t.Fatalf("real structured log metadata mismatch: %+v", entry)
		}
		count := 0
		for _, bucket := range result.Histogram {
			count += bucket.Count
		}
		if count != 1 || len(result.Warnings) == 0 {
			t.Fatal("sampled histogram or scope warning missing")
		}
		t.Log("real CRI timestamp, JSON filter, severity, pod metadata and sampled histogram verified")
	})
}
