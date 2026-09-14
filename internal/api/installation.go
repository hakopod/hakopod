package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"
)

func (s *Server) installationOwner(w http.ResponseWriter, r *http.Request) bool {
	mode, err := cluster.ParseDeploymentMode(s.Auth.DeploymentMode)
	if err != nil || mode != cluster.DeploymentSelfHosted || !who(r).IsSuperAdmin() {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}
func (s *Server) registerInstallationRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/installation/status", s.installationStatus)
	m.HandleFunc("GET /api/v1/installation/logs", s.installationLogs)
	m.HandleFunc("POST /api/v1/installation/logs/query", s.queryInstallationLogs)
	m.HandleFunc("GET /api/v1/installation/setup", s.installationSetup)
	m.HandleFunc("POST /api/v1/installation/upgrade", s.installationUpgrade)
}
func (s *Server) maintenanceClient() *http.Client {
	client := s.maintenanceHTTP
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", "/run/hakopod-maintenance/control.sock")
		}, DisableKeepAlives: true}}
	}
	return client
}
func (s *Server) maintenance(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	client := s.maintenanceClient()
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(r.Context(), method, "http://maintenance"+path, bytes.NewReader(body))
	if err != nil {
		failure(w, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		problem(w, 503, "maintenance_unavailable", "The installation maintenance service is unavailable. See Infrastructure > Setup for administrator instructions.")
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
	if err != nil || len(data) > 512<<10 || !json.Valid(data) {
		problem(w, 503, "maintenance_unavailable", "The maintenance service could not complete this request. Inspect its status on the server.")
		return
	}
	if response.StatusCode != 200 {
		problem(w, 409, "maintenance_failed", "Maintenance could not start. Check the installation status before retrying.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if body != nil {
		w.WriteHeader(http.StatusAccepted)
	}
	_, _ = w.Write(data)
}
func (s *Server) installationStatus(w http.ResponseWriter, r *http.Request) {
	if s.installationOwner(w, r) {
		s.maintenance(w, r, "/status", nil)
	}
}
func (s *Server) installationLogs(w http.ResponseWriter, r *http.Request) {
	if s.installationOwner(w, r) {
		snapshot, err := s.installationLogSnapshot(r.Context())
		if err != nil {
			problem(w, 503, "maintenance_unavailable", "API logs are unavailable. See Infrastructure > Setup for administrator instructions.")
			return
		}
		write(w, http.StatusOK, snapshot)
	}
}
func (s *Server) installationSetup(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Cluster configuration is unavailable")
		return
	}
	result, err := s.Cluster.InstallationSetup(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}

var upgradeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`)

func (s *Server) installationUpgrade(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	var in struct {
		Version      string `json:"version"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Version) > 64 || !upgradeVersion.MatchString(in.Version) || in.Confirmation != "upgrade "+in.Version {
		problem(w, 400, "confirmation_required", "Review the backup and restart steps, then confirm the target version.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'installation.upgrade.request','installation',$3)", who(r).ID, who(r).KeyID, store.JSON(map[string]string{"version": in.Version})); err != nil {
		failure(w, err)
		return
	}
	body, _ := json.Marshal(map[string]string{"version": in.Version})
	s.maintenance(w, r, "/upgrade", body)
}
