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
	registration, err := c.Register(context.Background(), "team/repo", "hako-fixture", []string{"hakopod", "linux"})
	if err != nil || registration.Runner.ID != 42 {
		t.Fatal("registration", err)
	}
	found, err := c.Find(context.Background(), "team/repo", "hako-fixture")
	if err != nil || found == nil || found.ID != 42 {
		t.Fatal("recovery", err)
	}
	if err = c.Delete(context.Background(), "team/repo", 42); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/repos/team/repo/actions/runners") {
			t.Fatal("scope escaped", path)
		}
	}
	for _, repository := range []string{"https://evil.invalid/repo", "team/repo?token=1", "team/../repo", "team/repo/extra"} {
		if _, err = c.Register(context.Background(), repository, "hako-fixture", []string{"hakopod"}); err == nil {
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
	_, err := c.Register(context.Background(), "team/repo", "hako-fixture", []string{"hakopod"})
	if err == nil || strings.Contains(err.Error(), "private") || called != 1 {
		t.Fatal("unsafe provider error", err, called)
	}
}
