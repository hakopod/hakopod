package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

// Explorer targets are operator configuration. Requests never choose an upstream.
// Each target must have its own OOS metadata, encryption keys and data volume.
type explorerTarget struct {
	Scope          string `json:"scope" toml:"scope"`
	Project        string `json:"project" toml:"project"`
	Environment    string `json:"environment" toml:"environment"`
	URL            string `json:"url" toml:"url"`
	KeyFile        string `json:"key_file" toml:"key_file"`
	CloudWorkspace string `json:"cloud_workspace,omitempty" toml:"cloud_workspace"`
	key            string
	mu             sync.Mutex
	nextSync       time.Time
}
type explorerRuntime struct {
	targets []*explorerTarget
	mu      sync.Mutex
	tickets map[string]*explorerTicket
	client  *http.Client
	err     error
}
type explorerTicket struct {
	target    *explorerTarget
	expires   time.Time
	session   string
	authorize func(context.Context) (store.Principal, string, string, string, error)
}
type explorerAuthority struct {
	Version   int    `json:"version"`
	Scope     string `json:"scope" toml:"scope"`
	Session   string `json:"session"`
	Actor     string `json:"actor"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	CanWrite  bool   `json:"canWrite"`
	CanManage bool   `json:"canManage"`
}

func explorerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, errors.New("explorer requires HTTPS or loopback HTTP")
	}
	return u, nil
}
func (s *Server) registerExplorerRuntime(public, routes *http.ServeMux) {
	rt := &explorerRuntime{tickets: map[string]*explorerTicket{}, client: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.explorerRuntime = rt
	if file := os.Getenv("HAKOPOD_EXPLORER_TARGETS_FILE"); file != "" {
		data, err := os.ReadFile(file)
		if err == nil && len(data) <= 64<<10 {
			var config struct {
				SchemaVersion int               `toml:"schema_version"`
				Targets       []*explorerTarget `toml:"targets"`
			}
			err = toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config)
			if config.SchemaVersion != 1 {
				err = errors.New("unsupported explorer configuration version")
			}
			rt.targets = config.Targets
		} else {
			err = errors.New("invalid explorer target file")
		}
		if len(rt.targets) > 64 {
			err = errors.New("too many explorer targets")
		}
		seen, addresses, scopes := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, t := range rt.targets {
			if t == nil {
				err = errors.New("invalid explorer target")
				break
			}
			u, e := explorerURL(t.URL)
			if e != nil || u.Path != "/synehq" || !validScope(t.Project, t.Environment) || t.Scope == "" || len(t.Scope) > 512 || seen[t.Project+"/"+t.Environment] || addresses[t.URL] || scopes[t.Scope] {
				err = errors.New("invalid or duplicate explorer target")
				break
			}
			seen[t.Project+"/"+t.Environment], addresses[t.URL], scopes[t.Scope] = true, true, true
			key, e := os.ReadFile(t.KeyFile)
			info, se := os.Stat(t.KeyFile)
			if e != nil || se != nil || info.Mode().Perm()&0077 != 0 {
				err = errors.New("explorer key file must be private")
				break
			}
			t.key = strings.TrimSpace(string(key))
			decoded, e := hex.DecodeString(t.key)
			if e != nil || len(decoded) != 32 || len(t.key) != 64 {
				err = errors.New("invalid explorer control key")
				break
			}
		}
		rt.err = err
	}
	public.HandleFunc("POST /internal/database-explorer/authorize", s.authorizeExplorer)
	routes.HandleFunc("POST /api/v1/database-explorer/http", s.explorerHTTP)
}
func (s *Server) explorerTarget(project, environment string) *explorerTarget {
	if s.explorerRuntime == nil || s.explorerRuntime.err != nil {
		return nil
	}
	for _, t := range s.explorerRuntime.targets {
		if t.Project == project && t.Environment == environment {
			return t
		}
	}
	return nil
}
func explorerAllowed(p store.Principal, t *explorerTarget) bool {
	return p.AllowsDatabase(t.Project, t.Environment, false) && databaseQueryAllowed(p, database.Resource{Project: t.Project, Environment: t.Environment}, true)
}
func explorerFingerprint(d database.Resource) string {
	// Observation timestamps change without a connection change. Bind credentials,
	// private endpoints and TLS trust, while readiness is checked independently.
	if d.Observation.TLS != nil {
		tls := *d.Observation.TLS
		tls.CheckedAt = nil
		tls.Message = ""
		d.Observation.TLS = &tls
	}
	b, _ := json.Marshal(struct {
		ID         string
		Revision   int64
		Credential any
		Endpoints  any
		TLS        any
	}{d.ID, d.Revision, d.EncryptedCredentials, d.Observation.Endpoints, d.Observation.TLS})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type explorerSource struct {
	Source      string         `json:"source"`
	Fingerprint string         `json:"fingerprint"`
	Connection  map[string]any `json:"connection"`
}

func (s *Server) explorerSources(ctx context.Context, p store.Principal, t *explorerTarget) ([]explorerSource, error) {
	if s.Cluster == nil {
		return nil, errors.New("database runtime is unavailable")
	}
	items, err := s.Store.Databases(ctx, p, t.Project, t.Environment)
	if err != nil {
		return nil, err
	}
	result := []explorerSource{}
	for _, item := range items {
		if !database.ExplorerSupported(item.Spec.Engine) {
			continue
		}
		d, err := s.Store.DatabaseInternal(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		observation, err := s.Cluster.ObserveDatabase(ctx, d)
		if err != nil {
			return nil, err
		}
		d.Observation = observation
		if database.ExplorerSource(d, time.Now(), false).State != "eligible" {
			continue
		}
		trust, err := s.Cluster.DatabaseTrust(ctx, d)
		if err != nil {
			return nil, err
		}
		password, err := database.OpenCredentials(s.authEncryptionKey(), d)
		if err != nil {
			return nil, err
		}
		purpose := "read_write"
		if d.Spec.Engine == "clickhouse" {
			purpose = "https"
		}
		if d.Spec.Engine == "mongodb" {
			purpose = "cluster"
		}
		for _, endpoint := range d.Observation.Endpoints {
			if endpoint.Purpose == purpose {
				user, db := "app", "app"
				if d.Spec.Engine == "oracle" {
					user, db = "APP", "FREEPDB1"
					if d.Spec.Oracle != nil && d.Spec.Oracle.Edition == "enterprise" {
						db = "APPDB"
					}
				}
				engine := d.Spec.Engine
				if engine == "postgresql" {
					engine = "postgres"
				}
				fields := map[string]any{"engine": engine, "label": d.Spec.Name, "host": endpoint.Host, "port": endpoint.Port, "database": db, "username": user, "password": string(password), "tlsMode": "verify-full", "tlsCa": trust.CertificatePEM, "readOnly": false}
				if d.Spec.Engine == "oracle" {
					fields["serviceName"] = db
				}
				if d.Spec.Engine == "mongodb" {
					fields["authSource"] = "app"
				}
				result = append(result, explorerSource{d.ID, explorerFingerprint(d), fields})
				break
			}
		}
	}
	return result, nil
}
func (s *Server) authorizeExplorer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version     int    `json:"version"`
		Scope       string `json:"scope" toml:"scope"`
		Ticket      string `json:"ticket"`
		Source      string `json:"source"`
		Fingerprint string `json:"fingerprint"`
		Write       bool   `json:"write"`
		Manage      bool   `json:"manage"`
	}
	if !decode(w, r, &in) {
		return
	}
	rt := s.explorerRuntime
	rt.mu.Lock()
	ticket := rt.tickets[in.Ticket]
	rt.mu.Unlock()
	if in.Version != 1 || ticket == nil || time.Now().After(ticket.expires) || ticket.target.Scope != in.Scope || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+ticket.target.key)) != 1 {
		problem(w, 403, "forbidden", "Explorer authority expired.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, actor, name, email, err := ticket.authorize(ctx)
	if err != nil || !explorerAllowed(p, ticket.target) {
		problem(w, 403, "forbidden", "Explorer access was revoked.")
		return
	}
	canWrite := databaseQueryAllowed(p, database.Resource{Project: ticket.target.Project, Environment: ticket.target.Environment}, false)
	canManage := p.AllowsDatabase(ticket.target.Project, ticket.target.Environment, true)
	if (in.Write && !canWrite) || (in.Manage && !canManage) {
		problem(w, 403, "forbidden", "The operation is not permitted.")
		return
	}
	if in.Source != "" {
		d, err := s.Store.DatabaseInternal(ctx, in.Source)
		if err != nil || d.Project != ticket.target.Project || d.Environment != ticket.target.Environment || !databaseQueryAllowed(p, d, !in.Write) || s.Cluster == nil {
			problem(w, 403, "forbidden", "The database is unavailable.")
			return
		}
		observation, err := s.Cluster.ObserveDatabase(ctx, d)
		d.Observation = observation
		if err != nil || database.ExplorerSource(d, time.Now(), canWrite).State != "eligible" || explorerFingerprint(d) != in.Fingerprint {
			problem(w, 409, "source_changed", "Refresh the database connection.")
			return
		}
	}
	write(w, 200, explorerAuthority{1, in.Scope, ticket.session, actor, name, email, canWrite, canManage})
}
func (s *Server) explorerHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string `json:"project" toml:"project"`
		Environment string `json:"environment" toml:"environment"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		Body        string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	t := s.explorerTarget(in.Project, in.Environment)
	if t == nil {
		problem(w, 503, "explorer_unavailable", "The explorer is not configured for this scope.")
		return
	}
	p, err := s.freshRuntimePrincipal(r)
	if err != nil || !explorerAllowed(p, t) {
		problem(w, 404, "not_found", "Resource not found.")
		return
	}
	cloudTicket := r.Header.Get("X-Hakopod-Explorer-Authority")
	var external explorerCloudIdentity
	if p.CredentialType == "machine" && !p.RuntimeScoped {
		p, external, err = s.explorerCloudPrincipal(r.Context(), p, t, cloudTicket)
		if err != nil || !explorerAllowed(p, t) {
			problem(w, 403, "forbidden", "The explorer requires a verified human session.")
			return
		}
	} else if p.CredentialType != "browser" {
		problem(w, 403, "forbidden", "Open the explorer with your browser session.")
		return
	}
	if !explorerPublicPath(in.Method, in.Path) || len(in.Body) > 256<<10 {
		problem(w, 400, "invalid_request", "Invalid explorer request.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	baseContext := context.WithoutCancel(r.Context())
	keyID := p.KeyID
	authorize := func(ctx context.Context) (store.Principal, string, string, string, error) {
		// Preserve only the trusted runtime scope; use the new request deadline.
		if scope, ok := baseContext.Value(runtimeScopeKey{}).(RuntimeScope); ok {
			ctx = context.WithValue(ctx, runtimeScopeKey{}, scope)
		}
		latest, e := s.runtimePrincipalForKey(ctx, keyID)
		if e != nil {
			return latest, "", "", "", e
		}
		if external.Actor != "" {
			var current explorerCloudIdentity
			latest, current, e = s.explorerCloudPrincipal(ctx, latest, t, cloudTicket)
			if e != nil || current.Actor != external.Actor || current.Session != external.Session {
				return latest, "", "", "", store.ErrForbidden
			}
			return latest, current.Actor, current.Name, current.Email, nil
		}
		return latest, latest.ID, latest.Name, latest.Email, e
	}
	sessionMAC := hmac.New(sha256.New, []byte(t.key))
	sessionMAC.Write([]byte(p.ID + "\x00" + p.KeyID + "\x00" + external.Actor + "\x00" + external.Session))
	session := hex.EncodeToString(sessionMAC.Sum(nil))
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		failure(w, err)
		return
	}
	token := hex.EncodeToString(tokenBytes)
	rt := s.explorerRuntime
	rt.mu.Lock()
	for id, entry := range rt.tickets {
		if time.Now().After(entry.expires) {
			delete(rt.tickets, id)
		}
	}
	for id, entry := range rt.tickets {
		if entry.session == session && entry.target == t {
			token = id
			break
		}
	}
	if len(rt.tickets) >= 1024 && rt.tickets[token] == nil {
		rt.mu.Unlock()
		problem(w, 429, "busy", "Too many explorer sessions.")
		return
	}
	rt.tickets[token] = &explorerTicket{t, time.Now().Add(30 * time.Minute), session, authorize}
	rt.mu.Unlock()
	// Serialize a complete source snapshot with its delivery. OOS rejects old source
	// fingerprints again at credential resolution, including after a broker restart.
	t.mu.Lock()
	if time.Now().After(t.nextSync) {
		sources, syncErr := s.explorerSources(ctx, p, t)
		err = syncErr
		if err == nil {
			var payload []byte
			payload, err = json.Marshal(map[string]any{"version": 1, "scope": t.Scope, "connections": sources})
			if err == nil {
				_, err = s.explorerFetch(ctx, t, "POST", "/internal/hakopod/sync", payload, "")
			}
		}
		if err == nil {
			t.nextSync = time.Now().Add(10 * time.Second)
		}
	}
	t.mu.Unlock()
	if err != nil {
		problem(w, 503, "explorer_sync_failed", "Database connections could not be synchronized.")
		return
	}
	response, err := s.explorerFetch(ctx, t, in.Method, in.Path, []byte(in.Body), token)
	if err != nil {
		problem(w, 503, "explorer_unavailable", "The explorer did not respond.")
		return
	}
	if latest, _, _, _, e := authorize(ctx); e != nil || !explorerAllowed(latest, t) {
		problem(w, 403, "forbidden", "Explorer access was revoked.")
		return
	}
	write(w, 200, response)
}

type explorerResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
}

func (s *Server) explorerFetch(ctx context.Context, t *explorerTarget, method, path string, body []byte, ticket string) (explorerResponse, error) {
	u, _ := url.Parse(t.URL)
	origin := u.Scheme + "://" + u.Host
	req, err := http.NewRequestWithContext(ctx, method, t.URL+path, bytes.NewReader(body))
	if err != nil {
		return explorerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	if ticket == "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	} else {
		req.Header.Set("X-Hakopod-Explorer-Ticket", ticket)
	}
	resp, err := s.explorerRuntime.client.Do(req)
	if err != nil {
		return explorerResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (12<<20)+1))
	if err != nil || len(data) > 12<<20 {
		return explorerResponse{}, errors.New("explorer response exceeds limit")
	}
	if ticket == "" && resp.StatusCode != 200 {
		return explorerResponse{}, fmt.Errorf("explorer sync rejected: %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return explorerResponse{}, errors.New("explorer redirects are not allowed")
	}
	return explorerResponse{resp.StatusCode, resp.Header.Get("Content-Type"), data}, nil
}
func explorerPublicPath(method, path string) bool {
	if len(path) > 2048 || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n?#%") || strings.Contains(path, "//") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	if strings.HasPrefix(path, "/internal") || strings.HasPrefix(path, "/api/auth") {
		return false
	}
	switch method {
	case "GET", "HEAD":
		return true
	case "POST", "PUT", "PATCH", "DELETE":
		return strings.HasPrefix(path, "/api/") && path != "/api/setup"
	}
	return false
}
