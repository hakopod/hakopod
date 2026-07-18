package api

import "net/http"

func (s *Server) registerLicenseRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/license", s.getLicense)
	routes.HandleFunc("PUT /api/v1/license", s.setLicense)
	routes.HandleFunc("DELETE /api/v1/license", s.removeLicense)
}
func (s *Server) getLicense(w http.ResponseWriter, r *http.Request) {
	status, err := s.Store.LicenseStatus(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, status)
}
func (s *Server) setLicense(w http.ResponseWriter, r *http.Request) {
	var in struct {
		License          string `json:"license"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "review_required", "provide the current license revision")
		return
	}
	status, err := s.Store.InstallLicense(r.Context(), who(r), in.License, *in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, status)
}
func (s *Server) removeLicense(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "review_required", "provide the current license revision")
		return
	}
	status, err := s.Store.RemoveLicense(r.Context(), who(r), *in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, status)
}
