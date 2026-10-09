package api

import (
	"net/http"
)

func (s *Server) resumeDeployment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	d, err := s.Store.ResumeDeployment(r.Context(), who(r), r.PathValue("id"), in.ExpectedRevision, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, d)
}
