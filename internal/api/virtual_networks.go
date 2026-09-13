package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/pelletier/go-toml/v2"
)

func (s *Server) registerVirtualNetworkRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/virtual-networks", s.virtualNetworks)
	m.HandleFunc("GET /api/v1/virtual-networks/{name}", s.virtualNetwork)
	m.HandleFunc("GET /api/v1/virtual-networks/{name}/candidates", s.virtualNetworkCandidates)
	m.HandleFunc("POST /api/v1/virtual-networks/plan", s.planVirtualNetwork)
	m.HandleFunc("POST /api/v1/virtual-networks", s.putVirtualNetwork)
	m.HandleFunc("PUT /api/v1/virtual-networks/{name}", s.putVirtualNetwork)
	m.HandleFunc("DELETE /api/v1/virtual-networks/{name}", s.deleteVirtualNetwork)
}

type virtualNetworkSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Description string    `json:"description"`
	Segments    []string  `json:"segments"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func virtualNetworkView(row store.RuntimeResource) (virtualNetworkSummary, store.VirtualNetworkMetadata, error) {
	var data store.VirtualNetworkMetadata
	if json.Unmarshal(row.Metadata, &data) != nil || data.ID == "" {
		return virtualNetworkSummary{}, data, errors.New("virtual network configuration is unavailable")
	}
	names := make([]string, 0, len(data.Spec.Segments))
	for name := range data.Spec.Segments {
		names = append(names, name)
	}
	sort.Strings(names)
	return virtualNetworkSummary{ID: data.ID, Name: row.Name, Project: row.Project, Environment: row.Environment, Description: data.Spec.Description, Segments: names, Revision: row.Revision, UpdatedAt: row.UpdatedAt}, data, nil
}

func networkScope(w http.ResponseWriter, r *http.Request, project, environment string, manage bool) bool {
	if !validScope(project, environment) {
		problem(w, 400, "invalid_scope", "project and environment are required")
		return false
	}
	p := who(r)
	if !p.Allows("deployments:read", project, environment, "") || manage && !p.CanManageVirtualNetworks(project, environment) {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}

func (s *Server) virtualNetworks(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !networkScope(w, r, p, e, false) {
		return
	}
	rows, err := s.Store.RuntimeResources(r.Context(), "virtual-network", p, e)
	if err != nil {
		failure(w, err)
		return
	}
	items := []virtualNetworkSummary{}
	for _, row := range rows {
		view, _, err := virtualNetworkView(row)
		if err != nil {
			failure(w, err)
			return
		}
		items = append(items, view)
	}
	write(w, 200, map[string]any{"items": items, "can_manage": who(r).CanManageVirtualNetworks(p, e)})
}

type networkConnection struct {
	ApplicationID string      `json:"application_id"`
	Application   string      `json:"application"`
	Service       string      `json:"service"`
	Network       string      `json:"network"`
	Segment       string      `json:"segment"`
	Address       string      `json:"address"`
	Ports         []spec.Port `json:"ports"`
	Revision      int64       `json:"revision"`
	Status        string      `json:"status"`
}

func (s *Server) virtualNetworkCandidates(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !networkScope(w, r, p, e, false) {
		return
	}
	resource, err := s.Store.RuntimeResource(r.Context(), "virtual-network", p, e, r.PathValue("name"))
	if err != nil {
		failure(w, err)
		return
	}
	_, data, err := virtualNetworkView(resource)
	if err != nil {
		failure(w, err)
		return
	}
	allowed := []string{}
	for _, segment := range data.Spec.Segments {
		allowed = append(allowed, segment.Applications...)
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT id,name,revision,ARRAY(SELECT jsonb_object_keys(spec->'services') ORDER BY 1)
 FROM applications WHERE project=$1 AND environment=$2 AND name=ANY($3::text[]) ORDER BY name LIMIT 200`, p, e, allowed)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type candidate struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Revision int64    `json:"revision"`
		Services []string `json:"services"`
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.ID, &item.Name, &item.Revision, &item.Services); err != nil {
			failure(w, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}

func (s *Server) virtualNetwork(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !networkScope(w, r, p, e, false) {
		return
	}
	row, err := s.Store.RuntimeResource(r.Context(), "virtual-network", p, e, r.PathValue("name"))
	if err != nil {
		failure(w, err)
		return
	}
	view, data, err := virtualNetworkView(row)
	if err != nil {
		failure(w, err)
		return
	}
	// Return just connection fields, never entire application specs or env values.
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT a.id,a.name,s.key,n.key,n.value->>'segment',a.revision,a.status,
 COALESCE((s.value->>'port')::integer,0),COALESCE(s.value->'ports','[]'::jsonb)
 FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') s
 CROSS JOIN LATERAL jsonb_each(a.spec->'networks') n
 WHERE a.project=$1 AND a.environment=$2 AND n.value->>'virtual_network'=$3
 AND (s.value->'networks') ? n.key ORDER BY a.name,s.key,n.key LIMIT 257`, p, e, row.Name)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	connections := []networkConnection{}
	truncated := false
	for rows.Next() {
		if len(connections) == 256 {
			truncated = true
			break
		}
		var item networkConnection
		var port int32
		var extra []spec.Port
		if err = rows.Scan(&item.ApplicationID, &item.Application, &item.Service, &item.Network, &item.Segment, &item.Revision, &item.Status, &port, &extra); err != nil {
			failure(w, err)
			return
		}
		item.Address = item.Service + "." + cluster.Namespace(item.ApplicationID) + ".svc.cluster.local"
		item.Ports = spec.ServicePorts(spec.Service{Port: port, Ports: extra})
		connections = append(connections, item)
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	canonical, _ := toml.Marshal(data.Spec)
	write(w, 200, map[string]any{"network": view, "spec": data.Spec, "toml": string(canonical), "connections": connections, "truncated": truncated, "can_manage": who(r).CanManageVirtualNetworks(p, e)})
}

type virtualNetworkInput struct {
	Project          string               `json:"project"`
	Environment      string               `json:"environment"`
	Spec             *spec.VirtualNetwork `json:"spec,omitempty"`
	TOML             string               `json:"toml,omitempty"`
	ExpectedID       string               `json:"expected_id,omitempty"`
	ExpectedRevision *int64               `json:"expected_revision,omitempty"`
}

func validVirtualNetworkExpectation(in virtualNetworkInput) bool {
	if in.ExpectedRevision == nil {
		return in.ExpectedID == ""
	}
	if *in.ExpectedRevision == 0 {
		return in.ExpectedID == ""
	}
	return *in.ExpectedRevision > 0 && len(in.ExpectedID) == 32
}

func parseVirtualNetworkInput(in virtualNetworkInput) (spec.VirtualNetwork, error) {
	if (in.Spec == nil) == (in.TOML == "") {
		return spec.VirtualNetwork{}, errors.New("provide exactly one of spec or toml")
	}
	if in.Spec != nil {
		return spec.NormalizeVirtualNetwork(*in.Spec)
	}
	return spec.ParseVirtualNetwork([]byte(in.TOML))
}

func (s *Server) planVirtualNetwork(w http.ResponseWriter, r *http.Request) {
	var in virtualNetworkInput
	if !decode(w, r, &in) || !networkScope(w, r, in.Project, in.Environment, true) {
		return
	}
	if !validVirtualNetworkExpectation(in) {
		problem(w, 400, "invalid_revision", "supply expected_id and expected_revision from the same reviewed network")
		return
	}
	next, err := parseVirtualNetworkInput(in)
	if err != nil {
		problem(w, 400, "invalid_network", err.Error())
		return
	}
	row, err := s.Store.RuntimeResource(r.Context(), "virtual-network", in.Project, in.Environment, next.Name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	var previous *spec.VirtualNetwork
	expectedID := ""
	if row.Revision > 0 {
		_, old, err := virtualNetworkView(row)
		if err != nil {
			failure(w, err)
			return
		}
		previous = &old.Spec
		expectedID = old.ID
	}
	if in.ExpectedRevision != nil && (*in.ExpectedRevision != row.Revision || in.ExpectedID != expectedID) {
		failure(w, store.ErrConflict)
		return
	}
	if err := s.Store.CheckVirtualNetworkChange(r.Context(), in.Project, in.Environment, next); err != nil {
		failure(w, err)
		return
	}
	canonical, _ := toml.Marshal(next)
	write(w, 200, map[string]any{"spec": next, "previous": previous, "toml": string(canonical), "expected_id": expectedID, "expected_revision": row.Revision})
}

func (s *Server) putVirtualNetwork(w http.ResponseWriter, r *http.Request) {
	var in virtualNetworkInput
	if !decode(w, r, &in) || !networkScope(w, r, in.Project, in.Environment, true) {
		return
	}
	next, err := parseVirtualNetworkInput(in)
	if err != nil {
		problem(w, 400, "invalid_network", err.Error())
		return
	}
	if in.ExpectedRevision == nil || !validVirtualNetworkExpectation(in) || r.Method == "POST" && *in.ExpectedRevision != 0 || r.Method == "PUT" && (next.Name != r.PathValue("name") || *in.ExpectedRevision == 0) {
		problem(w, 400, "invalid_revision", "review a plan and supply expected_id and expected_revision; network names must agree")
		return
	}
	metadata := store.VirtualNetworkMetadata{ID: store.NewID(), Spec: next}
	if r.Method == "PUT" {
		metadata.ID = in.ExpectedID
	}
	row, err := s.Store.PutRuntimeResource(r.Context(), who(r), "virtual-network", in.Project, in.Environment, next.Name, *in.ExpectedRevision, metadata)
	if err != nil {
		failure(w, err)
		return
	}
	view, _, _ := virtualNetworkView(row)
	status := http.StatusOK
	if r.Method == "POST" {
		status = http.StatusCreated
	}
	write(w, status, view)
}

func (s *Server) deleteVirtualNetwork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project          string `json:"project"`
		Environment      string `json:"environment"`
		ExpectedID       string `json:"expected_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		Confirmation     string `json:"confirmation"`
	}
	if !decode(w, r, &in) || !networkScope(w, r, in.Project, in.Environment, true) {
		return
	}
	if in.ExpectedRevision < 1 || len(in.ExpectedID) != 32 || in.Confirmation != r.PathValue("name") {
		problem(w, 400, "confirmation_required", "confirm the network name and supply the reviewed ID and revision")
		return
	}
	if err := s.Store.DeleteVirtualNetwork(r.Context(), who(r), in.Project, in.Environment, in.Confirmation, in.ExpectedID, in.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
