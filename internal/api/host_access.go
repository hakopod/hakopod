package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/remotecommand"
)

func (s *Server) registerHostRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/host-access", s.hostAccess)
	m.HandleFunc("PUT /api/v1/host-access/{user}", s.grantHostAccess)
	m.HandleFunc("DELETE /api/v1/host-access/{user}/{node}", s.revokeHostAccess)
	m.HandleFunc("POST /api/v1/nodes/{node}/terminal", s.createHostTerminal)
	m.HandleFunc("GET /api/v1/nodes/{node}/terminal/{session}/output", s.terminalOutput)
	m.HandleFunc("POST /api/v1/nodes/{node}/terminal/{session}/input", s.terminalInput)
	m.HandleFunc("DELETE /api/v1/nodes/{node}/terminal/{session}", s.deleteTerminal)
}
func (s *Server) hostAccess(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	grants, err := s.Store.HostGrants(r.Context(), p)
	if err != nil {
		profileError(w, err)
		return
	}
	type node struct {
		Name         string `json:"name"`
		ControlPlane bool   `json:"control_plane"`
		Ready        bool   `json:"ready"`
		Allowed      bool   `json:"allowed"`
	}
	nodes := []node{}
	if s.Cluster != nil {
		observed, err := s.Cluster.Nodes(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		for _, n := range observed {
			if p.IsAdmin() || p.CanHostTerminal(n.Name) {
				nodes = append(nodes, node{n.Name, n.ControlPlane, n.Ready, p.CanHostTerminal(n.Name)})
			}
		}
	}
	write(w, 200, map[string]any{"super_admin": p.IsSuperAdmin(), "grants": grants, "nodes": nodes})
}
func (s *Server) grantHostAccess(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Node       string    `json:"node"`
		Permission string    `json:"permission"`
		ExpiresAt  time.Time `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !who(r).IsSuperAdmin() {
		failure(w, store.ErrForbidden)
		return
	}
	if in.Node != "*" {
		if s.Cluster == nil {
			problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
			return
		}
		if _, err := s.Cluster.HostNode(r.Context(), in.Node); err != nil {
			problem(w, 400, "node_unavailable", "Select a ready Linux node")
			return
		}
	}
	g, err := s.Store.SetHostGrant(r.Context(), who(r), store.HostGrant{IdentityID: r.PathValue("user"), Node: in.Node, Permission: in.Permission, ExpiresAt: in.ExpiresAt})
	if err != nil {
		profileError(w, err)
		return
	}
	write(w, 200, g)
}
func (s *Server) revokeHostAccess(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeHostGrant(r.Context(), who(r), r.PathValue("user"), r.PathValue("node")); err != nil {
		profileError(w, err)
		return
	}
	write(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) createHostTerminal(w http.ResponseWriter, r *http.Request) {
	p, node := who(r), r.PathValue("node")
	if !p.CanHostTerminal(node) {
		problem(w, 403, "host_access_required", "Host terminal access requires an explicit grant from the super-admin")
		return
	}
	var in struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Cols == 0 {
		in.Cols = 100
	}
	if in.Rows == 0 {
		in.Rows = 30
	}
	if in.Cols < 20 || in.Cols > 400 || in.Rows < 5 || in.Rows > 200 {
		problem(w, 400, "invalid_size", "Terminal size must be 20–400 columns and 5–200 rows")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
		return
	}
	uid, err := s.Cluster.HostNode(r.Context(), node)
	if err != nil {
		problem(w, 409, "node_unavailable", "Select a ready Linux node")
		return
	}
	expires := time.Now().Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	x := &terminalSession{id: store.NewID(), owner: p.ID, key: p.KeyID, hostNode: node, uid: uid, options: cluster.TerminalOptions{Cols: in.Cols, Rows: in.Rows}, expires: expires, lastInput: time.Now(), ctx: ctx, cancel: cancel, input: make(chan []byte, 8), sizes: make(chan remotecommand.TerminalSize, 1)}
	x.sizes <- remotecommand.TerminalSize{Width: in.Cols, Height: in.Rows}
	s.terminalMu.Lock()
	if len(s.terminals) >= 4 {
		s.terminalMu.Unlock()
		cancel()
		problem(w, 429, "terminal_limit", "Four terminal sessions are already open; close one and retry")
		return
	}
	s.terminals[x.id] = x
	x.timer = time.AfterFunc(30*time.Second, func() {
		s.terminalMu.Lock()
		defer s.terminalMu.Unlock()
		current := s.terminals[x.id]
		if current == nil {
			return
		}
		current.mu.Lock()
		started := current.started
		current.mu.Unlock()
		if !started {
			current.cancel()
			delete(s.terminals, x.id)
		}
	})
	s.terminalMu.Unlock()
	_, err = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'terminal.host.open',$3,$4)", p.ID, p.KeyID, node, store.JSON(map[string]string{"session": x.id, "node_uid": string(uid)}))
	if err != nil {
		s.closeTerminal(x)
		failure(w, err)
		return
	}
	write(w, 201, map[string]any{"id": x.id, "expires_at": expires, "node": node})
}
func (s *Server) hostTerminalFor(w http.ResponseWriter, r *http.Request) (*terminalSession, store.Application, bool) {
	p, node := who(r), r.PathValue("node")
	if !p.CanHostTerminal(node) {
		failure(w, store.ErrForbidden)
		return nil, store.Application{}, false
	}
	s.terminalMu.Lock()
	x := s.terminals[r.PathValue("session")]
	s.terminalMu.Unlock()
	if x == nil || x.hostNode != node || x.owner != p.ID || x.key != p.KeyID || x.ctx.Err() != nil {
		problem(w, 404, "terminal_expired", "Terminal is closed or belongs to another session; reconnect")
		return nil, store.Application{}, false
	}
	return x, store.Application{}, true
}
func (s *Server) guardHostTerminal(x *terminalSession) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-x.ctx.Done():
			return
		case <-ticker.C:
		}
		check, done := context.WithTimeout(x.ctx, 2*time.Second)
		p, err := s.Store.KeyPrincipal(check, x.key)
		if err == nil && p.ID == x.owner && p.CanHostTerminal(x.hostNode) {
			uid, nodeErr := s.Cluster.HostNode(check, x.hostNode)
			if nodeErr != nil || uid != x.uid {
				err = store.ErrForbidden
			}
		} else {
			err = store.ErrForbidden
		}
		done()
		if err != nil {
			x.cancel()
			return
		}
	}
}
