package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
)

type appLogSource struct {
	applogs.PodRecord
	Current bool `json:"current"`
}

// listLogSources lists current and past pods that served the application.
func (s *Server) listLogSources(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "logs:read")
	if !ok {
		return
	}
	settings, available, message, err := s.historyStatus(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	now := time.Now().UTC()
	retention := time.Duration(settings.RetentionDays) * 24 * time.Hour
	from, to, limit := now.Add(-retention), now, 200
	q := r.URL.Query()
	for name, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				problem(w, 400, "invalid_query_options", name+" must be an RFC 3339 time")
				return
			}
			*dst = t.UTC()
		}
	}
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 500 {
			problem(w, 400, "invalid_query_options", "limit must be between 1 and 500")
			return
		}
	}
	if !to.After(from) {
		problem(w, 400, "invalid_query_options", "to must be later than from")
		return
	}
	if from.Before(now.Add(-retention - time.Minute)) {
		problem(w, 400, "invalid_query_options", fmt.Sprintf("from cannot be more than %d days in the past, the retention window", settings.RetentionDays))
		return
	}
	out := map[string]any{"items": []appLogSource{}, "retention_days": settings.RetentionDays, "available": available, "message": message}
	if available {
		pods, err := s.Store.LogPods(r.Context(), a.ID, from, to, limit)
		if err != nil {
			failure(w, err)
			return
		}
		items := make([]appLogSource, 0, len(pods))
		for _, p := range pods {
			p.StartedAt = p.StartedAt.UTC()
			if p.EndedAt != nil {
				e := p.EndedAt.UTC()
				p.EndedAt = &e
			}
			items = append(items, appLogSource{PodRecord: p, Current: p.EndedAt == nil})
		}
		out["items"] = items
	}
	write(w, 200, out)
}
