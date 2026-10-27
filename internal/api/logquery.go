package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/logquery"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/apimachinery/pkg/util/validation"
)

// logQueryRequest adds the history fields to the live query. With history
// false or absent only the embedded live options are used.
type logQueryRequest struct {
	cluster.LogQueryOptions
	History bool       `json:"history,omitempty"`
	From    *time.Time `json:"from,omitempty"`
	To      *time.Time `json:"to,omitempty"`
	PodUID  string     `json:"pod_uid,omitempty"`
	Cursor  string     `json:"cursor,omitempty"`
}

func (s *Server) queryLogs(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "logs:read")
	if !ok {
		return
	}
	var req logQueryRequest
	if !decode(w, r, &req) {
		return
	}
	options := req.LogQueryOptions
	if _, ok = a.Spec.Services[options.Service]; !ok {
		problem(w, 400, "invalid_service", "select an application service")
		return
	}
	if req.History {
		s.queryHistory(w, r, a, req)
		return
	}
	if err := options.Validate(); err != nil {
		problem(w, 400, "invalid_query_options", err.Error())
		return
	}
	match, err := logquery.Compile(options.Query)
	if err != nil {
		problem(w, 400, "invalid_log_query", err.Error())
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
		return
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	go s.guardStream(ctx, cancel, who(r).KeyID, a, "logs:read")
	result, err := s.Cluster.QueryLogs(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, options, match)
	if err != nil {
		problem(w, 503, "logs_unavailable", "No readable logs for the selected pods and containers; inspect runtime events or narrow the selection")
		return
	}
	s.writeLogResult(ctx, w, r, a, result)
}

// writeLogResult sends a buffered result only if the reader still has authority.
func (s *Server) writeLogResult(ctx context.Context, w http.ResponseWriter, r *http.Request, a store.Application, result any) {
	p, err := s.Store.KeyPrincipal(ctx, who(r).KeyID)
	if err != nil || !p.Allows("logs:read", a.Project, a.Environment, a.Name) {
		problem(w, 403, "forbidden", "log access was revoked")
		return
	}
	writes := guardResponseWrites(ctx, w, 10*time.Second)
	defer writes.stop()
	if !writes.begin() {
		return
	}
	write(w, 200, result)
}

var podUIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

const (
	maxHistoryGaps   = 50
	maxHistoryCursor = 512
)

type historyEntry struct {
	logquery.Entry
	PodUID    string `json:"pod_uid"`
	Revision  int64  `json:"revision"`
	Restart   int    `json:"restart"`
	Stream    string `json:"stream"`
	Truncated bool   `json:"truncated"`
}

type historyGap struct {
	PodUID  string `json:"pod_uid"`
	PodName string `json:"pod_name"`
	Seconds int    `json:"seconds"`
	Note    string `json:"note"`
}

type historyInfo struct {
	Available        bool         `json:"available"`
	Message          string       `json:"message"`
	CollectedThrough *time.Time   `json:"collected_through"`
	ScanLimited      bool         `json:"scan_limited"`
	Gaps             []historyGap `json:"gaps"`
	RetentionDays    int          `json:"retention_days"`
}

type historyResult struct {
	Entries       []historyEntry      `json:"entries"`
	Histogram     []cluster.LogBucket `json:"histogram"`
	Scanned       int                 `json:"scanned"`
	Matched       int                 `json:"matched"`
	Truncated     bool                `json:"truncated"`
	Pods          int                 `json:"pods"`
	WindowSeconds int64               `json:"window_seconds"`
	Warnings      []string            `json:"warnings"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	History       historyInfo         `json:"history"`
}

// appLogReader builds the segment reader once. Only the collector writes to
// the same root, so a second instance is safe.
func (s *Server) appLogReader() *applogs.Store {
	s.appLogsOnce.Do(func() {
		if s.appLogs == nil {
			s.appLogs = &applogs.Store{Index: s.Store}
		}
	})
	return s.appLogs
}

// historyStatus says whether stored logs can be read right now and why not.
func (s *Server) historyStatus(ctx context.Context) (settings store.AppLogSettings, available bool, message string, err error) {
	settings, _, err = s.Store.AppLogSettings(ctx)
	if err != nil {
		return settings, false, "", err
	}
	if !settings.Enabled {
		return settings, false, "Log history is turned off in installation settings.", nil
	}
	if s.appLogReader().Available() != nil {
		return settings, false, "Log history is not set up on this server yet. Run installer.sh --upgrade to enable it.", nil
	}
	return settings, true, "", nil
}

// historyWindow validates from, to and the paging fields against retention.
func historyWindow(req logQueryRequest, now time.Time, retentionDays int) (from, to time.Time, limit int, msg string) {
	retention := time.Duration(retentionDays) * 24 * time.Hour
	to = now
	if req.To != nil {
		to = req.To.UTC()
	}
	switch {
	case req.From != nil:
		from = req.From.UTC()
	case req.SinceSeconds < 0 || time.Duration(req.SinceSeconds)*time.Second > retention:
		return from, to, 0, fmt.Sprintf("since_seconds must be between 1 and %d seconds, the retention window", retentionDays*86400)
	case req.SinceSeconds > 0:
		from = to.Add(-time.Duration(req.SinceSeconds) * time.Second)
	default:
		from = to.Add(-time.Hour)
	}
	switch {
	case !to.After(from):
		return from, to, 0, "to must be later than from"
	case to.Sub(from) > retention:
		return from, to, 0, fmt.Sprintf("the time range can be at most %d days, the retention window", retentionDays)
	case from.Before(now.Add(-retention - time.Minute)):
		return from, to, 0, fmt.Sprintf("from cannot be more than %d days in the past, the retention window", retentionDays)
	case len(req.Cursor) > maxHistoryCursor:
		return from, to, 0, "cursor is too long"
	case req.Cursor != "" && !validCursor(req.Cursor):
		return from, to, 0, "the log cursor is not valid; start the query again without it"
	case req.PodUID != "" && !podUIDPattern.MatchString(req.PodUID):
		return from, to, 0, "pod_uid must be 1 to 64 letters, digits or hyphens"
	case req.Limit < 0 || req.Limit > applogs.MaxQueryLines:
		return from, to, 0, fmt.Sprintf("limit must be between 1 and %d", applogs.MaxQueryLines)
	case len(req.Pod) > 253 || req.Pod != "" && len(validation.IsDNS1123Subdomain(req.Pod)) > 0:
		return from, to, 0, "select a valid pod name"
	case len(req.Container) > 63 || req.Container != "" && len(validation.IsDNS1123Label(req.Container)) > 0:
		return from, to, 0, "select a valid container name"
	}
	limit = req.Limit
	if limit == 0 {
		limit = 500
	}
	return from, to, limit, ""
}

// validCursor checks the opaque cursor's shape (base64url JSON) so bad input is a 400, not a read failure.
func validCursor(c string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && json.Valid(raw)
}

// collectedThrough is the newest time any stored segment of the app reaches.
func (s *Server) collectedThrough(ctx context.Context, appID string, now time.Time, retention time.Duration) (*time.Time, error) {
	// ponytail: a quiet app falls back to the oldest 500 segments in retention; fine until an app holds more than that and is silent for 10 minutes.
	var newest *time.Time
	for _, from := range []time.Time{now.Add(-10 * time.Minute), now.Add(-retention)} {
		segments, err := s.Store.Segments(ctx, applogs.SegmentQuery{ApplicationID: appID, From: from, To: now, Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, g := range segments {
			if newest == nil || g.LastTime.After(*newest) {
				t := g.LastTime.UTC()
				newest = &t
			}
		}
		if newest != nil {
			break
		}
	}
	return newest, nil
}

func (s *Server) queryHistory(w http.ResponseWriter, r *http.Request, a store.Application, req logQueryRequest) {
	if _, ok := a.Spec.Services[req.Service]; !ok {
		problem(w, 400, "invalid_service", "select an application service")
		return
	}
	match, err := logquery.Compile(req.Query)
	if err != nil {
		problem(w, 400, "invalid_log_query", err.Error())
		return
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	go s.guardStream(ctx, cancel, who(r).KeyID, a, "logs:read")
	settings, available, message, err := s.historyStatus(ctx)
	if err != nil {
		failure(w, err)
		return
	}
	now := time.Now().UTC()
	from, to, limit, bad := historyWindow(req, now, settings.RetentionDays)
	if bad != "" {
		problem(w, 400, "invalid_query_options", bad)
		return
	}
	out := historyResult{Entries: []historyEntry{}, Histogram: []cluster.LogBucket{}, WindowSeconds: int64(to.Sub(from) / time.Second), Warnings: []string{},
		History: historyInfo{Available: available, Message: message, Gaps: []historyGap{}, RetentionDays: settings.RetentionDays}}
	if available {
		if err = s.fillHistory(ctx, &out, a, req, match, from, to, limit, now); err != nil {
			problem(w, 503, "logs_unavailable", "Stored logs could not be read; retry shortly")
			return
		}
	}
	s.writeLogResult(ctx, w, r, a, out)
}

func (s *Server) fillHistory(ctx context.Context, out *historyResult, a store.Application, req logQueryRequest, match logquery.Predicate, from, to time.Time, limit int, now time.Time) error {
	// The application ID always comes from the authorized application.
	q := applogs.ReadQuery{ApplicationID: a.ID, From: from, To: to, Services: []string{req.Service}, Limit: limit, Cursor: req.Cursor}
	if req.PodUID != "" {
		q.PodUIDs = []string{req.PodUID}
	}
	records, err := s.Store.LogPods(ctx, a.ID, from, to, 500)
	if err != nil {
		return err
	}
	var res applogs.ReadResult
	if req.Pod != "" {
		// Resolve the name to pod UIDs so pages stay full and cursors stay correct.
		q.PodUIDs = nil
		for _, p := range records {
			if p.PodName == req.Pod && (req.PodUID == "" || p.PodUID == req.PodUID) {
				q.PodUIDs = append(q.PodUIDs, p.PodUID)
			}
		}
		if len(q.PodUIDs) == 0 {
			out.Warnings = append(out.Warnings, "Hakopod has no stored logs for a pod named "+req.Pod+" in this time range.")
		}
	}
	if req.Pod == "" || len(q.PodUIDs) > 0 {
		if res, err = s.appLogReader().Read(ctx, q); err != nil {
			return err
		}
	}
	pods := map[string]bool{}
	for _, e := range res.Entries {
		out.Scanned++
		// Container is a post-filter, so a page can hold fewer than limit entries.
		if req.Container != "" && e.Instance.Container != req.Container {
			continue
		}
		parsed := logquery.ParseEntry(e.Line.Time.UTC().Format(time.RFC3339Nano)+" "+e.Line.Message, e.Instance.Service, e.Instance.PodName, e.Instance.Container)
		if !match(parsed) {
			continue
		}
		out.Matched++
		pods[e.Instance.PodUID] = true
		out.Entries = append(out.Entries, historyEntry{Entry: parsed, PodUID: e.Instance.PodUID, Revision: e.Instance.Revision, Restart: e.Instance.Restart, Stream: e.Line.Stream, Truncated: e.Line.Truncated})
	}
	out.Pods, out.NextCursor, out.History.ScanLimited = len(pods), res.NextCursor, res.ScanLimited
	out.Truncated = res.NextCursor != "" || res.ScanLimited
	if res.ScanLimited {
		out.Warnings = append(out.Warnings, "Hakopod stopped reading stored logs at its per-request scan limit. Narrow the time range or use the cursor to continue.")
	}
	retention := time.Duration(out.History.RetentionDays) * 24 * time.Hour
	if out.History.CollectedThrough, err = s.collectedThrough(ctx, a.ID, now, retention); err != nil {
		return err
	}
	for _, p := range records {
		if p.GapSeconds <= 0 || (req.Pod != "" || req.PodUID != "") && !slices.Contains(q.PodUIDs, p.PodUID) {
			continue
		}
		if len(out.History.Gaps) == maxHistoryGaps {
			out.Warnings = append(out.Warnings, fmt.Sprintf("More than %d gaps overlap this range; only the newest %d pods are listed.", maxHistoryGaps, maxHistoryGaps))
			break
		}
		out.History.Gaps = append(out.History.Gaps, historyGap{PodUID: p.PodUID, PodName: p.PodName, Seconds: p.GapSeconds, Note: p.GapNote})
	}
	return nil
}
