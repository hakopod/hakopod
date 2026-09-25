package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistrationScopeAndCredentialBoundary(t *testing.T) {
	token := "github_fixture_private_credential"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("credential missing")
		}
		switch r.Method {
		case "POST":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["name"] != "hako-fixture" || body["work_folder"] != "_work" {
				t.Error("invalid registration input")
			}
			fmt.Fprint(w, `{"runner":{"id":42,"name":"hako-fixture","status":"offline"},"encoded_jit_config":"opaque-single-job-config"}`)
		case "GET":
			fmt.Fprint(w, `{"total_count":1,"runners":[{"id":42,"name":"hako-fixture","status":"offline","busy":false}]}`)
		case "DELETE":
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, err := New(token)
	if err != nil {
		t.Fatal(err)
	}
	c.base = server.URL
	registration, err := c.Register(context.Background(), Target{Repository: "team/repo"}, "hako-fixture", []string{"hakopod", "linux"})
	if err != nil || registration.Runner.ID != 42 {
		t.Fatal("registration", err)
	}
	found, err := c.Find(context.Background(), Target{Repository: "team/repo"}, "hako-fixture")
	if err != nil || found == nil || found.ID != 42 {
		t.Fatal("recovery", err)
	}
	if err = c.Delete(context.Background(), Target{Repository: "team/repo"}, 42); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/repos/team/repo/actions/runners") {
			t.Fatal("scope escaped", path)
		}
	}
	for _, repository := range []string{"https://evil.invalid/repo", "team/repo?token=1", "team/../repo", "team/repo/extra"} {
		if _, err = c.Register(context.Background(), Target{Repository: repository}, "hako-fixture", []string{"hakopod"}); err == nil {
			t.Fatal("invalid repository accepted")
		}
	}
	if len(paths) != 3 {
		t.Fatal("invalid input reached network")
	}
}
func TestProviderErrorsDoNotDiscloseBodiesOrFollowRedirects(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.Header().Set("Location", "/stolen")
		w.WriteHeader(302)
		fmt.Fprint(w, "private-provider-value")
	}))
	defer server.Close()
	c, _ := New("github_fixture_private_credential")
	c.base = server.URL
	_, err := c.Register(context.Background(), Target{Repository: "team/repo"}, "hako-fixture", []string{"hakopod"})
	if err == nil || strings.Contains(err.Error(), "private") || called != 1 {
		t.Fatal("unsafe provider error", err, called)
	}
}

func TestOrganizationRegistrationAndDefaultGroup(t *testing.T) {
	for _, group := range []int64{0, 82} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/orgs/team/actions/runner-groups" {
					if group != 0 {
						t.Error("explicit group triggered discovery")
					}
					fmt.Fprint(w, `{"total_count":2,"runner_groups":[{"id":1,"default":false},{"id":82,"default":true}]}`)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/orgs/team/actions/runners") {
					t.Error("wrong organization endpoint", r.URL.Path)
				}
				switch {
				case r.Method == "POST":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["runner_group_id"] != float64(82) {
						t.Error("wrong runner group", body)
					}
					fmt.Fprint(w, `{"runner":{"id":42,"name":"hako-fixture"},"encoded_jit_config":"opaque"}`)
				case r.Method == "DELETE":
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/42"):
					fmt.Fprint(w, `{"id":42,"name":"hako-fixture","status":"online"}`)
				default:
					fmt.Fprint(w, `{"total_count":1,"runners":[{"id":42,"name":"hako-fixture"}]}`)
				}
			}))
			defer server.Close()
			c, _ := New("github_fixture_private_credential")
			c.base = server.URL
			target := Target{Organization: "team", RunnerGroupID: group}
			ctx := context.Background()
			if _, err := c.Register(ctx, target, "hako-fixture", []string{"hakopod"}); err != nil {
				t.Fatal(err)
			}
			if r, err := c.Get(ctx, target, 42); err != nil || r.ID != 42 {
				t.Fatal(r, err)
			}
			if r, err := c.Find(ctx, target, "hako-fixture"); err != nil || r == nil || r.ID != 42 {
				t.Fatal(r, err)
			}
			if err := c.Delete(ctx, target, 42); err != nil {
				t.Fatal(err)
			}
			expected := 4
			if group == 0 {
				expected++
			}
			if len(paths) != expected {
				t.Fatal(paths)
			}
		})
	}
}

func TestInvalidOrganizationTargetsNeverReachNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	c, _ := New("github_fixture_private_credential")
	c.base = server.URL
	for _, target := range []Target{{}, {Organization: "team", Repository: "team/repo"}, {Organization: "https://evil.invalid"}, {Organization: "../team"}, {Organization: "team?query"}, {Organization: "team/repo"}, {Organization: "-team"}, {Organization: "team--name"}, {Organization: "team", RunnerGroupID: -1}, {Organization: "team", RunnerGroupID: 9007199254740992}, {Repository: "team/repo", RunnerGroupID: 1}} {
		ctx := context.Background()
		if _, e := c.Register(ctx, target, "fixture", []string{"hakopod"}); e == nil {
			t.Fatal("accepted invalid target", target)
		}
		if _, e := c.Get(ctx, target, 1); e == nil {
			t.Fatal("accepted invalid get", target)
		}
		if _, e := c.Find(ctx, target, "fixture"); e == nil {
			t.Fatal("accepted invalid find", target)
		}
		if e := c.Delete(ctx, target, 1); e == nil {
			t.Fatal("accepted invalid delete", target)
		}
	}
	if calls != 0 {
		t.Fatal("unsafe target reached network", calls)
	}
}

func TestDefaultGroupDiscoveryFailsClosed(t *testing.T) {
	for _, response := range []string{`{"total_count":0,"runner_groups":[]}`, `{"total_count":1001,"runner_groups":[{"id":1,"default":true}]}`, `{"runner_groups":[{"id":0,"default":true}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("registered without a valid default group")
			}
			fmt.Fprint(w, response)
		}))
		c, _ := New("github_fixture_private_credential")
		c.base = server.URL
		if _, e := c.Register(context.Background(), Target{Organization: "team"}, "fixture", []string{"hakopod"}); e == nil {
			t.Error("missing group accepted")
		}
		server.Close()
	}
}
