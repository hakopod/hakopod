package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxBody         = 2 << 20
	maxWireBytes    = 4 << 20
	maxHandlers     = 8
	maxUpgrade      = 1
	requestLifetime = 35 * time.Second
	statusPath      = "/__hakopod_fault_proxy/status"
)

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

func validNamespace(value string) bool {
	return len(value) <= 63 && dnsLabelPattern.MatchString(value)
}

func validResourceName(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validNamespace(label) {
			return false
		}
	}
	return true
}

type options struct {
	listenAddr        string
	upstream          *url.URL
	mode              string
	proxyNamespace    string
	databaseNamespace string
	routeNamespace    string
	routeResource     string
}

type requestMetadata struct {
	upgradeNamespace string
	routeMutation    bool
}

type requestMetadataKey struct{}

type recordingConn struct {
	net.Conn
	once   sync.Once
	record func()
}

func (c *recordingConn) Write(value []byte) (int, error) {
	n, err := c.Conn.Write(value)
	if n > 0 {
		c.once.Do(c.record)
	}
	return n, err
}

type upgradeResponseWriter struct {
	http.ResponseWriter
	record func()
}

func (w *upgradeResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	connection, buffer, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	recorded := &recordingConn{Conn: connection, record: w.record}
	buffer.Writer.Reset(recorded)
	return recorded, buffer, nil
}

type faultState struct {
	mu                       sync.Mutex
	routeMutationSeen        bool
	proxyUpgrades            int
	databaseUpgrades         int
	preMutationProxyUpgrades int
	preMutationDBUpgrades    int
	successfulRouteMutations int
}

func (s *faultState) recordUpgrade(namespace string, cfg options) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if namespace == cfg.proxyNamespace {
		s.proxyUpgrades++
	}
	if namespace == cfg.databaseNamespace {
		s.databaseUpgrades++
	}
}

func (s *faultState) recordRouteMutation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.routeMutationSeen {
		s.preMutationProxyUpgrades = s.proxyUpgrades
		s.preMutationDBUpgrades = s.databaseUpgrades
	}
	s.successfulRouteMutations++
	s.routeMutationSeen = true
}

func (s *faultState) blocksProxyExec(mode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return mode == "master_socket_unavailable" || mode == "route_reload_after_mutation" && s.routeMutationSeen
}

func (s *faultState) snapshot(mode string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"schema_version":                       1,
		"mode":                                 mode,
		"route_mutation_seen":                  s.routeMutationSeen,
		"successful_route_mutations":           s.successfulRouteMutations,
		"pre_mutation_proxy_upgrade_relays":    s.preMutationProxyUpgrades,
		"pre_mutation_database_upgrade_relays": s.preMutationDBUpgrades,
	}
}

type boundedConn struct {
	net.Conn
	readMu, writeMu sync.Mutex
	read, write     int64
}

func (c *boundedConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	remaining := int64(maxWireBytes) - c.read
	if remaining <= 0 {
		return 0, fmt.Errorf("upstream response exceeded %d bytes", maxWireBytes)
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.Conn.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *boundedConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	remaining := int64(maxWireBytes) - c.write
	if remaining <= 0 || int64(len(p)) > remaining {
		return 0, fmt.Errorf("upstream request exceeded %d bytes", maxWireBytes)
	}
	n, err := c.Conn.Write(p)
	c.write += int64(n)
	return n, err
}

type fixedBufferPool struct{ pool sync.Pool }

func (p *fixedBufferPool) Get() []byte {
	if value := p.pool.Get(); value != nil {
		return value.([]byte)
	}
	return make([]byte, 32<<10)
}

func (p *fixedBufferPool) Put(value []byte) {
	if cap(value) == 32<<10 {
		p.pool.Put(value[:cap(value)])
	}
}

type boundedListener struct {
	net.Listener
	slots  chan struct{}
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
	open   map[*boundedListenerConn]struct{}
}

type boundedListenerConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func newBoundedListener(listener net.Listener) *boundedListener {
	return &boundedListener{Listener: listener, slots: make(chan struct{}, maxHandlers),
		done: make(chan struct{}), open: map[*boundedListenerConn]struct{}{}}
}

func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	wrapped := &boundedListenerConn{Conn: connection}
	wrapped.release = func() {
		l.mu.Lock()
		delete(l.open, wrapped)
		l.mu.Unlock()
		<-l.slots
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = connection.Close()
		<-l.slots
		return nil, net.ErrClosed
	}
	l.open[wrapped] = struct{}{}
	l.mu.Unlock()
	return wrapped, nil
}

func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	l.mu.Lock()
	l.closed = true
	connections := make([]*boundedListenerConn, 0, len(l.open))
	for connection := range l.open {
		connections = append(connections, connection)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, connection := range connections {
		_ = connection.Close()
	}
	return err
}

func (c *boundedListenerConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type faultProxy struct {
	options  options
	state    faultState
	requests chan struct{}
	upgrades chan struct{}
	proxy    *httputil.ReverseProxy
}

func newFaultProxy(cfg options) *faultProxy {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 5 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		MaxConnsPerHost:       maxHandlers,
		MaxIdleConns:          maxHandlers,
		MaxIdleConnsPerHost:   maxHandlers,
		IdleConnTimeout:       5 * time.Second,
		ResponseHeaderTimeout: requestLifetime,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if err = connection.SetDeadline(time.Now().Add(requestLifetime)); err != nil {
				connection.Close()
				return nil, err
			}
			return &boundedConn{Conn: connection}, nil
		},
	}
	result := &faultProxy{options: cfg, requests: make(chan struct{}, maxHandlers),
		upgrades: make(chan struct{}, maxUpgrade)}
	result.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(cfg.upstream)
			request.Out.Host = cfg.upstream.Host
		},
		Transport:  transport,
		BufferPool: &fixedBufferPool{},
		ErrorLog:   log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProblem(w, http.StatusBadGateway, "fault_proxy_upstream", "The owned Kubernetes proxy is unavailable.")
		},
		ModifyResponse: func(response *http.Response) error {
			metadata, _ := response.Request.Context().Value(requestMetadataKey{}).(requestMetadata)
			if response.StatusCode >= 200 && response.StatusCode < 300 && metadata.routeMutation {
				result.state.recordRouteMutation()
			}
			return nil
		},
	}
	return result
}

func (p *faultProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.requests <- struct{}{}:
		defer func() { <-p.requests }()
	default:
		writeProblem(w, http.StatusServiceUnavailable, "fault_proxy_busy", "The bounded fault proxy is busy.")
		return
	}
	if r.URL.IsAbs() || strings.HasPrefix(r.RequestURI, "http://") || strings.HasPrefix(r.RequestURI, "https://") {
		writeProblem(w, http.StatusBadRequest, "invalid_proxy_request", "Absolute-form proxy requests are forbidden.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut &&
		r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeProblem(w, http.StatusMethodNotAllowed, "invalid_proxy_method", "The Kubernetes request method is not allowed.")
		return
	}
	if r.URL.Path == statusPath {
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			writeProblem(w, http.StatusBadRequest, "invalid_status_request", "Use GET without a query string.")
			return
		}
		writeJSON(w, http.StatusOK, p.state.snapshot(p.options.mode))
		return
	}
	if p.options.mode == "missing_tcp_crd" && strings.HasPrefix(r.URL.Path, "/apis/ingress.v3.haproxy.org/v3/") {
		writeKubernetesStatus(w, http.StatusNotFound, "NotFound")
		return
	}
	upgradeNamespace, upgrade := p.upgradeNamespace(r)
	if upgrade {
		if upgradeNamespace == "" {
			writeProblem(w, http.StatusBadRequest, "invalid_upgrade", "Only owned Kubernetes pod exec upgrades are allowed.")
			return
		}
		if upgradeNamespace == p.options.proxyNamespace && p.state.blocksProxyExec(p.options.mode) {
			writeKubernetesStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable")
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			writeProblem(w, http.StatusBadRequest, "invalid_upgrade", "Kubernetes pod exec upgrades must not include a request body.")
			return
		}
		select {
		case p.upgrades <- struct{}{}:
			defer func() { <-p.upgrades }()
		default:
			writeProblem(w, http.StatusServiceUnavailable, "fault_proxy_busy", "The bounded upgrade relay is busy.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestLifetime)
		defer cancel()
		metadata := requestMetadata{upgradeNamespace: upgradeNamespace}
		writer := &upgradeResponseWriter{ResponseWriter: w,
			record: func() { p.state.recordUpgrade(upgradeNamespace, p.options) }}
		p.proxy.ServeHTTP(writer, r.WithContext(context.WithValue(ctx, requestMetadataKey{}, metadata)))
		return
	}
	if len(r.TransferEncoding) != 0 || r.ContentLength > maxBody {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request_too_large", "The proxied Kubernetes request exceeded its bound.")
		return
	}
	mutationMethod := r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch
	if p.ownedRoutePath(r) && mutationMethod && (!p.routeMutationPath(r) || r.URL.RawQuery != "") {
		writeProblem(w, http.StatusBadRequest, "invalid_route_mutation", "The owned route mutation target or query is invalid.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request_too_large", "The proxied Kubernetes request exceeded its bound.")
		return
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	metadata := requestMetadata{routeMutation: p.ownedRouteMutation(r, body)}
	ctx, cancel := context.WithTimeout(r.Context(), requestLifetime)
	defer cancel()
	p.proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, requestMetadataKey{}, metadata)))
}

func (p *faultProxy) upgradeNamespace(r *http.Request) (string, bool) {
	connectionUpgrade := false
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			connectionUpgrade = connectionUpgrade || strings.EqualFold(strings.TrimSpace(token), "upgrade")
		}
	}
	if r.Header.Get("Upgrade") == "" || !connectionUpgrade {
		return "", false
	}
	if r.Method != http.MethodPost {
		return "", true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "namespaces" ||
		parts[4] != "pods" || parts[6] != "exec" || parts[3] == "" || parts[5] == "" {
		return "", true
	}
	namespace, err := url.PathUnescape(parts[3])
	if err != nil || namespace != p.options.proxyNamespace && namespace != p.options.databaseNamespace {
		return "", true
	}
	return namespace, true
}

func (p *faultProxy) ownedRouteMutation(r *http.Request, body []byte) bool {
	if !p.routeMutationPath(r) || r.URL.RawQuery != "" {
		return false
	}
	var object struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	return json.Unmarshal(body, &object) == nil && object.Metadata.Name == p.options.routeResource &&
		object.Metadata.Namespace == p.options.routeNamespace
}

func (p *faultProxy) routeMutationPath(r *http.Request) bool {
	if p.options.mode != "route_reload_after_mutation" ||
		r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
		return false
	}
	collection := p.routeCollectionPath()
	if !p.ownedRoutePath(r) {
		return false
	}
	if r.Method == http.MethodPost && r.URL.Path != collection {
		return false
	}
	if (r.Method == http.MethodPut || r.Method == http.MethodPatch) &&
		r.URL.Path != collection+"/"+url.PathEscape(p.options.routeResource) {
		return false
	}
	return true
}

func (p *faultProxy) routeCollectionPath() string {
	return "/apis/ingress.v3.haproxy.org/v3/namespaces/" + url.PathEscape(p.options.routeNamespace) + "/tcps"
}

func (p *faultProxy) ownedRoutePath(r *http.Request) bool {
	if p.options.mode != "route_reload_after_mutation" {
		return false
	}
	collection := p.routeCollectionPath()
	return r.URL.Path == collection || r.URL.Path == collection+"/"+url.PathEscape(p.options.routeResource)
}

func validOwnedIdentities(cfg options) bool {
	validMode := cfg.mode == "missing_tcp_crd" || cfg.mode == "master_socket_unavailable" || cfg.mode == "route_reload_after_mutation"
	if !validMode || !validNamespace(cfg.proxyNamespace) || !validNamespace(cfg.databaseNamespace) ||
		cfg.proxyNamespace == cfg.databaseNamespace {
		return false
	}
	if cfg.mode == "route_reload_after_mutation" {
		return validNamespace(cfg.routeNamespace) && validResourceName(cfg.routeResource) &&
			cfg.routeNamespace == cfg.databaseNamespace
	}
	return cfg.routeNamespace == "" && cfg.routeResource == ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeKubernetesStatus(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "code": status})
}

func parseOptions() (options, error) {
	listen := flag.String("listen", "", "owned loopback listen address")
	upstreamValue := flag.String("upstream", "", "owned loopback kubectl proxy URL")
	mode := flag.String("mode", "", "fault mode")
	proxyNamespace := flag.String("proxy-namespace", "", "owned HAProxy namespace")
	databaseNamespace := flag.String("database-namespace", "", "owned database namespace")
	routeNamespace := flag.String("route-namespace", "", "owned database route namespace")
	routeResource := flag.String("route-resource", "", "owned HAProxy TCP resource")
	flag.Parse()
	if flag.NArg() != 0 {
		return options{}, errors.New("positional arguments are forbidden")
	}
	identities := options{mode: *mode, proxyNamespace: *proxyNamespace, databaseNamespace: *databaseNamespace,
		routeNamespace: *routeNamespace, routeResource: *routeResource}
	if !validOwnedIdentities(identities) {
		return options{}, errors.New("fault mode, proxy namespace and database namespace are required")
	}
	host, port, err := net.SplitHostPort(*listen)
	listenPort, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || listenPort < 1 || listenPort > 65535 ||
		net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return options{}, errors.New("listen address must use a numeric loopback host and port")
	}
	upstream, err := url.Parse(*upstreamValue)
	if err != nil || upstream.Scheme != "http" || upstream.User != nil || upstream.Path != "" ||
		upstream.RawQuery != "" || upstream.Fragment != "" {
		return options{}, errors.New("upstream must be an owned loopback HTTP origin")
	}
	upstreamHost, upstreamPort, err := net.SplitHostPort(upstream.Host)
	parsedUpstreamPort, portErr := strconv.Atoi(upstreamPort)
	if err != nil || portErr != nil || parsedUpstreamPort < 1 || parsedUpstreamPort > 65535 ||
		net.ParseIP(upstreamHost) == nil || !net.ParseIP(upstreamHost).IsLoopback() {
		return options{}, errors.New("upstream must use a numeric loopback host and port")
	}
	return options{listenAddr: *listen, upstream: upstream, mode: *mode,
		proxyNamespace: *proxyNamespace, databaseNamespace: *databaseNamespace,
		routeNamespace: *routeNamespace, routeResource: *routeResource}, nil
}

func main() {
	cfg, err := parseOptions()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid fault proxy configuration")
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fault proxy could not bind its owned listener")
		os.Exit(1)
	}
	server := &http.Server{Handler: newFaultProxy(cfg), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: requestLifetime, WriteTimeout: requestLifetime, IdleTimeout: 5 * time.Second,
		MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(newBoundedListener(listener)) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdown)
		cancel()
		_ = server.Close()
		err = <-done
	case err = <-done:
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "fault proxy stopped unexpectedly")
		os.Exit(1)
	}
}
