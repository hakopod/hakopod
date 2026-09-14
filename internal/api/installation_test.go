package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/serverlogs"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallationOwnerBoundary(t *testing.T) {
	owner := store.Principal{Owner: true, Admin: true, Email: "owner@example.test", CredentialType: "browser", Permissions: []string{"admin"}}
	cases := []struct {
		name, mode string
		p          store.Principal
		want       int
	}{{"self-hosted owner", "self-hosted", owner, 200}, {"cloud owner", "managed-cloud", owner, 403}}
	admin := owner
	admin.Owner = false
	cases = append(cases, struct {
		name, mode string
		p          store.Principal
		want       int
	}{"admin", "self-hosted", admin, 403})
	cli := owner
	cli.CredentialType = "cli"
	cases = append(cases, struct {
		name, mode string
		p          store.Principal
		want       int
	}{"cli", "self-hosted", cli, 403})
	scoped := owner
	scoped.Project = "demo"
	cases = append(cases, struct {
		name, mode string
		p          store.Principal
		want       int
	}{"scoped", "self-hosted", scoped, 403})
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("browser authority forwarded to helper")
		}
		if r.URL.Path == "/logs" {
			write(w, 200, serverlogs.Snapshot{Entries: []serverlogs.Entry{}, ObservedAt: time.Now().UTC()})
		} else {
			write(w, 200, map[string]string{"source": "fixture"})
		}
	}))
	defer remote.Close()
	client := remote.Client()
	client.Transport = maintenanceTestTransport{remote.URL, http.DefaultTransport}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{Auth: AuthConfig{DeploymentMode: c.mode}, maintenanceHTTP: client, ProcessLogs: serverlogs.New()}
			for _, method := range []func(http.ResponseWriter, *http.Request){s.installationStatus, s.installationLogs} {
				r := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), principalKey{}, c.p))
				r.Header.Set("Authorization", "Bearer private")
				r.Header.Set("Cookie", "private")
				w := httptest.NewRecorder()
				method(w, r)
				if w.Code != c.want {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
	if calls != 2 {
		t.Fatal("denied caller reached helper", calls)
	}
}

type maintenanceTestTransport struct {
	url  string
	base http.RoundTripper
}

func (t maintenanceTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.url, "http://")
	return t.base.RoundTrip(req)
}
func TestInstallationResponseBound(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"` + strings.Repeat("x", 513<<10) + `"}`))
	}))
	defer remote.Close()
	s := &Server{maintenanceHTTP: &http.Client{Transport: maintenanceTestTransport{remote.URL, http.DefaultTransport}}}
	w := httptest.NewRecorder()
	s.maintenance(w, httptest.NewRequest("GET", "/", nil), "/logs", nil)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestProcessLogsFallbackIsOwnerOnlyAndDoesNotEnableMaintenance(t *testing.T) {
	buffer := serverlogs.New()
	buffer.Write([]byte("API ready"))
	owner := store.Principal{Owner: true, Admin: true, Email: "owner@example.test", CredentialType: "browser", Permissions: []string{"admin"}}
	for _, mode := range []string{"self-hosted", "managed-cloud"} {
		for _, isOwner := range []bool{true, false} {
			p := owner
			p.Owner = isOwner
			s := &Server{Auth: AuthConfig{DeploymentMode: mode}, ProcessLogs: buffer,
				maintenanceHTTP: &http.Client{Transport: unavailableMaintenance{}}}
			request := httptest.NewRequest("GET", "/api/v1/installation/logs", nil).WithContext(context.WithValue(context.Background(), principalKey{}, p))
			response := httptest.NewRecorder()
			s.installationLogs(response, request)
			expected := http.StatusForbidden
			if mode == "self-hosted" && isOwner {
				expected = http.StatusOK
			}
			if response.Code != expected {
				t.Fatalf("mode=%s owner=%v: %d %s", mode, isOwner, response.Code, response.Body.String())
			}
			if expected == http.StatusOK {
				var snapshot serverlogs.Snapshot
				if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.Source != "process" || len(snapshot.Entries) != 1 || snapshot.Entries[0].Message != "API ready" {
					t.Fatal(snapshot)
				}
				response = httptest.NewRecorder()
				s.installationStatus(response, request)
				if response.Code != 503 {
					t.Fatal("fallback enabled maintenance status", response.Code)
				}
			}
		}
	}
}

type unavailableMaintenance struct{}

func (unavailableMaintenance) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("maintenance socket unavailable")
}
