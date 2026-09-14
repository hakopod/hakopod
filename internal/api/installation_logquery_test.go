package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/logquery"
	"github.com/hakopod/hakopod/internal/serverlogs"
	"github.com/hakopod/hakopod/internal/store"
)

func TestInstallationQueryFilterWindowLimitAndHistogram(t *testing.T) {
	now := time.Date(2026, 9, 14, 5, 0, 0, 0, time.UTC)
	entry := func(age time.Duration, message string) serverlogs.Entry {
		return serverlogs.Entry{Timestamp: strconv.FormatInt(now.Add(-age).UnixMicro(), 10), Message: message}
	}
	snapshot := serverlogs.Snapshot{Source: "process", StartedAt: now.Add(-time.Hour), ObservedAt: now, Entries: []serverlogs.Entry{
		entry(time.Minute, `{"level":"ERROR","msg":"request timeout","status":503}`),
		entry(2*time.Minute, `{"level":"INFO","msg":"ready","status":200}`),
		entry(3*time.Minute, `time=2026-09-14T04:57:00Z level=ERROR msg="legacy timeout"`),
		entry(2*time.Hour, `{"level":"ERROR","msg":"old timeout"}`),
		{Timestamp: "invalid", Message: "unknown time"},
	}}
	match, err := logquery.Compile("severity >= ERROR AND message ILIKE '%timeout%'")
	if err != nil {
		t.Fatal(err)
	}
	result := queryInstallationSnapshot(snapshot, installationLogOptions{SinceSeconds: 3600, Limit: 1}, match, now)
	if result.Scanned != 5 || result.Matched != 2 || len(result.Entries) != 1 || !result.Truncated || result.Source != "process" || result.StartedAt == nil {
		t.Fatal(result)
	}
	if result.Entries[0].Severity != "ERROR" || result.Entries[0].Fields["status"] != json.Number("503") {
		t.Fatal(result.Entries[0])
	}
	count := 0
	for _, bucket := range result.Histogram {
		count += bucket.Count
	}
	if count != 2 || len(result.Histogram) > 60 {
		t.Fatal("histogram must describe all matches before the result limit", result.Histogram)
	}
	if len(result.Warnings) != 2 {
		t.Fatal("missing retention and timestamp warnings", result.Warnings)
	}
	match, _ = logquery.Compile("json.status >= 500")
	result = queryInstallationSnapshot(snapshot, installationLogOptions{SinceSeconds: 3600, Limit: 200}, match, now)
	if result.Matched != 1 {
		t.Fatal("JSON comparison failed", result)
	}
}

func TestInstallationQueryValidationAndAuthorizationBeforeReading(t *testing.T) {
	owner := store.Principal{Owner: true, Admin: true, Email: "owner@example.test", CredentialType: "browser", Permissions: []string{"admin"}}
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		write(w, 200, serverlogs.Snapshot{Entries: []serverlogs.Entry{}, ObservedAt: time.Now().UTC()})
	}))
	defer remote.Close()
	for _, tc := range []struct {
		mode, body string
		owner      bool
		want       int
	}{
		{"managed-cloud", `{}`, true, 403}, {"self-hosted", `{}`, false, 403},
		{"self-hosted", `{"query":"message ="}`, true, 400},
		{"self-hosted", `{"query":"` + strings.Repeat("x", 4097) + `"}`, true, 400},
		{"self-hosted", `{"limit":201}`, true, 400}, {"self-hosted", `{"since_seconds":86401}`, true, 400},
		{"self-hosted", `{"since_seconds":-1}`, true, 400}, {"self-hosted", `{"previous":true}`, true, 400},
		{"self-hosted", `{}`, true, 200},
	} {
		s := &Server{Auth: AuthConfig{DeploymentMode: tc.mode}, maintenanceHTTP: &http.Client{Transport: maintenanceTestTransport{remote.URL, http.DefaultTransport}}}
		principal := owner
		principal.Owner = tc.owner
		request := httptest.NewRequest("POST", "/api/v1/installation/logs/query", strings.NewReader(tc.body)).WithContext(context.WithValue(context.Background(), principalKey{}, principal))
		response := httptest.NewRecorder()
		s.queryInstallationLogs(response, request)
		if response.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.mode, tc.body, response.Code, response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("invalid/unauthorized query read logs", calls)
	}
}

func TestInstallationQueryUsesCurrentProcessFallback(t *testing.T) {
	buffer := serverlogs.New()
	buffer.Write([]byte(`{"level":"WARN","msg":"worker unavailable"}`))
	s := &Server{Auth: AuthConfig{DeploymentMode: "self-hosted"}, ProcessLogs: buffer, maintenanceHTTP: &http.Client{Transport: unavailableMaintenance{}}}
	p := store.Principal{Owner: true, Admin: true, Email: "owner@example.test", CredentialType: "browser", Permissions: []string{"admin"}}
	request := httptest.NewRequest("POST", "/api/v1/installation/logs/query", strings.NewReader(`{"query":"severity >= WARNING"}`)).WithContext(context.WithValue(context.Background(), principalKey{}, p))
	response := httptest.NewRecorder()
	s.queryInstallationLogs(response, request)
	var result installationLogResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || result.Source != "process" || result.Matched != 1 || result.StartedAt == nil {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestInstallationLogsRejectMalformedHelperSnapshot(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) }))
	defer remote.Close()
	s := &Server{ProcessLogs: serverlogs.New(), maintenanceHTTP: &http.Client{Transport: maintenanceTestTransport{remote.URL, http.DefaultTransport}}}
	if _, err := s.installationLogSnapshot(context.Background()); err == nil {
		t.Fatal("malformed helper response represented as an empty journal")
	}
}
