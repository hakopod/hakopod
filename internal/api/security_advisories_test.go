package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestSecurityInventoryMapping(t *testing.T) {
	configured := spec.Application{Services: map[string]spec.Service{
		"api": {Image: "node:22"}, "web": {Image: "registry.example/web@" + testDigest}, "bare": {Image: "busybox"}}}
	resolved := spec.Application{Services: map[string]spec.Service{
		"api": {Image: "docker.io/library/node@" + testDigest}, "web": {Image: "registry.example/web@" + testDigest}, "bare": {Image: "busybox@" + testDigest}}}
	builds := map[string]buildConfig{"web": {Mode: "framework", Framework: &framework.Plan{Runtime: "static"}}}
	env := func(image string) (map[string]string, error) {
		switch {
		case strings.HasPrefix(image, "docker.io/library/node"):
			return map[string]string{"NODE_VERSION": "v22.3.0", "PYTHON_VERSION": "3.12.4-slim", "SECRET": "x"}, nil
		case strings.HasPrefix(image, "busybox"):
			return nil, errors.New("registry unavailable")
		}
		return map[string]string{}, nil
	}
	inv := buildInventory(store.SecurityCandidate{ApplicationID: "a", DeploymentID: "d", Revision: 2}, configured, resolved, nil, builds, env)
	by := map[string]advisory.ServiceEvidence{}
	for _, s := range inv.Services {
		by[s.Service] = s
	}
	api := by["api"]
	if api.Kind != advisory.KindImage || !api.Mutable || api.Digest != testDigest || api.Unknown || api.Runtimes["node"] != "22.3.0" || api.Runtimes["python"] != "3.12.4" || len(api.Runtimes) != 2 {
		t.Fatalf("api evidence: %+v", api)
	}
	web := by["web"]
	if web.Kind != advisory.KindSource || web.Mutable || web.BaseImage != framework.StaticImage || !web.BasePinned || !web.Unknown {
		t.Fatalf("web evidence: %+v", web)
	}
	if web.Reason != "The image does not record a runtime version." {
		t.Fatalf("web reason: %q", web.Reason)
	}
	if by["bare"].Reason != "Could not read the image configuration from the registry. Cause: registry access unavailable." {
		t.Fatalf("bare reason: %q", by["bare"].Reason)
	}
	if len(inv.Incomplete) != 1 || inv.Incomplete[0] != "bare: "+by["bare"].Reason {
		t.Fatalf("incomplete must list only the registry failure: %v", inv.Incomplete)
	}
	if api.Reason != "" {
		t.Fatalf("known runtime must have no reason: %q", api.Reason)
	}
	if !by["bare"].Unknown || by["bare"].Runtimes != nil {
		t.Fatalf("registry failure must be unknown: %+v", by["bare"])
	}
}

func TestSecurityRegistryCauseHidesErrorText(t *testing.T) {
	err := errors.New("GET https://robot:hunter2@registry.example/v2/app/manifests/latest: 401 Unauthorized")
	if got := registryCause(err); got != "access denied" || strings.Contains(got, "hunter2") {
		t.Fatalf("cause: %q", got)
	}
	if got := registryCause(errors.New("dial https://u:p@x: something odd")); got != "registry error" {
		t.Fatalf("cause: %q", got)
	}
}

func TestSecurityFeedErrorClassHidesURL(t *testing.T) {
	leak := errors.New(`Get "https://raw.example/feed.json?token=ghp_secret": dial tcp: lookup raw.example: no such host`)
	for _, got := range []string{feedErrorClass(leak, nil, true), feedErrorClass(errors.New("advisory feed: unexpected status 503 Service Unavailable"), nil, true)} {
		if strings.Contains(got, "ghp_secret") || strings.Contains(got, "http") {
			t.Fatalf("leaked: %q", got)
		}
	}
	cases := map[string]string{
		feedErrorClass(leak, nil, true): "Could not reach the advisory feed.",
		feedErrorClass(errors.New("advisory feed: unexpected status 404 Not Found"), nil, false): "The advisory feed has not been published yet.",
		feedErrorClass(errors.New("advisory feed: unexpected status 404 Not Found"), nil, true):  "The advisory feed returned HTTP 404.",
		feedErrorClass(nil, errors.New("advisory feed signature does not verify"), true):         "The advisory feed signature does not verify.",
		feedErrorClass(nil, errors.New("decode advisory feed: https://x?token=y"), true):         "The advisory feed is invalid.",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestSecurityCombineDropsOSVDuplicate(t *testing.T) {
	feed := []advisory.Finding{{AdvisoryID: "HKP-1", Aliases: []string{"CVE-2026-1"}, Service: "api", Source: advisory.SourceHakopod}}
	osv := []advisory.Finding{
		{AdvisoryID: "CVE-2026-1", Service: "api", Source: advisory.SourceOSV},
		{AdvisoryID: "CVE-2026-1", Service: "worker", Source: advisory.SourceOSV},
	}
	got := combineFindings(feed, osv)
	dropped := slices.ContainsFunc(got, func(f advisory.Finding) bool { return f.Source == advisory.SourceOSV && f.Service == "api" })
	if len(got) != 2 || dropped || !slices.ContainsFunc(got, func(f advisory.Finding) bool { return f.Service == "worker" }) {
		t.Fatalf("merged: %+v", got)
	}
	if out := combineFindings(nil, nil); out == nil || len(out) != 0 {
		t.Fatalf("empty must be non-nil: %v", out)
	}
}

func TestSecurityNotificationGating(t *testing.T) {
	cases := []struct {
		f    advisory.Finding
		want bool
	}{
		{advisory.Finding{Source: advisory.SourceHakopod, Severity: advisory.Low, Confidence: advisory.Confirmed}, true},
		{advisory.Finding{Source: advisory.SourceHakopod, Severity: advisory.Critical, Confidence: advisory.Possible}, false},
		{advisory.Finding{Source: advisory.SourceOSV, Severity: advisory.High, Confidence: advisory.Confirmed}, true},
		{advisory.Finding{Source: advisory.SourceOSV, Severity: advisory.Moderate, Confidence: advisory.Confirmed}, false},
	}
	for i, c := range cases {
		if notifiable(c.f) != c.want {
			t.Fatalf("case %d: want %v", i, c.want)
		}
	}
}

func TestSecurityAutoPatchEligibility(t *testing.T) {
	row := store.ApplicationSecurity{AutoPatch: true, ApprovedBy: "u1", Project: "demo", Environment: "development", ApplicationName: "app"}
	f := advisory.Finding{AutoEligible: true, FixedDigests: []string{testDigest}}
	approver := store.Principal{ID: "u1", Project: "demo", Environment: "development", Permissions: []string{"deployments:write"}, IdentityPermissions: []string{"deployments:write"}}
	fixed := "docker.io/library/node@" + testDigest
	if !patchEligible(row, f, approver, nil, fixed, false) {
		t.Fatal("expected eligible")
	}
	off := row
	off.AutoPatch = false
	reader := approver
	reader.Permissions = []string{"deployments:read"}
	reader.IdentityPermissions = reader.Permissions
	notAuto := f
	notAuto.AutoEligible = false
	for name, ok := range map[string]bool{
		"policy off":       patchEligible(off, f, approver, nil, fixed, false),
		"not eligible":     patchEligible(row, notAuto, approver, nil, fixed, false),
		"approver gone":    patchEligible(row, f, store.Principal{}, store.ErrUnauthorized, fixed, false),
		"other approver":   patchEligible(row, f, store.Principal{ID: "u2", Permissions: approver.Permissions}, nil, fixed, false),
		"no write":         patchEligible(row, f, reader, nil, fixed, false),
		"feed stale":       patchEligible(row, f, approver, nil, fixed, true),
		"digest not fixed": patchEligible(row, f, approver, nil, "docker.io/library/node@sha256:22", false),
	} {
		if ok {
			t.Fatalf("%s: must not be eligible", name)
		}
	}
	if patchAttemptID("a", "adv", 1, "api", "d") == patchAttemptID("a", "adv", 2, "api", "d") {
		t.Fatal("attempt id must include advisory revision")
	}
}

func TestSecurityAdvisoriesHandlers(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "security-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := spec.Parse([]byte("name='security-app'\n[services.api]\nimage='node:22'"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", parsed, 0, "security-initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=jsonb_set(spec,'{services,api,image}',to_jsonb($2::text)) WHERE id=$1", d.ID, "docker.io/library/node@"+testDigest); err != nil {
		t.Fatal(err)
	}
	// Offline: no OSV and no feed in this test.
	if _, err = db.PutSecuritySettings(ctx, store.SecuritySettings{}, 0); err != nil {
		t.Fatal(err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Auth: AuthConfig{DeploymentMode: cluster.DeploymentSelfHosted, EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), PublicURL: "https://dashboard.example"}}
	handler := s.Handler()
	call := func(method, path, key, idem string, body any, status int) []byte {
		t.Helper()
		var payload string
		if body != nil {
			payload = string(store.JSON(body))
		}
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		if idem != "" {
			r.Header.Set("Idempotency-Key", idem)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	_, reader, err := db.CreateKey(ctx, p, store.KeyInput{Name: "security-reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	app := "/applications/" + a.ID + "/security"
	// Check evaluates the running deployment; without a cluster the service is unknown, never safe.
	var checked securityApplication
	if err = json.Unmarshal(call("POST", app+"/check", token, "", nil, 200), &checked); err != nil {
		t.Fatal(err)
	}
	if checked.DeploymentID != d.ID || len(checked.UnknownServices) != 1 || !checked.CanWrite {
		t.Fatalf("check: %+v", checked)
	}
	call("POST", app+"/check", token, "", nil, 429)
	call("POST", app+"/check", reader, "", nil, 403)
	call("PUT", app+"/policy", reader, "", map[string]any{"auto_patch": true, "expected_policy_revision": 1}, 403)
	call("POST", app+"/apply", reader, "idem-key-1", map[string]any{"advisory_id": "HKP-1", "service": "api", "expected_revision": a.Revision}, 403)
	call("PUT", "/security/settings", reader, "", map[string]any{"feed_enabled": true, "osv_enabled": true, "expected_revision": 1}, 403)

	call("POST", app+"/apply", token, "", map[string]any{"advisory_id": "HKP-1", "service": "api", "expected_revision": a.Revision}, 400)
	call("POST", app+"/apply", token, "idem-key-1", map[string]any{"advisory_id": "HKP-1", "service": "api"}, 400)
	call("POST", app+"/apply", token, "idem-key-1", map[string]any{"advisory_id": "HKP-1", "service": "api", "expected_revision": a.Revision + 5}, 409)
	call("POST", app+"/apply", token, "idem-key-1", map[string]any{"advisory_id": "HKP-1", "service": "api", "expected_revision": a.Revision}, 422)

	call("PUT", app+"/policy", token, "", map[string]any{"auto_patch": true}, 400)
	var policy securityApplication
	if err = json.Unmarshal(call("PUT", app+"/policy", token, "", map[string]any{"auto_patch": true, "expected_policy_revision": 1}, 200), &policy); err != nil {
		t.Fatal(err)
	}
	if !policy.AutoPatch || policy.ApprovedBy != p.ID || policy.PolicyRevision != 2 {
		t.Fatalf("policy: %+v", policy)
	}
	call("PUT", app+"/policy", token, "", map[string]any{"auto_patch": false, "expected_policy_revision": 1}, 409)
	if policy.ApprovalExpiresAt == nil || policy.ApprovalExpiresAt.Before(time.Now().Add(80*24*time.Hour)) {
		t.Fatalf("approval expiry: %v", policy.ApprovalExpiresAt)
	}
	// A later failed rollout does not freeze checks: evidence comes from the latest succeeded deployment.
	failed, err := db.Accept(ctx, p, "demo", "development", parsed, a.Revision, "security-failed-next")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='failed' WHERE id=$1", failed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE application_security SET checked_at=now()-interval '1 minute' WHERE application_id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	var refreshed securityApplication
	if err = json.Unmarshal(call("POST", app+"/check", token, "", nil, 200), &refreshed); err != nil || refreshed.DeploymentID != d.ID {
		t.Fatalf("check after failed rollout: %+v %v", refreshed, err)
	}
	a.Revision = failed.Revision

	// Seed findings as a scan would have saved them, then read the overview.
	row, err := db.ApplicationSecurity(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.Findings = []advisory.Finding{{AdvisoryID: "HKP-2026-0001", AdvisoryRevision: 1, Source: advisory.SourceHakopod, Severity: advisory.Critical, Title: "Node.js image update",
		URL: "https://github.com/hakopod/hakopod/security/advisories", Service: "api", Confidence: advisory.Confirmed, Evidence: "docker.io/library/node@" + testDigest,
		Fix: advisory.FixReresolve, AutoEligible: true, FixedDigests: []string{"sha256:2222222222222222222222222222222222222222222222222222222222222222"}}}
	if err = db.SaveApplicationSecurity(ctx, row); err != nil {
		t.Fatal(err)
	}
	c := store.SecurityCandidate{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Name: a.Name, Revision: a.Revision, DeploymentID: d.ID}
	many := make([]advisory.Finding, 7)
	for i := range many {
		many[i] = row.Findings[0]
		many[i].AdvisoryID = "HKP-CAP-" + string(rune('a'+i))
	}
	if calls := s.notifySecurity(ctx, c, many); calls != securityNotifyCap {
		t.Fatalf("notification cap: %d", calls)
	}
	// Already-notified findings do not use up the cap: the second pass sends 6 and 7.
	if calls := s.notifySecurity(ctx, c, many); calls != 2 {
		t.Fatalf("second pass: %d", calls)
	}
	var sent []string
	rows, err := db.Pool.Query(ctx, "SELECT advisory_id FROM security_notifications WHERE application_id=$1 AND advisory_id LIKE 'HKP-CAP-%' ORDER BY advisory_id", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sent, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || len(sent) != 7 {
		t.Fatalf("sent %v %v", sent, err)
	}
	overview := call("GET", "/security/advisories", reader, "", nil, 200)
	var parsedOverview struct {
		Applications []securityApplication `json:"applications"`
		Summary      securitySummary       `json:"summary"`
		Feed         securityFeedStatus    `json:"feed"`
	}
	if err = json.Unmarshal(overview, &parsedOverview); err != nil {
		t.Fatal(err)
	}
	if len(parsedOverview.Applications) != 1 || parsedOverview.Summary != (securitySummary{AffectedApplications: 1, Critical: 1}) || !parsedOverview.Feed.Stale || parsedOverview.Applications[0].CanWrite {
		t.Fatalf("overview: %s", overview)
	}
	// A hakopod finding cannot be applied while the feed is stale; the re-evaluation also clears it here.
	// The current revision failed: no fix is built on the older spec.
	call("POST", app+"/apply", token, "idem-key-2", map[string]any{"advisory_id": "HKP-2026-0001", "service": "api", "expected_revision": a.Revision}, 409)
	// Replaying an Idempotency-Key returns the deployment it already started.
	var replay map[string]string
	if err = json.Unmarshal(call("POST", app+"/apply", token, "security-failed-next", map[string]any{"advisory_id": "HKP-2026-0001", "service": "api", "expected_revision": 1}, 202), &replay); err != nil || replay["deployment_id"] != failed.ID {
		t.Fatalf("replay: %v %v", replay, err)
	}
	t.Logf("GET /security/advisories: %s", overview)
	if parsedOverview.Applications[0].Incomplete[0] != "api: Could not read the image configuration from the registry. Cause: registry access unavailable." {
		t.Fatalf("incomplete: %v", parsedOverview.Applications[0].Incomplete)
	}
	var settings map[string]any
	_ = json.Unmarshal(call("GET", "/security/settings", reader, "", nil, 200), &settings)
	if settings["can_manage"] != false {
		t.Fatalf("settings: %v", settings)
	}
	// Automatic patches run under the grant minted at approval.
	policyRow, err := db.ApplicationSecurity(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver, err := s.approverPrincipal(ctx, policyRow)
	if err != nil || approver.KeyID != policyRow.GrantID || !s.approverCanPatch(ctx, policyRow) {
		t.Fatalf("approver: %+v %v", approver, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", policyRow.GrantID); err != nil {
		t.Fatal(err)
	}
	in, err := s.loadSecurityInputs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := s.evaluateSecurity(ctx, c, in)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.AutoPatch || !slices.Contains(paused.Inventory.Incomplete, approverPausedNote) || s.autoPatch(ctx, c, paused, in) {
		t.Fatalf("paused: auto_patch=%v incomplete=%v", paused.AutoPatch, paused.Inventory.Incomplete)
	}

	// A recent check against an older deployment is not reused.
	if _, err = db.Pool.Exec(ctx, "UPDATE application_security SET deployment_id='older-deployment',checked_at=now() WHERE application_id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	reused, _, err := s.recentOrEvaluate(ctx, a, d.ID)
	if err != nil || reused.DeploymentID != d.ID {
		t.Fatalf("stale cached row reused: %s %v", reused.DeploymentID, err)
	}
	// OSV truncated or unreachable: note it and retry in about an hour.
	if _, err = db.PutSecuritySettings(ctx, store.SecuritySettings{OSVEnabled: true}, 1); err != nil {
		t.Fatal(err)
	}
	securityRuntimeEnvForTest = func(string) (map[string]string, error) { return map[string]string{"GOLANG_VERSION": "1.22.0"}, nil }
	t.Cleanup(func() { securityRuntimeEnvForTest = nil })
	vuln, err := os.ReadFile("../advisory/testdata/osv_vuln_go.json")
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	osvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/querybatch" {
			w.Write(vuln)
			return
		}
		pages++
		fmt.Fprintf(w, `{"results":[{"vulns":[{"id":"GO-2024-2598"}],"next_page_token":"p%d"}]}`, pages)
	}))
	defer osvSrv.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	in, err = s.loadSecurityInputs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for url, note := range map[string]string{osvSrv.URL: osvTruncatedNote, closed.URL: osvUnreachableNote} {
		t.Setenv("HAKOPOD_OSV_URL", url)
		row, err := s.evaluateSecurity(ctx, c, in)
		if err != nil {
			t.Fatal(err)
		}
		age := time.Since(*row.OSVCheckedAt)
		if !slices.Contains(row.Inventory.Incomplete, note) || age < 23*time.Hour-time.Minute || age > 23*time.Hour+time.Minute {
			t.Fatalf("%s: incomplete=%v osv age=%v", note, row.Inventory.Incomplete, age)
		}
		var cached int
		if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM osv_cache").Scan(&cached); err != nil || cached != 0 {
			t.Fatalf("cached %d %v", cached, err)
		}
	}
	if pages == 0 {
		t.Fatal("OSV server not used")
	}
}
