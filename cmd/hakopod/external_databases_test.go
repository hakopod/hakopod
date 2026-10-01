package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const externalCLIConfig = "schema_version = 1\nname = 'orders'\nprovider = 'planetscale'\nengine = 'mysql'\nhost = 'fixture.psdb.cloud'\nport = 3306\ndatabase = 'app'\n"
const externalCLICredentials = `{"username":"fixture-private-user","password":"fixture-private-password"}`

func externalCLIFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func captureExternalCLI(t *testing.T, run func() error) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "cli-output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	commandErr := run()
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), commandErr
}

func TestExternalDatabaseCLICredentialsAreStrictAndRestricted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix credential file permissions")
	}
	path := externalCLIFile(t, "credentials.json", externalCLICredentials)
	credentials, err := readExternalDatabaseCredentials(path, false, nil)
	if err != nil || credentials.Password != "fixture-private-password" {
		t.Fatal("restricted credentials not read", err)
	}
	if _, err = readExternalDatabaseCredentials(path, true, nil); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readExternalDatabaseCredentials(path, false, nil); err == nil {
		t.Fatal("public-readable credential file accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-credentials.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = readExternalDatabaseCredentials(link, false, nil); err == nil {
		t.Fatal("credential symlink accepted")
	}
	for _, raw := range []string{"", `{"username":"fixture-private-user","password":"fixture-private-password","unexpected":"secret"}`, externalCLICredentials + externalCLICredentials, `{"username":"fixture-private-user","password":123}`, strings.Repeat("x", (16<<10)+1)} {
		bad := externalCLIFile(t, "invalid.json", raw)
		if _, err = readExternalDatabaseCredentials(bad, false, nil); err == nil {
			t.Fatal("invalid credential JSON accepted")
		}
		if strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("credential parser echoed input")
		}
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err = readExternalDatabaseCredentials("", true, input); err != nil {
		t.Fatal("redirected credentials rejected", err)
	}
	if _, err = readExternalDatabaseCredentials("", true, nil); err == nil {
		t.Fatal("missing stdin accepted")
	}
}

func TestExternalDatabaseCLIReadReviewAndDeleteContracts(t *testing.T) {
	id := strings.Repeat("d", 32)
	for _, action := range []string{"list", "show", "operation", "trust", "connections", "refresh-review", "refresh", "disconnect-review", "disconnect", "delete"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			flags := externalDatabaseFlags{Name: "orders", Revision: 4, ApplicationID: strings.Repeat("e", 32), Service: "api", Variable: "DATABASE_URL", ReviewID: strings.Repeat("f", 32), IdempotencyKey: "stable-external-action"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/api/v1/external-databases"
				method := "GET"
				switch action {
				case "list":
					if r.URL.Query().Get("project") != "demo" || r.URL.Query().Get("environment") != "development" {
						t.Error("list scope lost")
					}
				case "operation":
					path = "/api/v1/external-database-operations/" + id
				case "show":
					path += "/" + id
				case "delete":
					path += "/" + id
					method = "DELETE"
				case "refresh-review", "disconnect-review":
					path += "/" + id + "/connection-plan"
					method = "POST"
				case "refresh", "disconnect":
					path += "/" + id + "/connect"
					method = "POST"
				default:
					path += "/" + id + "/" + action
				}
				if r.Method != method || r.URL.Path != path {
					t.Errorf("wrong %s request: %s %s", action, r.Method, r.URL.Path)
				}
				if method != "GET" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if strings.HasSuffix(action, "-review") && (body["disconnect"] != (action == "disconnect-review") || body["variable"] != "DATABASE_URL") {
						t.Error("binding review fields lost")
					}
					if action == "disconnect" || action == "refresh" {
						if body["review_id"] != flags.ReviewID || body["confirm_application"] != "orders" {
							t.Error("binding action fields lost")
						}
					}
					if action == "delete" && (body["expected_revision"] != float64(4) || body["confirm_name"] != "orders") {
						t.Error("delete confirmation lost")
					}
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			c := &client{url: server.URL, http: server.Client()}
			args := []string{action, id}
			if action == "list" {
				args = args[:1]
			}
			if _, err := captureExternalCLI(t, func() error {
				return externalDatabaseCommand(context.Background(), c, "demo", "development", args, flags)
			}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("request missing")
			}
		})
	}
}

func TestExternalDatabaseCLIRotatesOnlyCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix credential file permissions")
	}
	id := strings.Repeat("c", 32)
	credentialsFile := externalCLIFile(t, "credentials.json", externalCLICredentials)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"id":"` + id + `","revision":3,"spec":{"schema_version":1,"name":"orders","provider":"planetscale","engine":"mysql","host":"fixture.psdb.cloud","port":3306,"database":"app"}}`))
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/api/v1/external-databases/"+id {
			t.Error("wrong rotate contract")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
			return
		}
		if !strings.Contains(string(body["credentials"]), "fixture-private-password") || strings.Contains(string(body["spec"]), "password") {
			t.Error("credential rotation crossed the spec boundary")
		}
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	output, err := captureExternalCLI(t, func() error {
		return externalDatabaseCommand(context.Background(), c, "demo", "development", []string{"rotate", id}, externalDatabaseFlags{CredentialsFile: credentialsFile, Revision: 3, Name: "orders", IdempotencyKey: "stable-external-rotate"})
	})
	if err != nil || calls != 2 || strings.Contains(output, "fixture-private") {
		t.Fatal("credential rotation failed or echoed credentials", err)
	}
}

func TestExternalDatabaseCLIRejectsUnsafeActionsAndRedactsErrors(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, item := range []struct {
		args  []string
		flags externalDatabaseFlags
	}{
		{[]string{"delete", id}, externalDatabaseFlags{Name: "orders"}},
		{[]string{"disconnect", id}, externalDatabaseFlags{}},
		{[]string{"create"}, externalDatabaseFlags{}},
		{[]string{"update", id}, externalDatabaseFlags{}},
		{[]string{"connect", id}, externalDatabaseFlags{}},
		{[]string{"connection-plan", id}, externalDatabaseFlags{}},
		{[]string{"show", id}, externalDatabaseFlags{CredentialsStdin: true}},
		{[]string{"show", id}, externalDatabaseFlags{Disconnect: true}},
		{[]string{"show", "../../secrets"}, externalDatabaseFlags{}},
		{[]string{"list", id}, externalDatabaseFlags{}},
	} {
		if err := externalDatabaseCommand(context.Background(), nil, "demo", "development", item.args, item.flags); err == nil {
			t.Fatal("invalid action was not rejected")
		}
	}
	input, err := os.Open(externalCLIFile(t, "credentials.json", externalCLICredentials))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	credentials, err := readExternalDatabaseCredentials("", true, input)
	if err != nil {
		t.Fatal(err)
	}
	redacted := externalDatabaseRequestError(&exitError{4, "conflict fixture-private-user fixture-private-password"}, credentials)
	var exit *exitError
	if !errors.As(redacted, &exit) || exit.code != 4 || strings.Contains(redacted.Error(), "fixture-private") {
		t.Fatal("request error leaked credentials or lost status")
	}
	want := []string{"--service", "api", "disconnect-review", id}
	if got := reorder([]string{"disconnect-review", id, "--service", "api"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("flags consumed positionals: %v", got)
	}
}
