package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestValidCustomHostname(t *testing.T) {
	protected := []string{"apps.example.test", "control.example.test"}
	for host, ok := range map[string]bool{
		"shop.customer.com":              true,
		"a.b.c.customer.co.uk":           true,
		"Shop.customer.com":              false,
		"*.customer.com":                 false,
		"customer":                       false,
		"192.0.2.10":                     false,
		"shop.customer.com.":             false,
		"https://shop.customer.com":      false,
		"apps.example.test":              false,
		"tenant.apps.example.test":       false,
		"control.example.test":           false,
		strings.Repeat("a", 64) + ".com": false,
		strings.Repeat("a.", 127) + "co": false,
	} {
		err := ValidCustomHostname(host, protected)
		if (err == nil) != ok || err != nil && !errors.Is(err, ErrInput) {
			t.Errorf("%q: got %v, want accepted=%v", host, err, ok)
		}
	}
}

func TestHostnameBackoffSchedule(t *testing.T) {
	want := []time.Duration{time.Minute, time.Minute, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for attempts, d := range want {
		if got := HostnameBackoff(attempts); got != d {
			t.Errorf("attempt %d: %s, want %s", attempts, got, d)
		}
	}
}

func TestHostnameTransitions(t *testing.T) {
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	h := CustomHostname{ID: "h", Hostname: "shop.customer.com", Status: HostnamePendingDNS, CreatedAt: created, NextCheckAt: created}
	now := created.Add(time.Minute)
	for i, wait := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour} {
		h = HostnameAfterDNS(h, false, "no CNAME", now)
		if h.Status != HostnamePendingDNS || h.Attempts != i+1 || h.NextCheckAt != now.Add(wait) || h.StatusReason != "no CNAME" {
			t.Fatalf("miss %d: %+v", i+1, h)
		}
		now = h.NextCheckAt
	}
	failed := HostnameAfterDNS(h, false, "no CNAME", created.Add(HostnameDNSWindow))
	if failed.Status != HostnameFailed || !strings.Contains(failed.StatusReason, "7 days") {
		t.Fatalf("a hostname must fail after seven days: %+v", failed)
	}
	verified := HostnameAfterDNS(h, true, "", now)
	if verified.Status != HostnameIssuing || verified.VerifiedAt == nil || !verified.VerifiedAt.Equal(now) || verified.StatusReason != "" {
		t.Fatalf("matched DNS must start issuing: %+v", verified)
	}

	waiting := HostnameAfterIssue(verified, true, false, "Issuing certificate", now.Add(time.Minute))
	if waiting.Status != HostnameIssuing || waiting.NextCheckAt != now.Add(2*time.Minute) || waiting.StatusReason != "Issuing certificate" {
		t.Fatalf("a pending certificate keeps issuing: %+v", waiting)
	}
	active := HostnameAfterIssue(verified, true, true, "", now.Add(time.Minute))
	if active.Status != HostnameActive || active.ActiveAt == nil {
		t.Fatalf("a ready certificate activates: %+v", active)
	}
	plain := HostnameAfterIssue(verified, false, false, "", now)
	if plain.Status != HostnameActive || !strings.Contains(plain.StatusReason, "HTTP") {
		t.Fatalf("without an issuer a routed host is active over HTTP: %+v", plain)
	}
	timedOut := HostnameAfterIssue(verified, true, false, "ACME order failed", now.Add(HostnameIssueWindow))
	if timedOut.Status != HostnameFailed || !strings.Contains(timedOut.StatusReason, "ACME order failed") {
		t.Fatalf("issuance is bounded and keeps the cert-manager reason: %+v", timedOut)
	}
}

func TestCustomHostnameLimit(t *testing.T) {
	svc := spec.Service{CustomHostnames: &spec.CustomHostnames{Enabled: true}}
	if CustomHostnameLimit(svc, 10000) != 1000 || CustomHostnameLimit(svc, 200) != 200 {
		t.Fatal("default per-service limit or installation cap ignored")
	}
	svc.CustomHostnames.Max = 20000
	if CustomHostnameLimit(svc, 10000) != 10000 {
		t.Fatal("installation cap must bound the service max")
	}
}

func hostnameTestSpec(t *testing.T, max int) spec.Application {
	t.Helper()
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.Public = true
	svc.CustomHostnames = &spec.CustomHostnames{Enabled: true, Max: max}
	app.Services["api"] = svc
	app, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestCustomHostnamesStore(t *testing.T) {
	db := isolatedDatabase(t)
	db.ProtectedDomains = []string{"apps.example.test"}
	p := bootstrapPrincipal(t, db)
	p.Permissions = append([]string{"hostnames:manage"}, p.Permissions...)
	ctx := context.Background()
	d, err := db.Accept(ctx, p, "demo", "development", hostnameTestSpec(t, 3), 0, "custom-hostnames-1")
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	writer := p
	writer.Permissions = []string{"deployments:read", "deployments:write"}
	if _, _, err = db.CreateCustomHostname(ctx, writer, a, "api", "shop.customer.com"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deployments:write must not attach hostnames: %v", err)
	}
	h, created, err := db.CreateCustomHostname(ctx, p, a, "api", "shop.customer.com")
	if err != nil || !created || h.Status != HostnamePendingDNS {
		t.Fatal(h, created, err)
	}
	if len(h.Verification.Value) != 64 || h.Verification.Type != "TXT" || h.Verification.Name != "_hakopod-challenge.shop.customer.com" {
		t.Fatal("a hostname needs its own 32-byte TXT verification token", h.Verification)
	}
	again, created, err := db.CreateCustomHostname(ctx, p, a, "api", "shop.customer.com")
	if err != nil || created || again.ID != h.ID || again.Verification != h.Verification {
		t.Fatal("create must be idempotent for the same service", again, created, err)
	}
	// A failed hostname added again starts over with the token the customer already published.
	if _, err = db.Pool.Exec(ctx, "UPDATE custom_hostnames SET status='failed' WHERE id=$1", h.ID); err != nil {
		t.Fatal(err)
	}
	retried, _, err := db.CreateCustomHostname(ctx, p, a, "api", "shop.customer.com")
	if err != nil || retried.Status != HostnamePendingDNS || retried.Verification != h.Verification {
		t.Fatal("retry must restart DNS checks and keep the token", retried, err)
	}
	if _, _, err = db.CreateCustomHostname(ctx, p, a, "api", "x.apps.example.test"); !errors.Is(err, ErrInput) {
		t.Fatalf("installation domains must be refused: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO application_domains(hostname,application_id) VALUES('owned.customer.com',$1)", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.CreateCustomHostname(ctx, p, a, "api", "owned.customer.com"); !errors.Is(err, ErrConflict) {
		t.Fatalf("an application domain must not become a tenant hostname: %v", err)
	}
	if _, _, err = db.CreateCustomHostname(ctx, p, a, "api", "b.customer.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.CreateCustomHostname(ctx, p, a, "api", "c.customer.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.CreateCustomHostname(ctx, p, a, "api", "d.customer.com"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the service max of 3 must hold: %v", err)
	}
	page, next, err := db.CustomHostnames(ctx, a.ID, "api", "", "", 2)
	if err != nil || len(page) != 2 || next != page[1].Hostname {
		t.Fatal(page, next, err)
	}
	rest, next, err := db.CustomHostnames(ctx, a.ID, "api", "", next, 2)
	if err != nil || len(rest) != 1 || next != "" || rest[0].Hostname != "shop.customer.com" {
		t.Fatal(rest, next, err)
	}
	if filtered, _, _ := db.CustomHostnames(ctx, a.ID, "api", HostnameActive, "", 10); len(filtered) != 0 {
		t.Fatal("status filter ignored", filtered)
	}

	// Domains cannot take a tenant hostname either.
	withDomain := hostnameTestSpec(t, 3)
	withDomain.Domains = map[string]string{"shop.customer.com": "api"}
	if _, err = db.Accept(ctx, p, "demo", "development", withDomain, a.Revision, "custom-hostnames-domain"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a domain must not claim a tenant hostname: %v", err)
	}
	// The installation cap bounds the service max at acceptance.
	if _, err = db.PutCustomHostnameSettings(ctx, CustomHostnameSettings{MaxPerService: 2}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Accept(ctx, p, "demo", "development", hostnameTestSpec(t, 5), a.Revision, "custom-hostnames-cap"); !errors.Is(err, ErrInput) {
		t.Fatalf("max above the installation cap must be refused: %v", err)
	}

	claimed, err := db.ClaimCustomHostnames(ctx, 10)
	if err != nil || len(claimed) != 3 {
		t.Fatal(claimed, err)
	}
	if again, _ := db.ClaimCustomHostnames(ctx, 10); len(again) != 0 {
		t.Fatal("claimed hostnames must be leased", again)
	}
	verified := HostnameAfterDNS(claimed[0], true, "", time.Now())
	if err = db.SaveCustomHostnameState(ctx, HostnamePendingDNS, verified); err != nil {
		t.Fatal(err)
	}
	hosts, _, err := db.RoutedCustomHostnames(ctx, a.ID, "api")
	if err != nil || len(hosts) != 1 || hosts[0] != verified.Hostname {
		t.Fatal(hosts, err)
	}
	gone, err := db.DeleteCustomHostname(ctx, p, a, "api", verified.Hostname)
	if err != nil || gone.Status != HostnameDeleting {
		t.Fatal(gone, err)
	}
	if hosts, _, _ = db.RoutedCustomHostnames(ctx, a.ID, "api"); len(hosts) != 0 {
		t.Fatal("a deleting hostname must leave routing", hosts)
	}
	// A stale worker decision cannot resurrect a deleted hostname.
	if err = db.SaveCustomHostnameState(ctx, HostnameIssuing, HostnameAfterIssue(verified, true, true, "", time.Now())); err != nil {
		t.Fatal(err)
	}
	if h, _ := db.CustomHostname(ctx, a.ID, "api", verified.Hostname); h.Status != HostnameDeleting {
		t.Fatal("deleting state was overwritten", h)
	}
	var audits int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action IN ('custom_hostname.create','custom_hostname.delete') AND resource=$1", a.ID).Scan(&audits); err != nil || audits != 4 {
		t.Fatal("create and delete must be audited", audits, err)
	}
	_, asOf, _ := db.RoutedCustomHostnames(ctx, a.ID, "api")
	if err = db.PurgeDeletedCustomHostnames(ctx, a.ID, "api", asOf); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CustomHostname(ctx, a.ID, "api", verified.Hostname); err == nil {
		t.Fatal("purged hostname still present")
	}

	// Turning the feature off marks every remaining hostname for removal.
	off := hostnameTestSpec(t, 2)
	svc := off.Services["api"]
	svc.CustomHostnames = nil
	off.Services["api"] = svc
	if _, err = db.Accept(ctx, p, "demo", "development", off, a.Revision, "custom-hostnames-off"); err != nil {
		t.Fatal(err)
	}
	left, _, _ := db.CustomHostnames(ctx, a.ID, "api", HostnameDeleting, "", 10)
	if len(left) != 2 {
		t.Fatal("disabling custom hostnames must mark them deleting", left)
	}
}
