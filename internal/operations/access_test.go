package operations

import (
	"context"
	"encoding/json"
	"testing"
)

func TestInstallationConnectionRequiresExactGrants(t *testing.T) {
	for _, identity := range []string{`{"permissions":["admin"]}`, `{"permissions":["admin","agent:admin"],"project":"p","environment":"dev"}`, `{"permissions":["agent:admin"]}`} {
		calls := 0
		r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
			calls++
			return json.Unmarshal([]byte(identity), out)
		}
		if _, err := InvokeWithAccess(context.Background(), r, Scope{}, Access{Installation: true, AllowAdmin: true}, Invocation{Operation: "listUsers"}); err == nil {
			t.Fatal("invalid installation identity accepted")
		}
		if calls != 1 {
			t.Fatal(calls)
		}
	}
	calls := 0
	r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
		calls++
		raw := `{"items":[]}`
		if p == "/me" {
			raw = `{"permissions":["admin","agent:admin"],"credential_type":"machine"}`
		}
		return json.Unmarshal([]byte(raw), out)
	}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{}, Access{Installation: true, AllowAdmin: true}, Invocation{Operation: "listUsers"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestInstallationConnectionCannotRunProjectOrCredentialWrites(t *testing.T) {
	r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
		return json.Unmarshal([]byte(`{"permissions":["admin","agent:admin"],"credential_type":"machine"}`), out)
	}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{}, Access{Installation: true, AllowAdmin: true, AllowWrite: true}, Invocation{Operation: "listApplications"}); err == nil {
		t.Fatal("project operation used installation connection")
	}
	in := Invocation{Operation: "putDNSProvider", Path: map[string]string{"name": "provider"}, Body: json.RawMessage(`{"kind":"cloudflare","zone_filter":[],"expected_revision":0,"credentials":{"token":"secret"}}`)}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{}, Access{Installation: true, AllowAdmin: true, AllowWrite: true}, in); err == nil {
		t.Fatal("credential write bypassed opt-in")
	}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{"p", "dev"}, Access{AllowAdmin: true}, Invocation{Operation: "listUsers"}); err == nil {
		t.Fatal("scoped administration accepted")
	}
}

func TestBackupProjectCredentialFence(t *testing.T) {
	for _, raw := range []string{`{"project":"","environment":"","credential_type":"machine"}`, `{"project":"other","environment":"dev","credential_type":"machine"}`, `{"project":"p","environment":"dev","application":"app","credential_type":"cli"}`} {
		calls := 0
		r := func(_ context.Context, _ string, _ string, _ any, _ string, out any) error {
			calls++
			return json.Unmarshal([]byte(raw), out)
		}
		if _, err := InvokeWithAccess(context.Background(), r, Scope{"p", "dev"}, Access{}, Invocation{Operation: "listBackups"}); err == nil || calls != 1 {
			t.Fatal("backup scope fence bypass", err, calls)
		}
	}
	calls := []string{}
	r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
		calls = append(calls, m+" "+p)
		if p == "/me" {
			return json.Unmarshal([]byte(`{"project":"p","environment":"dev","credential_type":"machine"}`), out)
		}
		return json.Unmarshal([]byte(`{"items":[]}`), out)
	}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{"p", "dev"}, Access{}, Invocation{Operation: "listBackups"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "GET /backups" {
		t.Fatal(calls)
	}
}

func TestInstallationOwnerCannotBeReplacedByAdmin(t *testing.T) {
	calls := 0
	r := func(_ context.Context, _ string, _ string, _ any, _ string, out any) error {
		calls++
		return json.Unmarshal([]byte(`{"permissions":["admin","agent:admin"],"credential_type":"machine","owner":false}`), out)
	}
	if _, err := InvokeWithAccess(context.Background(), r, Scope{}, Access{Installation: true, AllowAdmin: true}, Invocation{Operation: "getInstallationStatus"}); err == nil || calls != 1 {
		t.Fatal("admin acquired owner maintenance", err, calls)
	}
}

func TestCredentialOptInIsIndependent(t *testing.T) {
	calls := 0
	r := func(context.Context, string, string, any, string, any) error { calls++; return nil }
	if _, err := InvokeWithAccess(context.Background(), r, Scope{"p", "dev"}, Access{AllowWrite: true}, Invocation{Operation: "revealDatabaseCredentials", Path: map[string]string{"id": "db"}, Body: json.RawMessage(`{}`)}); err == nil || calls != 0 {
		t.Fatal("credential opt-in bypass", err, calls)
	}
}
