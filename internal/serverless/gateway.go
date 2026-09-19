// Package serverless activates known public HTTP services and forwards each
// request once. It has no management routes or caller-controlled upstreams.
package serverless

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hakopod/hakopod/internal/idlehttp"
	"github.com/hakopod/hakopod/internal/spec"
)

type Route struct {
	Target   idlehttp.IdleHTTPService
	Backend  string
	Settings spec.Serverless
}
type Runtime interface {
	Routes(context.Context) ([]Route, error)
	Validate(context.Context, Route) error
	WakeHTTPService(context.Context, idlehttp.IdleHTTPService) error
	SleepHTTPService(context.Context, idlehttp.IdleHTTPService) error
}
type entry struct {
	route    Route
	gate     chan struct{}
	active   int
	awake    bool
	sleeping bool
	last     time.Time
}
type Gateway struct {
	runtime     Runtime
	mu          sync.RWMutex
	routes      map[string]*entry
	entries     map[string]*entry
	refreshed   time.Time
	refresh     chan struct{}
	slots       chan struct{}
	transitions chan struct{}
	transport   http.RoundTripper
	dial        func(context.Context, string, string) (net.Conn, error)
}

func New(runtime Runtime, sourceIP ...net.IP) *Gateway {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if len(sourceIP) > 0 && sourceIP[0] != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: sourceIP[0]}
	}
	return &Gateway{dial: dialer.DialContext, runtime: runtime, routes: map[string]*entry{}, entries: map[string]*entry{}, refresh: make(chan struct{}, 1), slots: make(chan struct{}, 128), transitions: make(chan struct{}, 2), transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 300 * time.Second, MaxResponseHeaderBytes: 64 << 10, ForceAttemptHTTP2: false}}
}
func key(r Route) string { return r.Target.ApplicationID + "/" + r.Target.Service }
func host(raw string) string {
	raw = strings.ToLower(raw)
	if h, _, e := net.SplitHostPort(raw); e == nil {
		raw = h
	}
	return strings.TrimSuffix(raw, ".")
}
func (g *Gateway) Refresh(ctx context.Context) error {
	select {
	case g.refresh <- struct{}{}:
		defer func() { <-g.refresh }()
	default:
		return nil
	}
	g.mu.RLock()
	recent := time.Since(g.refreshed) < time.Second
	g.mu.RUnlock()
	if recent {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	routes, err := g.runtime.Routes(ctx)
	if err != nil {
		return err
	}
	if len(routes) > 512 {
		return errors.New("serverless service limit exceeded")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entries := map[string]*entry{}
	hosts := map[string]*entry{}
	for _, r := range routes {
		upstream, e := url.Parse(r.Backend)
		if e != nil || upstream.Scheme != "http" || upstream.User != nil || net.ParseIP(upstream.Hostname()) == nil || len(r.Target.Hosts) > 64 || r.Settings.MaxConcurrency < 1 || r.Settings.MaxConcurrency > 64 {
			return errors.New("invalid serverless route")
		}
		v := g.entries[key(r)]
		if v == nil || v.route.Target.Revision != r.Target.Revision || v.route.Backend != r.Backend {
			v = &entry{route: r, gate: make(chan struct{}, 1), last: time.Now()}
		}
		entries[key(r)] = v
		for _, h := range r.Target.Hosts {
			h = host(h)
			if prior := hosts[h]; prior != nil && prior != v {
				return errors.New("ambiguous serverless host")
			}
			hosts[h] = v
		}
	}
	g.routes = hosts
	g.entries = entries
	g.refreshed = time.Now()
	return nil
}
func (g *Gateway) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = g.Refresh(ctx)
		g.SleepIdle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (g *Gateway) SleepIdle(ctx context.Context) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	g.mu.RLock()
	entries := make([]*entry, 0, len(g.entries))
	for _, e := range g.entries {
		entries = append(entries, e)
	}
	g.mu.RUnlock()
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		select {
		case e.gate <- struct{}{}:
		default:
			continue
		}
		if !e.sleeping && e.active == 0 && e.route.Settings.MinReplicas == 0 && time.Since(e.last) >= time.Duration(e.route.Settings.IdleSeconds)*time.Second {
			bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
			if g.runtime.SleepHTTPService(bounded, e.route.Target) == nil {
				e.awake = false
				e.sleeping = true
			}
			cancel()
		}
		<-e.gate
	}
}
func reject(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	if status == 429 || status == 503 {
		w.Header().Set("Retry-After", "5")
	}
	http.Error(w, message, status)
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		reject(w, 503, "HTTP capacity is busy. Try again shortly.")
		return
	}
	g.mu.RLock()
	e := g.routes[host(r.Host)]
	fresh := time.Since(g.refreshed) < 30*time.Second
	g.mu.RUnlock()
	if e == nil || !fresh {
		if err := g.Refresh(r.Context()); err != nil {
			reject(w, 503, "HTTP routing is temporarily unavailable.")
			return
		}
		g.mu.RLock()
		e = g.routes[host(r.Host)]
		fresh = time.Since(g.refreshed) < 30*time.Second
		g.mu.RUnlock()
	}
	if !fresh {
		reject(w, 503, "HTTP routing is temporarily unavailable.")
		return
	}
	if e == nil {
		reject(w, 404, "No public HTTP service is configured for this host.")
		return
	}
	if r.ContentLength > 4<<20 {
		reject(w, 413, "Request body exceeds 4 MiB.")
		return
	}
	// A bounded startup wait holds the original body unread and never replays it.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(e.route.Settings.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	if err := g.runtime.Validate(ctx, e.route); err != nil {
		reject(w, 503, "Service is stopped, changing or unavailable.")
		return
	}
	select {
	case e.gate <- struct{}{}:
	case <-ctx.Done():
		reject(w, 504, "Service did not become ready in time.")
		return
	}
	if e.active >= e.route.Settings.MaxConcurrency {
		<-e.gate
		reject(w, 429, "This service has reached its concurrent request limit.")
		return
	}
	if !e.awake {
		select {
		case g.transitions <- struct{}{}:
		case <-ctx.Done():
			<-e.gate
			reject(w, 504, "Service did not become ready in time.")
			return
		}
		err := g.runtime.WakeHTTPService(ctx, e.route.Target)
		if err == nil {
			err = g.backendReady(ctx, e.route.Backend)
		}
		<-g.transitions
		if err != nil {
			<-e.gate
			reject(w, 503, "Service could not start. Check deployment and pod logs.")
			return
		}
		e.awake = true
		e.sleeping = false
	}
	// A release may have changed while this request waited for another request
	// or for cold-start readiness. Recheck before consuming its original body.
	if err := g.runtime.Validate(ctx, e.route); err != nil {
		<-e.gate
		reject(w, 503, "Service is stopped, changing or unavailable.")
		return
	}
	e.active++
	<-e.gate
	defer func() { e.gate <- struct{}{}; e.active--; e.last = time.Now(); <-e.gate }()
	timeout := time.Duration(e.route.Settings.RequestTimeoutSeconds) * time.Second
	forwardCtx, stop := context.WithTimeout(r.Context(), timeout)
	defer stop()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(timeout))
	_ = controller.SetWriteDeadline(time.Now().Add(timeout + time.Second))
	defer controller.SetReadDeadline(time.Time{})
	defer controller.SetWriteDeadline(time.Time{})
	r = r.Clone(forwardCtx)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	upstream, _ := url.Parse(e.route.Backend)
	proxy := &httputil.ReverseProxy{Transport: g.transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0), Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(upstream)
		p.Out.Host = p.In.Host
		// Trust the direct ingress forwarding metadata, but never forward proxy auth.
		p.Out.Header.Del("Proxy-Authorization")
		// The advertised listener is private to the ingress/cluster. Preserve its
		// bounded, syntactically valid client chain before adding this proxy hop.
		if chain := p.In.Header.Get("X-Forwarded-For"); validForwardedFor(chain) {
			p.Out.Header.Set("X-Forwarded-For", chain)
		}
		p.SetXForwarded()
		if proto := p.In.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
			p.Out.Header.Set("X-Forwarded-Proto", proto)
		}
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		reason := "upstream closed or unavailable"
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			reason = "upstream connection timed out"
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			reason = "upstream connection refused"
		}
		slog.Debug("serverless forwarding failed", "application", e.route.Target.ApplicationID, "service", e.route.Target.Service, "reason", reason)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			reject(w, 413, "Request body exceeds 4 MiB.")
		case errors.Is(err, context.DeadlineExceeded):
			reject(w, 504, "Service request exceeded its time limit.")
		default:
			reject(w, 502, "Service could not complete the request. Check its logs.")
		}
	}}
	proxy.ServeHTTP(w, r)
}

func validForwardedFor(chain string) bool {
	if len(chain) > 512 || chain == "" {
		return false
	}
	parts := strings.Split(chain, ",")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if net.ParseIP(strings.TrimSpace(part)) == nil {
			return false
		}
	}
	return true
}

// Endpoint publication precedes kube-proxy dataplane updates. Probe TCP without
// sending an HTTP request or touching its body before forwarding the invocation.
func (g *Gateway) backendReady(ctx context.Context, backend string) error {
	upstream, err := url.Parse(backend)
	if err != nil {
		return err
	}
	for {
		conn, err := g.dial(ctx, "tcp", upstream.Host)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
