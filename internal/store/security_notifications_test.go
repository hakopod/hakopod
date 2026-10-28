package store

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/advisory"
)

func TestSecurityNotificationQueue(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "security-notifications")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"INSERT INTO projects(name) VALUES('demo') ON CONFLICT DO NOTHING", "INSERT INTO environments(project,name) VALUES('demo','production') ON CONFLICT DO NOTHING"} {
		if _, err = db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	app := Application{ID: NewID(), Name: "web", Project: "demo", Environment: "production"}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,spec) VALUES($1,$2,$3,$4,'{}')", app.ID, app.Project, app.Environment, app.Name); err != nil {
		t.Fatal(err)
	}
	dest := []byte(strings.Repeat("x", 32))
	with, err := db.PutNotificationTarget(ctx, p, app, NotificationTarget{ID: NewID(), ApplicationID: app.ID, Name: "with", Kind: "webhook", Enabled: true, Events: []string{"failed", "succeeded", "cancelled", SecurityNotificationEvent}, Destination: dest}, 0)
	if err != nil {
		t.Fatal("security_advisory must be a valid event:", err)
	}
	if _, err = db.PutNotificationTarget(ctx, p, app, NotificationTarget{ID: NewID(), ApplicationID: app.ID, Name: "without", Kind: "webhook", Enabled: true, Events: []string{"failed"}, Destination: dest}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PutNotificationTarget(ctx, p, app, NotificationTarget{ID: NewID(), ApplicationID: app.ID, Name: "bad", Kind: "webhook", Enabled: true, Events: []string{"security.advisory"}, Destination: dest}, 0); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO slack_integration(singleton,client_configuration,bot_configuration,team_id,team_name,channel_id,channel_name,channel_private,events,revision,connected_by) VALUES(true,'\\x00','\\x00','T','team','C','chan',false,ARRAY['security.advisory'],1,$1)", p.ID); err != nil {
		t.Fatal(err)
	}
	cand := SecurityCandidate{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Name: app.Name, Revision: 3, DeploymentID: "dep"}
	f := advisory.Finding{AdvisoryID: "HKP-1", AdvisoryRevision: 1, Severity: advisory.High, Title: "Node issue", Service: "api", Evidence: "node 22.3.0"}
	count := func(q string) (n int) {
		if err := db.Pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	for i, want := range []bool{true, false} {
		queued, err := db.QueueSecurityNotification(ctx, cand, f, "https://hakopod.example/a")
		if err != nil || queued != want {
			t.Fatal(i, queued, err)
		}
	}
	if n := count("SELECT count(*) FROM deployment_notification_deliveries WHERE target_id='" + with.ID + "' AND payload->>'type'='security_advisory'"); n != 1 {
		t.Fatal("deliveries for target with event", n)
	}
	if n := count("SELECT count(*) FROM deployment_notification_deliveries"); n != 1 {
		t.Fatal("target without event received a delivery", n)
	}
	if n := count("SELECT count(*) FROM slack_event_outbox WHERE event_kind='security.advisory'"); n != 1 {
		t.Fatal("slack outbox", n)
	}
	// A new advisory revision notifies again.
	f.AdvisoryRevision = 2
	if queued, err := db.QueueSecurityNotification(ctx, cand, f, ""); err != nil || !queued {
		t.Fatal(queued, err)
	}
	if queued, err := db.QueuePatchResultNotification(ctx, cand, PatchAttempt{ID: "att", AdvisoryID: "HKP-1", AdvisoryRevision: 2, Service: "api", Error: "rollout failed"}, ""); err != nil || !queued {
		t.Fatal(queued, err)
	}
	if n := count("SELECT count(*) FROM deployment_notification_deliveries WHERE payload->>'type'='security_patch_failed'"); n != 1 {
		t.Fatal("patch delivery", n)
	}
	// Full queue: nothing queued and nothing marked, so a later scan retries.
	if _, err = db.Pool.Exec(ctx, "INSERT INTO deployment_notification_deliveries(id,target_id,target_revision,payload) SELECT 'fill-'||n,$1,1,'{}' FROM generate_series(1,10000) n", with.ID); err != nil {
		t.Fatal(err)
	}
	f.AdvisoryRevision = 3
	if queued, err := db.QueueSecurityNotification(ctx, cand, f, ""); err != nil || queued {
		t.Fatal("queued past cap", queued, err)
	}
	if n := count("SELECT count(*) FROM security_notifications WHERE advisory_revision=3"); n != 0 {
		t.Fatal("capped notification was marked sent", n)
	}
}
