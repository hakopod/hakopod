package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	httpstreamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	remotecommandconstants "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/rest"
	clientremotecommand "k8s.io/client-go/tools/remotecommand"
)

func testProxy(t *testing.T, upstream *httptest.Server) (*faultProxy, *httptest.Server) {
	t.Helper()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newFaultProxy(options{upstream: target, mode: "route_reload_after_mutation",
		proxyNamespace: "proxy", databaseNamespace: "hdb-database",
		routeNamespace: "hdb-database", routeResource: "dbpe-endpoint"})
	return proxy, httptest.NewServer(proxy)
}

func TestAbsoluteRequestTargetsNeverReachUpstream(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	proxy, server := testProxy(t, upstream)
	defer server.Close()
	for _, target := range []string{"http://unowned.invalid/version", "https://unowned.invalid/version"} {
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("absolute request status = %d", response.Code)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("absolute request reached the upstream API")
	}
}

func TestDuplicateConnectionHeadersCannotBypassUpgradePolicy(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		hijacker := w.(http.Hijacker)
		connection, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
		_ = buffer.Flush()
	}))
	defer upstream.Close()
	_, server := testProxy(t, upstream)
	defer server.Close()

	request := func(namespace string) int {
		connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = fmt.Fprintf(connection, "POST /api/v1/namespaces/%s/pods/owned/exec?command=show HTTP/1.1\r\nHost: fault.invalid\r\nConnection: keep-alive\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: v4.channel.k8s.io\r\nX-Stream-Protocol-Version: v3.channel.k8s.io\r\nContent-Length: 0\r\n\r\n", namespace)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	if status := request("other"); status != http.StatusBadRequest {
		t.Fatalf("unowned duplicate-header upgrade status = %d", status)
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("unowned duplicate-header request reached upstream %d times", got)
	}
	if status := request("proxy"); status != http.StatusSwitchingProtocols {
		t.Fatalf("owned duplicate-header upgrade status = %d", status)
	}
	if got := upstreamRequests.Load(); got != 1 {
		t.Fatalf("owned duplicate-header request reached upstream %d times", got)
	}
}

func TestValidatorsRejectMalformedOwnedIdentities(t *testing.T) {
	for _, value := range []string{"", "Upper", "-leading", "trailing-", strings.Repeat("a", 64)} {
		if validNamespace(value) {
			t.Errorf("validNamespace(%q) = true", value)
		}
	}
	for _, value := range []string{"", "bad..name", "Bad", strings.Repeat("a", 254)} {
		if validResourceName(value) {
			t.Errorf("validResourceName(%q) = true", value)
		}
	}
	if !validNamespace("hdb-database") || !validResourceName("dbpe-endpoint.example") {
		t.Fatal("valid owned identities were rejected")
	}
	base := options{mode: "route_reload_after_mutation", proxyNamespace: "proxy",
		databaseNamespace: "hdb-database", routeNamespace: "hdb-database", routeResource: "dbpe-endpoint"}
	if !validOwnedIdentities(base) {
		t.Fatal("valid distinct owned identities were rejected")
	}
	base.proxyNamespace = base.databaseNamespace
	if validOwnedIdentities(base) {
		t.Fatal("identical proxy and database namespaces were accepted")
	}
	base.proxyNamespace = "proxy"
	base.routeNamespace = "other-database"
	if validOwnedIdentities(base) {
		t.Fatal("route namespace outside the database namespace was accepted")
	}
}

func TestOwnedRouteDryRunIsRejectedWithoutUpstreamContact(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	_, server := testProxy(t, upstream)
	defer server.Close()
	body := strings.NewReader(`{"metadata":{"name":"dbpe-endpoint","namespace":"hdb-database"}}`)
	response, err := http.Post(server.URL+"/apis/ingress.v3.haproxy.org/v3/namespaces/hdb-database/tcps?dryRun=All",
		"application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("dry-run mutation status = %d", response.StatusCode)
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("dry-run mutation reached upstream %d times", got)
	}
}

func TestInvalidOwnedRouteMutationFormsAreRejected(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	_, server := testProxy(t, upstream)
	defer server.Close()
	body := `{"metadata":{"name":"dbpe-endpoint","namespace":"hdb-database"}}`
	collection := server.URL + "/apis/ingress.v3.haproxy.org/v3/namespaces/hdb-database/tcps"
	item := collection + "/dbpe-endpoint"
	for _, test := range []struct {
		method string
		url    string
	}{{http.MethodPut, collection}, {http.MethodPatch, collection}, {http.MethodPost, item}} {
		request, err := http.NewRequest(test.method, test.url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s status = %d", test.method, test.url, response.StatusCode)
		}
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("invalid owned route mutation reached upstream %d times", got)
	}
}

func TestMalformedUpgradePathsAreRejectedWithoutUpstreamContact(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer upstream.Close()
	proxy, _ := testProxy(t, upstream)
	for _, path := range []string{
		"/api/v1/namespaces/hdb..database/pods/owned/exec",
		"/api/v1/namespaces/hdb-database%2Fother/pods/owned/exec",
		"/api/v1/namespaces/hdb-database/pods/owned/exec/extra",
		"/api/v1/namespaces/hdb-database/pods/owned/attach",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://fault"+path, nil)
		request.Header.Add("Connection", "keep-alive")
		request.Header.Add("Connection", "Upgrade")
		request.Header.Set("Upgrade", "SPDY/3.1")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("malformed upgrade %q status = %d", path, response.Code)
		}
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("malformed upgrades reached upstream %d times", got)
	}
}

func TestRequestBodyBoundRejectsBeforeUpstreamContact(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	_, server := testProxy(t, upstream)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/version",
		io.LimitReader(strings.NewReader(strings.Repeat("x", maxBody+1)), maxBody+1))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = maxBody + 1
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d", response.StatusCode)
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("oversized request reached upstream %d times", got)
	}
}

func TestUpgradeRelayAllowsOwnedNamespacesAndStreamsBothDirections(t *testing.T) {
	requests := make(chan *http.Request, 2)
	clientFrames := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream response writer cannot upgrade")
			return
		}
		connection, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\nserver-frame")
		if err = buffer.Flush(); err != nil {
			t.Error(err)
			return
		}
		frame := make([]byte, len("client-frame"))
		if _, err = io.ReadFull(buffer, frame); err != nil {
			t.Error(err)
			return
		}
		clientFrames <- string(frame)
		fmt.Fprint(buffer, "server-tail")
		_ = buffer.Flush()
	}))
	defer upstream.Close()
	proxy, server := testProxy(t, upstream)
	defer server.Close()

	for _, namespace := range []string{"proxy", "hdb-database"} {
		t.Run(namespace, func(t *testing.T) {
			address := strings.TrimPrefix(server.URL, "http://")
			connection, err := net.DialTimeout("tcp", address, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprintf(connection, "POST /api/v1/namespaces/%s/pods/owned/exec?command=show HTTP/1.1\r\nHost: fault.invalid\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: v4.channel.k8s.io\r\nX-Stream-Protocol-Version: v3.channel.k8s.io\r\nContent-Length: 0\r\n\r\n", namespace)
			reader := bufio.NewReader(connection)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade status = %d", response.StatusCode)
			}
			serverFrame := make([]byte, len("server-frame"))
			if _, err = io.ReadFull(reader, serverFrame); err != nil || string(serverFrame) != "server-frame" {
				t.Fatalf("server frame = %q, %v", serverFrame, err)
			}
			if _, err = connection.Write([]byte("client-frame")); err != nil {
				t.Fatal(err)
			}
			serverTail := make([]byte, len("server-tail"))
			if _, err = io.ReadFull(reader, serverTail); err != nil || string(serverTail) != "server-tail" {
				t.Fatalf("server tail = %q, %v", serverTail, err)
			}
			request := <-requests
			if request.Header.Get("Upgrade") != "SPDY/3.1" ||
				len(request.Header.Values("X-Stream-Protocol-Version")) != 2 {
				t.Fatalf("upgrade headers were not preserved: %#v", request.Header)
			}
			if frame := <-clientFrames; frame != "client-frame" {
				t.Fatalf("client frame = %q", frame)
			}
		})
	}
	state := proxy.state.snapshot(proxy.options.mode)
	if state["pre_mutation_proxy_upgrade_relays"] != 0 || state["pre_mutation_database_upgrade_relays"] != 0 {
		t.Fatalf("route mutation snapshot was populated early: %#v", state)
	}
}

func TestRouteMutationArmsOnlyProxyExecAndSnapshotsUpgradeProof(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	proxy, server := testProxy(t, upstream)
	defer server.Close()
	proxy.state.recordUpgrade("proxy", proxy.options)
	proxy.state.recordUpgrade("hdb-database", proxy.options)

	body := []byte(`{"metadata":{"name":"dbpe-endpoint","namespace":"hdb-database"}}`)
	response, err := http.Post(server.URL+"/apis/ingress.v3.haproxy.org/v3/namespaces/hdb-database/tcps",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("route mutation status = %d", response.StatusCode)
	}
	state := proxy.state.snapshot(proxy.options.mode)
	if state["route_mutation_seen"] != true || state["pre_mutation_proxy_upgrade_relays"] != 1 ||
		state["pre_mutation_database_upgrade_relays"] != 1 || state["successful_route_mutations"] != 1 {
		t.Fatalf("route mutation proof = %#v", state)
	}

	for _, test := range []struct {
		namespace string
		status    int
	}{{"proxy", http.StatusServiceUnavailable}, {"hdb-database", http.StatusCreated}, {"unowned", http.StatusBadRequest}} {
		request := httptest.NewRequest(http.MethodPost,
			"/api/v1/namespaces/"+test.namespace+"/pods/owned/exec?command=show", nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "SPDY/3.1")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("%s status = %d, want %d", test.namespace, response.Code, test.status)
		}
	}
}

type integrationStream struct {
	httpstream.Stream
	replySent <-chan struct{}
}

type fakeSPDYAPI struct {
	mu              sync.Mutex
	execPaths       []string
	protocolHeaders [][]string
	routeMutations  int
	unexpectedPaths []string
	holdStarted     chan struct{}
	holdClosed      chan struct{}
	holdOnce        sync.Once
}

func newFakeSPDYAPI() *fakeSPDYAPI {
	return &fakeSPDYAPI{holdStarted: make(chan struct{}), holdClosed: make(chan struct{})}
}

func (f *fakeSPDYAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/version" {
		writeJSON(w, http.StatusOK, map[string]string{"gitVersion": "v0.0.0-fake"})
		return
	}
	if r.URL.Path == "/apis/ingress.v3.haproxy.org/v3/namespaces/hdb-database/tcps" &&
		r.Method == http.MethodPost {
		f.mu.Lock()
		f.routeMutations++
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]bool{"created": true})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "namespaces" ||
		parts[4] != "pods" || parts[6] != "exec" {
		f.mu.Lock()
		f.unexpectedPaths = append(f.unexpectedPaths, r.URL.Path)
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	f.execPaths = append(f.execPaths, r.URL.Path)
	f.protocolHeaders = append(f.protocolHeaders,
		append([]string(nil), r.Header.Values(httpstream.HeaderProtocolVersion)...))
	f.mu.Unlock()
	if _, err := httpstream.Handshake(r, w, []string{remotecommandconstants.StreamProtocolV4Name}); err != nil {
		return
	}
	streams := make(chan integrationStream, 4)
	connection := httpstreamspdy.NewResponseUpgrader().UpgradeResponse(w, r,
		func(stream httpstream.Stream, replySent <-chan struct{}) error {
			streams <- integrationStream{Stream: stream, replySent: replySent}
			return nil
		})
	if connection == nil {
		return
	}
	defer connection.Close()
	expected := 1
	if r.URL.Query().Get("stdin") == "true" {
		expected++
	}
	if r.URL.Query().Get("stdout") == "true" {
		expected++
	}
	if r.URL.Query().Get("stderr") == "true" {
		expected++
	}
	var stdin io.ReadCloser
	var stdout io.WriteCloser
	var errorStream io.WriteCloser
	for received := 0; received < expected; received++ {
		select {
		case item := <-streams:
			<-item.replySent
			switch item.Headers().Get(corev1.StreamType) {
			case corev1.StreamTypeError:
				errorStream = item
			case corev1.StreamTypeStdin:
				stdin = item
			case corev1.StreamTypeStdout:
				stdout = item
			default:
				item.Reset()
				return
			}
		case <-time.After(5 * time.Second):
			return
		}
	}
	if parts[5] == "hold" {
		f.holdOnce.Do(func() { close(f.holdStarted) })
		<-connection.CloseChan()
		close(f.holdClosed)
		return
	}
	if stdin == nil || stdout == nil || errorStream == nil {
		return
	}
	_, _ = io.Copy(stdout, stdin)
	_ = stdout.Close()
	status, _ := json.Marshal(metav1.Status{Status: metav1.StatusSuccess})
	_, _ = errorStream.Write(status)
	_ = errorStream.Close()
}

func (f *fakeSPDYAPI) unexpected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unexpectedPaths...)
}

func (f *fakeSPDYAPI) snapshot() ([]string, [][]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := append([]string(nil), f.execPaths...)
	headers := make([][]string, len(f.protocolHeaders))
	for index := range f.protocolHeaders {
		headers[index] = append([]string(nil), f.protocolHeaders[index]...)
	}
	return paths, headers, f.routeMutations
}

func startRealKubectlProxy(t *testing.T, apiOrigin string) string {
	t.Helper()
	kubectl := os.Getenv("TEST_KUBECTL")
	if kubectl == "" {
		t.Skip("TEST_KUBECTL is required for the real kubectl proxy SPDY integration test")
	}
	if !filepath.IsAbs(kubectl) {
		t.Fatal("TEST_KUBECTL must be an absolute executable path")
	}
	info, err := os.Stat(kubectl)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatal("TEST_KUBECTL is not an executable regular file")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	kubeconfig := filepath.Join(t.TempDir(), "fake-kubeconfig.json")
	config := map[string]any{
		"apiVersion": "v1", "kind": "Config", "current-context": "fake",
		"clusters": []any{map[string]any{"name": "fake", "cluster": map[string]any{"server": apiOrigin}}},
		"contexts": []any{map[string]any{"name": "fake", "context": map[string]any{"cluster": "fake", "user": "fake"}}},
		"users":    []any{map[string]any{"name": "fake", "user": map[string]any{}}},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(kubeconfig, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command := exec.Command(kubectl, "--kubeconfig", kubeconfig, "--context", "fake", "proxy",
		"--address", "127.0.0.1", "--port", strconv.Itoa(port),
		"--accept-hosts", `^127\.0\.0\.1$`,
		"--reject-paths=^/api/.*/pods/.*/attach,^/api/.*/pods/.*/portforward")
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	var cleanup sync.Once
	stop := func() {
		cleanup.Do(func() {
			_ = command.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		})
	}
	t.Cleanup(stop)
	origin := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := client.Get(origin + "/version")
		if requestErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return origin
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("real kubectl proxy did not become ready: %s", strings.TrimSpace(stderr.String()))
	return ""
}

func startBoundedHelper(t *testing.T, proxy *faultProxy) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bounded := newBoundedListener(listener)
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: requestLifetime, WriteTimeout: requestLifetime, IdleTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(bounded) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = bounded.Close()
			_ = server.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("bounded helper server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return "http://" + listener.Addr().String(), stop
}

func spdyExec(origin, namespace, pod, input string) (string, error) {
	endpoint, err := url.Parse(origin + "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/exec")
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("command", "cat")
	query.Set("stdin", "true")
	query.Set("stdout", "true")
	query.Set("stderr", "false")
	query.Set("tty", "false")
	endpoint.RawQuery = query.Encode()
	executor, err := clientremotecommand.NewSPDYExecutor(&rest.Config{Host: origin}, http.MethodPost, endpoint)
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = executor.StreamWithContext(ctx, clientremotecommand.StreamOptions{
		Stdin: strings.NewReader(input), Stdout: &output,
	})
	return output.String(), err
}

func TestRealKubectlProxySPDYEndToEnd(t *testing.T) {
	fake := newFakeSPDYAPI()
	apiServer := httptest.NewServer(fake)
	defer apiServer.Close()
	kubectlOrigin := startRealKubectlProxy(t, apiServer.URL)
	target, err := url.Parse(kubectlOrigin)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newFaultProxy(options{upstream: target, mode: "route_reload_after_mutation",
		proxyNamespace: "proxy", databaseNamespace: "hdb-database",
		routeNamespace: "hdb-database", routeResource: "dbpe-endpoint"})
	helperOrigin, stopHelper := startBoundedHelper(t, proxy)

	for _, path := range []string{
		"/api/v1/namespaces/hdb-database/pods/owned/attach",
		"/api/v1/namespaces/hdb-database/pods/owned/portforward",
	} {
		response, requestErr := http.Post(kubectlOrigin+path, "application/octet-stream", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("kubectl proxy rejected-path status for %s = %d", path, response.StatusCode)
		}
	}
	if unexpected := fake.unexpected(); len(unexpected) != 0 {
		t.Fatalf("kubectl proxy forwarded rejected paths: %#v", unexpected)
	}

	for _, test := range []struct {
		namespace string
		input     string
	}{{"hdb-database", "database-stream"}, {"proxy", "haproxy-stream"}} {
		output, streamErr := spdyExec(helperOrigin, test.namespace, "owned", test.input)
		if streamErr != nil || output != test.input {
			t.Fatalf("%s bidirectional stream output = %q, err = %v", test.namespace, output, streamErr)
		}
	}
	paths, protocols, _ := fake.snapshot()
	if len(paths) != 2 || len(protocols) != 2 {
		t.Fatalf("initial SPDY relay inventory = %#v, %#v", paths, protocols)
	}
	for _, values := range protocols {
		if len(values) < 2 || !strings.Contains(strings.Join(values, ","), remotecommandconstants.StreamProtocolV4Name) ||
			!strings.Contains(strings.Join(values, ","), remotecommandconstants.StreamProtocolV3Name) {
			t.Fatalf("duplicate stream protocol negotiation was not preserved: %#v", values)
		}
	}

	before := len(paths)
	for _, rawPath := range []string{
		"/api/v1/namespaces/third/pods/owned/exec",
		"/api/v1/namespaces/hdb-database/pods/owned/exec/extra",
	} {
		endpoint, parseErr := url.Parse(helperOrigin + rawPath + "?command=cat&stdout=true")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		executor, createErr := clientremotecommand.NewSPDYExecutor(&rest.Config{Host: helperOrigin}, http.MethodPost, endpoint)
		if createErr != nil {
			t.Fatal(createErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		streamErr := executor.StreamWithContext(ctx, clientremotecommand.StreamOptions{Stdout: io.Discard})
		cancel()
		if streamErr == nil {
			t.Fatalf("malformed or unowned SPDY path was accepted: %s", rawPath)
		}
	}
	paths, _, _ = fake.snapshot()
	if len(paths) != before {
		t.Fatal("malformed or unowned SPDY request contacted the upstream API")
	}

	routeBody := strings.NewReader(`{"metadata":{"name":"dbpe-endpoint","namespace":"hdb-database"}}`)
	response, err := http.Post(helperOrigin+"/apis/ingress.v3.haproxy.org/v3/namespaces/hdb-database/tcps",
		"application/json", routeBody)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("owned route mutation status = %d", response.StatusCode)
	}
	paths, _, mutations := fake.snapshot()
	before = len(paths)
	if mutations != 1 {
		t.Fatalf("owned route mutations = %d", mutations)
	}
	state := proxy.state.snapshot(proxy.options.mode)
	if state["pre_mutation_proxy_upgrade_relays"] != 1 ||
		state["pre_mutation_database_upgrade_relays"] != 1 ||
		state["successful_route_mutations"] != 1 || state["route_mutation_seen"] != true {
		t.Fatalf("fault proxy proof counters = %#v", state)
	}
	if _, streamErr := spdyExec(helperOrigin, "proxy", "owned", "blocked"); streamErr == nil {
		t.Fatal("post-mutation HAProxy exec was not blocked")
	}
	afterBlocked, _, _ := fake.snapshot()
	if len(afterBlocked) != before {
		t.Fatal("post-mutation HAProxy exec contacted the upstream API")
	}
	if output, streamErr := spdyExec(helperOrigin, "hdb-database", "owned", "still-allowed"); streamErr != nil || output != "still-allowed" {
		t.Fatalf("post-mutation database exec output = %q, err = %v", output, streamErr)
	}

	holdEndpoint, err := url.Parse(helperOrigin +
		"/api/v1/namespaces/hdb-database/pods/hold/exec?command=hold&stdout=true&stdin=false&stderr=false&tty=false")
	if err != nil {
		t.Fatal(err)
	}
	holdExecutor, err := clientremotecommand.NewSPDYExecutor(&rest.Config{Host: helperOrigin}, http.MethodPost, holdEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	holdResult := make(chan error, 1)
	go func() {
		holdResult <- holdExecutor.StreamWithContext(context.Background(),
			clientremotecommand.StreamOptions{Stdout: io.Discard})
	}()
	select {
	case <-fake.holdStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active SPDY stream did not start")
	}
	paths, _, _ = fake.snapshot()
	before = len(paths)
	if _, streamErr := spdyExec(helperOrigin, "hdb-database", "owned", "over-limit"); streamErr == nil {
		t.Fatal("second concurrent upgraded stream exceeded the configured bound")
	}
	paths, _, _ = fake.snapshot()
	if len(paths) != before {
		t.Fatal("over-limit upgraded stream contacted the upstream API")
	}
	select {
	case <-holdResult:
		t.Fatal("active upgraded stream ended before shutdown")
	default:
	}
	select {
	case <-fake.holdClosed:
		t.Fatal("upstream upgraded connection closed before shutdown")
	default:
	}
	stopHelper()
	select {
	case <-holdResult:
		// Client-go may treat a closed, empty error stream as success. The
		// required shutdown evidence is closure on both sides of the relay.
	case <-time.After(5 * time.Second):
		t.Fatal("active upgraded stream remained open after bounded listener shutdown")
	}
	select {
	case <-fake.holdClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream upgraded connection remained open after shutdown")
	}
}
