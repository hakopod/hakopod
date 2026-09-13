package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func alarmFixture(t *testing.T, hold int, email bool) (*Store, Principal, Application) {
	t.Helper()
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	app := Application{ID: NewID(), Name: "alarm-test", Project: "demo", Environment: "development", Spec: emptyTestSpec(), Revision: 1}
	if _, err := db.Pool.Exec(context.Background(), "INSERT INTO applications(id,name,project,environment,revision,spec) VALUES($1,$2,$3,$4,$5,$6)", app.ID, app.Name, app.Project, app.Environment, app.Revision, JSON(app.Spec)); err != nil {
		t.Fatal(err)
	}
	_, err := db.PutAlarmSettings(context.Background(), p, AlarmScope{Project: app.Project}, AlarmSettingsInput{Enabled: true, HoldSeconds: hold, EmailEnabled: email})
	if err != nil {
		t.Fatal(err)
	}
	return db, p, app
}

func alarmSample(app Application, health string, at time.Time) AlarmObservation {
	return AlarmObservation{AlarmScope: AlarmScope{Project: app.Project, Environment: app.Environment, ApplicationID: app.ID}, Rule: "application-not-ready", ResourceType: "application", ResourceID: app.ID, ResourceName: app.Name, Health: health, Summary: health + " fixture observation", At: at}
}

func TestAlarmUnicodeDiagnosticBounds(t *testing.T) {
	db, p, app := alarmFixture(t, 0, false)
	observation := alarmSample(app, "unhealthy", time.Now().UTC())
	observation.Summary = strings.Repeat("界", 600) + "\x00"
	if err := db.ApplyAlarmObservations(context.Background(), []AlarmObservation{observation}, false); err != nil {
		t.Fatal(err)
	}
	page, err := db.Alarms(context.Background(), p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Summary) > 1024 || !utf8.ValidString(page.Items[0].Summary) {
		t.Fatalf("Unicode diagnostic could not persist safely: %+v %v", page, err)
	}
}

func TestAlarmDuplicateNanosecondSnapshotDoesNotRewriteState(t *testing.T) {
	db, _, app := alarmFixture(t, 0, false)
	ctx := context.Background()
	observation := alarmSample(app, "unhealthy", time.Now().UTC().Truncate(time.Microsecond).Add(789*time.Nanosecond))
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{observation}, false); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.Pool.QueryRow(ctx, "SELECT xmin::text FROM alarm_states WHERE fingerprint=$1", observation.fingerprint()).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{observation}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT xmin::text FROM alarm_states WHERE fingerprint=$1", observation.fingerprint()).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("identical nanosecond JSON snapshot rewrote durable state after PostgreSQL precision conversion")
	}
}

func TestAlarmNodeFailureIgnoresRetiredHistory(t *testing.T) {
	db, p, _ := alarmFixture(t, 0, false)
	ctx := context.Background()
	if _, err := db.PutAlarmSettings(ctx, p, AlarmScope{}, AlarmSettingsInput{Enabled: true, HoldSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-3 * time.Minute)
	if _, err := db.Pool.Exec(ctx, "INSERT INTO alarm_states(fingerprint,last_checked_at,observation_status) SELECT 'node-not-ready:retired-'||n,$1,'unknown' FROM generate_series(1,801) n", base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	node := AlarmObservation{Rule: "node-not-ready", ResourceType: "node", ResourceID: "current-node", ResourceName: "current-node", Health: "unhealthy", Summary: "node not ready", At: base}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{node}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.UnknownNodeAlarms(ctx, base.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	var retired int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_states WHERE fingerprint LIKE 'node-not-ready:retired-%' AND last_checked_at=$1", base.Add(-time.Hour)).Scan(&retired); err != nil || retired != 801 {
		t.Fatalf("retired history was refreshed: %d %v", retired, err)
	}
	node.At = base.Add(140 * time.Second)
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{node}, false); err != nil {
		t.Fatal(err)
	}
	page, err := db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("retired rows starved pending hold reset: %+v %v", page, err)
	}
}

func TestAlarmHoldTransitionsUnknownAndRestartDeduplication(t *testing.T) {
	db, p, app := alarmFixture(t, 60, false)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-3 * time.Minute)
	apply := func(health string, offset time.Duration) {
		t.Helper()
		if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, health, at.Add(offset))}, false, app.Revision); err != nil {
			t.Fatal(err)
		}
	}
	apply("unhealthy", 0)
	apply("unhealthy", 59*time.Second)
	page, err := db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("alarm fired before hold: %+v %v", page, err)
	}
	apply("unhealthy", 60*time.Second)
	page, err = db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 1 || page.Summary.Active != 1 || page.Summary.Unread != 1 {
		t.Fatalf("missing active incident: %+v %v", page, err)
	}
	first := page.Items[0]
	if _, err = db.ReadAlarm(ctx, p, first.ID, true, first.LastEventID); err != nil {
		t.Fatal(err)
	}
	// A new Store instance and concurrent copies of one observation cannot
	// create another incident/event or erase the user's acknowledgement.
	restarted := &Store{Pool: db.Pool, LicenseVerifier: db.LicenseVerifier}
	var group sync.WaitGroup
	for n := 0; n < 8; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := restarted.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, "unhealthy", at.Add(61*time.Second))}, false, app.Revision); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	apply("unknown", 62*time.Second)
	page, err = db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || page.Items[0].Status != "active" || page.Items[0].ObservationStatus != "unknown" || !page.Items[0].Acknowledged || page.Summary.Unread != 0 {
		t.Fatalf("unknown recovered or re-notified an incident: %+v %v", page, err)
	}
	if !page.Items[0].LastObservedAt.Equal(at.Add(61 * time.Second)) {
		t.Fatal("unknown observation replaced the last real observation time")
	}
	apply("healthy", 63*time.Second)
	page, err = db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || page.Items[0].Status != "recovered" || page.Summary.Active != 0 || page.Summary.Unread != 1 || page.Items[0].Acknowledged {
		t.Fatalf("recovery did not create a new unread transition: %+v %v", page, err)
	}
	if _, err = db.ReadAlarm(ctx, p, first.ID, true, first.LastEventID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale read acknowledged an unseen recovery: %v", err)
	}
	apply("healthy", 64*time.Second)
	apply("unhealthy", 60*time.Second) // An old callback cannot reopen it.
	var events int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_events").Scan(&events); err != nil || events != 2 {
		t.Fatalf("expected one active/recovery event pair, got %d: %v", events, err)
	}
}

func TestAlarmPendingHoldResetsOnUnknownGapAndRemoval(t *testing.T) {
	db, p, app := alarmFixture(t, 120, false)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	apply := func(kind string, offset time.Duration) {
		t.Helper()
		if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, kind, base.Add(offset))}, false); err != nil {
			t.Fatal(err)
		}
	}
	apply("unhealthy", 0)
	apply("unknown", 100*time.Second)
	apply("unhealthy", 121*time.Second)
	apply("unhealthy", 8*time.Minute) // Silent restart gap also resets the hold.
	page, err := db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown or downtime counted toward hold: %+v %v", page, err)
	}
	apply("unhealthy", 10*time.Minute)
	page, err = db.Alarms(ctx, p, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("fresh hold failed to fire: %+v %v", page, err)
	}
	service := alarmSample(app, "unhealthy", base.Add(12*time.Minute))
	service.Rule, service.ResourceType, service.ResourceID, service.Service = "service-not-ready", "service", app.ID+"/old", "old"
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{service}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.RemovedAlarmResources(ctx, "service", app.ID, app.Revision, []string{app.ID + "/api"}, service.At.Add(100*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if err := db.RemovedAlarmResources(ctx, "service", app.ID, app.Revision, []string{app.ID + "/api"}, service.At.Add(110*time.Second), false); err != nil {
		t.Fatal(err)
	}
	var retiredAt time.Time
	if err := db.Pool.QueryRow(ctx, "SELECT last_checked_at FROM alarm_states WHERE fingerprint=$1", service.fingerprint()).Scan(&retiredAt); err != nil || !retiredAt.Equal(service.At.Add(100*time.Second)) {
		t.Fatalf("retired state was refreshed indefinitely: %s %v", retiredAt, err)
	}
	service.At = service.At.Add(140 * time.Second)
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{service}, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_incidents WHERE resource_type='service'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("recreated resource reused removed pending hold: %d %v", count, err)
	}
	service.At = service.At.Add(120 * time.Second)
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{service}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.RemovedAlarmResources(ctx, "service", app.ID, app.Revision, []string{}, service.At.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	page, err = db.Alarms(ctx, p, AlarmScope{}, "recovered", "", 25)
	if err != nil || len(page.Items) != 1 || page.Items[0].ObservationStatus != "unknown" {
		t.Fatalf("removal claimed a healthy resource: %+v %v", page, err)
	}
}

func TestAlarmSettingsScopeInboxAndRevisionGuards(t *testing.T) {
	db, p, app := alarmFixture(t, 0, false)
	ctx := context.Background()
	scope := AlarmScope{Project: app.Project, Environment: app.Environment, ApplicationID: app.ID}
	settings, err := db.AlarmSettings(ctx, p, scope)
	if err != nil || !settings.Inherited || settings.Source != "project" || settings.Revision != 0 || settings.HoldSeconds != 0 {
		t.Fatalf("bad inheritance: %+v %v", settings, err)
	}
	viewer := Principal{ID: p.ID, Email: "viewer@example.test", CredentialType: "browser", Permissions: []string{"deployments:read"}, ProjectRoles: []ProjectRole{{Project: app.Project, Role: "viewer"}}}
	if _, err = db.PutAlarmSettings(ctx, viewer, scope, AlarmSettingsInput{Enabled: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer changed alarms: %v", err)
	}
	if _, err = db.AlarmSettings(ctx, viewer, AlarmScope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("project viewer read installation settings: %v", err)
	}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, "unhealthy", time.Now().UTC())}, false, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, "unhealthy", time.Now().UTC())}, false, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutAlarmSettings(ctx, p, AlarmScope{}, AlarmSettingsInput{Enabled: true, HoldSeconds: 0}); err != nil {
		t.Fatal(err)
	}
	node := AlarmObservation{Rule: "node-disk-pressure", ResourceType: "node", ResourceID: "node-a", ResourceName: "node-a", Health: "unhealthy", Summary: "disk pressure", At: time.Now().UTC()}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{node}, false); err != nil {
		t.Fatal(err)
	}
	page, err := db.Alarms(ctx, viewer, AlarmScope{}, "", "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ResourceType != "application" || page.Summary.Active != 1 {
		t.Fatalf("scoped inbox leaked node: %+v %v", page, err)
	}
	wrong := viewer
	wrong.ProjectRoles = []ProjectRole{{Project: "other", Role: "viewer"}}
	if _, err = db.ReadAlarm(ctx, wrong, page.Items[0].ID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read mark bypassed scope: %v", err)
	}
	page, err = db.Alarms(ctx, wrong, AlarmScope{}, "", "", 25)
	if err != nil || len(page.Items) != 0 || page.Summary.Active != 0 {
		t.Fatalf("inbox leaked another scope: %+v %v", page, err)
	}
	page, err = db.Alarms(ctx, p, AlarmScope{}, "", "", 1)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("pagination missing: %+v %v", page, err)
	}
	next, err := db.Alarms(ctx, p, AlarmScope{}, "", page.NextCursor, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.Summary.Active != 2 {
		t.Fatalf("bad bounded continuation: %+v %v", next, err)
	}
	if _, err = db.PutAlarmSettings(ctx, p, scope, AlarmSettingsInput{Enabled: true, HoldSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PutAlarmSettings(ctx, p, scope, AlarmSettingsInput{Enabled: false}); !errors.Is(err, ErrConflict) {
		t.Fatalf("settings revision not enforced: %v", err)
	}
}

func TestAlarmEmailRetryMembershipRevocationAndSupersededEvents(t *testing.T) {
	db, p, app := alarmFixture(t, 0, true)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "UPDATE identities SET email='operator@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	person := NewID()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,email_verified) VALUES($1,'Member','member@example.test',true)", person); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO personal_workspaces(identity_id,project) VALUES($1,$2)", person, app.Project); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, "unhealthy", base)}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.FanoutAlarmEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.FanoutAlarmEmail(ctx); err != nil {
		t.Fatal(err)
	}
	var deliveries int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_email_deliveries").Scan(&deliveries); err != nil || deliveries != 2 {
		t.Fatalf("fanout not idempotent: %d %v", deliveries, err)
	}
	job, err := db.ClaimAlarmEmail(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if _, err := db.AlarmEmailRecipient(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishAlarmEmail(ctx, *job, "retry"); err != nil {
		t.Fatal(err)
	}
	var delay bool
	if err := db.Pool.QueryRow(ctx, "SELECT next_attempt_at>now()+interval '25 seconds' FROM alarm_email_deliveries WHERE event_id=$1 AND identity_id=$2", job.EventID, job.IdentityID).Scan(&delay); err != nil || !delay {
		t.Fatalf("retry had no backoff: %v %v", delay, err)
	}
	if err := db.FinishAlarmEmail(ctx, *job, "sent"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease changed job: %v", err)
	}
	// Remove membership and verify a freshly claimed queued recipient loses access.
	if _, err := db.Pool.Exec(ctx, "DELETE FROM personal_workspaces WHERE identity_id=$1", person); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE alarm_email_deliveries SET next_attempt_at=now() WHERE identity_id=$1", person); err != nil {
		t.Fatal(err)
	}
	var memberJob *AlarmEmail
	for n := 0; n < 2; n++ {
		next, err := db.ClaimAlarmEmail(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next == nil {
			break
		}
		if next.IdentityID == person {
			memberJob = next
			break
		}
		if err := db.FinishAlarmEmail(ctx, *next, "retry"); err != nil {
			t.Fatal(err)
		}
	}
	if memberJob == nil {
		t.Fatal("revoked recipient job not found")
	}
	if _, err := db.AlarmEmailRecipient(ctx, *memberJob); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked recipient retained email access: %v", err)
	}
	if err := db.FinishAlarmEmail(ctx, *memberJob, "skipped"); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAlarmObservations(ctx, []AlarmObservation{alarmSample(app, "healthy", base.Add(time.Second))}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE alarm_email_deliveries SET next_attempt_at=now() WHERE status='pending'"); err != nil {
		t.Fatal(err)
	}
	old, err := db.ClaimAlarmEmail(ctx)
	if err != nil || old == nil {
		t.Fatalf("retry job missing: %+v %v", old, err)
	}
	if _, err := db.AlarmEmailRecipient(ctx, *old); !errors.Is(err, ErrForbidden) {
		t.Fatalf("active email retried after recovery: %v", err)
	}
	if err := db.FinishAlarmEmail(ctx, *old, "skipped"); err != nil {
		t.Fatal(err)
	}
	if err := db.FanoutAlarmEmail(ctx); err != nil {
		t.Fatal(err)
	}
	recovery, err := db.ClaimAlarmEmail(ctx)
	if err != nil || recovery == nil || recovery.Transition != "recovered" {
		t.Fatalf("recovery not queued: %+v %v", recovery, err)
	}
	if _, err := db.AlarmEmailRecipient(ctx, *recovery); err != nil {
		t.Fatal(err)
	}
	// Simulate the process dying on its sixth SMTP attempt: the expired lease
	// must become terminal instead of consuming bounded queue capacity forever.
	if _, err := db.Pool.Exec(ctx, "UPDATE alarm_email_deliveries SET attempts=6,lease_until=now()-interval '1 second' WHERE event_id=$1", recovery.EventID); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneAlarms(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM alarm_email_deliveries WHERE event_id=$1", recovery.EventID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("exhausted expired lease stuck: %s %v", status, err)
	}
}
