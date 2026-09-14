package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/logquery"
	"github.com/hakopod/hakopod/internal/serverlogs"
)

func (s *Server) installationLogSnapshot(ctx context.Context) (serverlogs.Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://maintenance/logs", nil)
	if err != nil {
		return serverlogs.Snapshot{}, err
	}
	response, err := s.maintenanceClient().Do(request)
	if err != nil {
		if s.ProcessLogs != nil {
			return s.ProcessLogs.Snapshot(), nil
		}
		return serverlogs.Snapshot{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || response.StatusCode != 200 || len(data) > 512<<10 {
		return serverlogs.Snapshot{}, fmt.Errorf("journal response unavailable")
	}
	var snapshot serverlogs.Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil || snapshot.Entries == nil || snapshot.ObservedAt.IsZero() || len(snapshot.Entries) > 200 {
		return serverlogs.Snapshot{}, fmt.Errorf("invalid journal response")
	}
	snapshot.Source = "journal"
	snapshot.StartedAt = time.Time{}
	return snapshot, nil
}

type installationLogOptions struct {
	Query        string `json:"query"`
	SinceSeconds int64  `json:"since_seconds"`
	Limit        int    `json:"limit"`
}
type installationLogResult struct {
	cluster.LogQueryResult
	Source     string     `json:"source"`
	ObservedAt time.Time  `json:"observed_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
}

func (s *Server) queryInstallationLogs(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	var options installationLogOptions
	if !decode(w, r, &options) {
		return
	}
	if options.SinceSeconds == 0 {
		options.SinceSeconds = 3600
	}
	if options.Limit == 0 {
		options.Limit = 200
	}
	if options.SinceSeconds < 1 || options.SinceSeconds > 86400 || options.Limit < 1 || options.Limit > 200 {
		problem(w, 400, "invalid_query_options", "Time window must be 1–86400 seconds and limit 1–200.")
		return
	}
	match, err := logquery.Compile(options.Query)
	if err != nil {
		problem(w, 400, "invalid_log_query", err.Error())
		return
	}
	snapshot, err := s.installationLogSnapshot(r.Context())
	if err != nil {
		problem(w, 503, "maintenance_unavailable", "API logs are unavailable. See Infrastructure > Setup for administrator instructions.")
		return
	}
	write(w, 200, queryInstallationSnapshot(snapshot, options, match, time.Now().UTC()))
}

var processLevel = regexp.MustCompile(`(?:^|\s)level=(DEBUG|INFO|WARN|ERROR)(?:\s|$)`)

func queryInstallationSnapshot(snapshot serverlogs.Snapshot, options installationLogOptions, match logquery.Predicate, now time.Time) installationLogResult {
	result := installationLogResult{Source: snapshot.Source, ObservedAt: snapshot.ObservedAt,
		LogQueryResult: cluster.LogQueryResult{Entries: []logquery.Entry{}, Histogram: []cluster.LogBucket{}, Warnings: []string{}, WindowSeconds: options.SinceSeconds}}
	if snapshot.Source == "process" {
		result.StartedAt = &snapshot.StartedAt
		result.Warnings = append(result.Warnings, "Search covers at most 200 captured API entries from this process. Journal history is unavailable; the buffer clears on API restart.")
	} else {
		result.Warnings = append(result.Warnings, "Search covers at most the 200 most recent API journal entries, not the complete journal.")
	}
	step := time.Duration((options.SinceSeconds+59)/60) * time.Second
	start := now.Add(-time.Duration(options.SinceSeconds) * time.Second)
	for stamp := start; stamp.Before(now); stamp = stamp.Add(step) {
		result.Histogram = append(result.Histogram, cluster.LogBucket{Timestamp: stamp})
	}
	invalid := 0
	for _, raw := range snapshot.Entries {
		result.Scanned++
		micros, err := strconv.ParseInt(raw.Timestamp, 10, 64)
		if err != nil || micros <= 0 {
			invalid++
			continue
		}
		stamp := time.UnixMicro(micros).UTC()
		if stamp.Before(start) || !stamp.Before(now) {
			continue
		}
		entry := logquery.ParseEntry(stamp.Format(time.RFC3339Nano)+" "+raw.Message, "hakopod-api", snapshot.Source, "")
		// Older systemd records used slog text format; read its explicit level field.
		if entry.Severity == "DEFAULT" && strings.HasPrefix(raw.Message, "time=") {
			if level := processLevel.FindStringSubmatch(raw.Message); len(level) == 2 {
				entry.Severity = level[1]
				if entry.Severity == "WARN" {
					entry.Severity = "WARNING"
				}
			}
		}
		if !match(entry) {
			continue
		}
		result.Matched++
		bucket := int(stamp.Sub(start) / step)
		if bucket >= 0 && bucket < len(result.Histogram) {
			result.Histogram[bucket].Count++
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.SliceStable(result.Entries, func(i, j int) bool { return result.Entries[i].Timestamp.After(result.Entries[j].Timestamp) })
	if len(result.Entries) > options.Limit {
		result.Entries = result.Entries[:options.Limit]
		result.Truncated = true
	}
	if invalid > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d entries had no valid journal timestamp and were excluded from the time window.", invalid))
	}
	return result
}
