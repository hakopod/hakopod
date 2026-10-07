package api

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
	utilexec "k8s.io/client-go/util/exec"
)

func TestPodExecArgumentsAndLimits(t *testing.T) {
	valid := func() podExecRequest {
		return podExecRequest{Pod: "worker-abc", Container: "app", Command: []string{"/bin/echo", ""}}
	}
	for _, tc := range []struct {
		name   string
		change func(*podExecRequest)
	}{
		{"missing pod", func(in *podExecRequest) { in.Pod = "" }},
		{"missing container", func(in *podExecRequest) { in.Container = "" }},
		{"missing command", func(in *podExecRequest) { in.Command = nil }},
		{"empty executable", func(in *podExecRequest) { in.Command[0] = "" }},
		{"nul", func(in *podExecRequest) { in.Command[1] = "a\x00b" }},
		{"long argument", func(in *podExecRequest) { in.Command[1] = strings.Repeat("x", 4097) }},
		{"too many args", func(in *podExecRequest) { in.Command = make([]string, 33); in.Command[0] = "echo" }},
		{"long timeout", func(in *podExecRequest) { in.TimeoutSeconds = 21 }},
		{"negative timeout", func(in *podExecRequest) { in.TimeoutSeconds = -1 }},
		{"large output", func(in *podExecRequest) { in.MaxOutputBytes = 65537 }},
		{"negative output", func(in *podExecRequest) { in.MaxOutputBytes = -1 }},
		{"bad pod", func(in *podExecRequest) { in.Pod = "../platform" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.change(&in)
			if in.validate() == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
	in := valid()
	if err := in.validate(); err != nil {
		t.Fatal(err)
	}
	if in.TimeoutSeconds != 20 || in.MaxOutputBytes != 65536 || in.Command[1] != "" {
		t.Fatal("defaults or empty argument changed")
	}
}

func TestPodExecMachineGrant(t *testing.T) {
	app := store.Application{Name: "worker", Project: "demo", Environment: "development"}
	valid := store.Principal{Admin: true, CredentialType: "machine", Project: "demo", Environment: "development", Permissions: []string{podExecPermission}}
	if !podExecAllowed(valid, app) {
		t.Fatal("explicit scoped machine grant refused")
	}
	for _, tc := range []struct {
		name   string
		change func(*store.Principal)
	}{
		{"admin wildcard", func(p *store.Principal) { p.Permissions = []string{"admin"} }},
		{"no project", func(p *store.Principal) { p.Project = "" }},
		{"no environment", func(p *store.Principal) { p.Environment = "" }},
		{"foreign project", func(p *store.Principal) { p.Project = "other" }},
		{"foreign application", func(p *store.Principal) { p.Application = "other" }},
		{"mfa required", func(p *store.Principal) { p.MFARequired = true }},
		{"unknown credential", func(p *store.Principal) { p.CredentialType = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			if podExecAllowed(p, app) {
				t.Fatal("ungranted command permitted")
			}
		})
	}
	human := store.Principal{Admin: true, CredentialType: "browser", Permissions: []string{"admin"}}
	if !podExecAllowed(human, app) {
		t.Fatal("human administrator refused")
	}
}

func TestPodExecOutputDrainAndSnapshot(t *testing.T) {
	b := &podExecBuffer{limit: 4}
	if n, err := b.Write([]byte{0, 255, 1}); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte{2, 3, 4}); n != 3 || err != nil {
		t.Fatal("output was not drained", n, err)
	}
	encoded, truncated := b.snapshot()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != string([]byte{0, 255, 1, 2}) || !truncated {
		t.Fatal("binary bytes or truncation lost")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Write([]byte("more output"))
				b.snapshot()
			}
		}()
	}
	wg.Wait()
	if len(b.bytes) != 4 {
		t.Fatal("output exceeded bound")
	}
}
func TestPodExecUnknownOutcome(t *testing.T) {
	outcome, code := podExecOutcome(nil)
	if outcome != "exited" || code == nil || *code != 0 {
		t.Fatal("successful exit lost")
	}
	outcome, code = podExecOutcome(utilexec.CodeExitError{Err: errors.New("fixture exit"), Code: 7})
	if outcome != "exited" || code == nil || *code != 7 {
		t.Fatal("nonzero exit lost")
	}
	outcome, code = podExecOutcome(errors.New("connection lost"))
	if outcome != "unknown" || code != nil {
		t.Fatal("transport failure presented as an exit")
	}
}

// This test uses an isolated PostgreSQL database. It does not execute a pod.
func TestPodExecHandlerAdmission(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Pod command operator", "pod-command@example.test", "disposable-password-123", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := spec.Normalize(spec.Application{Name: "command-admission", Services: map[string]spec.Service{"worker": {Image: "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"}}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := db.Accept(ctx, p, "demo", "development", normalized, 0, "pod-command-initial")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	call := func(principal store.Principal, body, service string) int {
		r := httptest.NewRequest("POST", "/exec", strings.NewReader(body))
		r.SetPathValue("id", deployment.ApplicationID)
		r.SetPathValue("service", service)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		w := httptest.NewRecorder()
		s.executePodCommand(w, r)
		return w.Code
	}
	valid := `{"pod":"worker","container":"app","command":["echo","fixture"]}`
	for _, tc := range []struct {
		name, body, service string
		want                int
	}{
		{"empty request", "", "worker", 400},
		{"unknown field", `{"password":"fixture"}`, "worker", 400},
		{"missing executable", `{"pod":"worker","container":"app","command":[]}`, "worker", 400},
		{"foreign service", valid, "other", 404},
		{"cluster unavailable", valid, "worker", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(p, tc.body, tc.service); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
	machine := p
	machine.CredentialType = "machine"
	machine.Project = "demo"
	machine.Environment = "development"
	machine.Permissions = []string{"admin"}
	if got := call(machine, valid, "worker"); got != 403 {
		t.Fatalf("ungranted machine status %d", got)
	}
	machine.Permissions = []string{podExecPermission}
	if got := call(machine, valid, "worker"); got != 503 {
		t.Fatalf("explicit machine grant status %d", got)
	}
}
