package api

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/remotecommand"
)

func TestProfilesTeamUsernamesAndHostAuthority(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Owner", "profiles@example.test", "disposable-password-123", "")
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
	if !p.CanHostTerminal("server") || !p.IsSuperAdmin() {
		t.Fatal("installer owner lacks explicit super-admin authority")
	}
	profile, err := db.UpdateProfile(ctx, p, store.ProfileInput{Name: "Display name", AvatarStyle: "glass", AvatarSeed: "blue", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Revision != 2 || !strings.HasPrefix(profile.AvatarURL, "https://api.dicebear.com/10.x/glass/") {
		t.Fatal("profile/avatar not persisted")
	}
	if _, err = db.UpdateProfile(ctx, p, store.ProfileInput{Name: "Stale", AvatarStyle: "initials", ExpectedRevision: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale profile overwrote saved preference", err)
	}
	if strings.Contains(store.AvatarURL("identicon", "", p.ID), "example.test") {
		t.Fatal("avatar URL reveals an email")
	}
	user := store.NewID()
	if _, err = db.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin) VALUES($1,'Member','member-profiles@example.test',true)", user); err != nil {
		t.Fatal(err)
	}
	memberSession, err := db.NewSession(ctx, user, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	member, err := db.Authenticate(ctx, memberSession.Token)
	if err != nil {
		t.Fatal(err)
	}
	if member.CanHostTerminal("server") || member.IsSuperAdmin() {
		t.Fatal("ordinary admin bypassed host permissions")
	}
	team, err := db.CreateTeam(ctx, p, "first-team")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetTeamMember(ctx, p, team.ID, user, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetTeamUsername(ctx, member, team.ID, user, "casey"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetTeamUsername(ctx, p, team.ID, p.ID, "casey"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("team accepted duplicate username", err)
	}
	second, err := db.CreateTeam(ctx, p, "second-team")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetTeamUsername(ctx, p, second.ID, p.ID, "casey"); err != nil {
		t.Fatal("username should be scoped to a team", err)
	}
	grant := store.HostGrant{IdentityID: user, Node: "server", Permission: "nodes:terminal", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err = db.SetHostGrant(ctx, member, grant); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("ordinary admin granted host access", err)
	}
	if _, err = db.SetHostGrant(ctx, p, grant); err != nil {
		t.Fatal(err)
	}
	member, err = db.Authenticate(ctx, memberSession.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !member.CanHostTerminal("server") || member.CanHostTerminal("other") {
		t.Fatal("node grant scope was not enforced")
	}
	for _, kind := range []string{"machine", "cli", "integration"} {
		probe := member
		probe.CredentialType = kind
		if probe.CanHostTerminal("server") {
			t.Fatal("non-browser credential inherited host grant", kind)
		}
	}
	probe := p
	probe.Project = "demo"
	if probe.CanHostTerminal("server") {
		t.Fatal("scoped owner credential granted host access")
	}
	s := &Server{Store: db}
	handler := s.Handler()
	defer s.CloseTerminals()
	xctx, cancel := context.WithCancel(ctx)
	defer cancel()
	x := &terminalSession{id: store.NewID(), hostNode: "server", owner: member.ID, key: member.KeyID, ctx: xctx, cancel: cancel, started: true, input: make(chan []byte, 1), sizes: make(chan remotecommand.TerminalSize, 1)}
	s.terminals[x.id] = x
	base := "/api/v1/nodes/server/terminal/" + x.id + "/input"
	request := func(token string) int {
		r := httptest.NewRequest("POST", base, bytes.NewBufferString(`{"data":"eA=="}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(memberSession.Token); code != 204 {
		t.Fatal("explicit grant denied", code)
	}
	if code := request(session.Token); code != 404 {
		t.Fatal("owner attached to another person's terminal", code)
	}
	if err = db.RevokeHostGrant(ctx, p, user, "server"); err != nil {
		t.Fatal(err)
	}
	if code := request(memberSession.Token); code != 403 {
		t.Fatal("revoked grant retained terminal input", code)
	}
	go s.guardHostTerminal(x)
	select {
	case <-xctx.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("host stream survived grant revocation")
	}
}
