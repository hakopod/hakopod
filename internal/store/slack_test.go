package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/hakopod/hakopod/internal/spec"
)

func seedSlackIntegration(t *testing.T, db *Store, p Principal, events []string, revision int64) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(), `INSERT INTO slack_integration(client_configuration,bot_configuration,team_id,team_name,channel_id,channel_name,events,revision,connected_by) VALUES($1,$2,'Tfixture','Fixture team','Cfixture','fixture', $3,$4,$5)`, []byte("client"), []byte("bot"), events, revision, p.ID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSlackOutboxIsTransactionalAndSnapshotsIntegrationRevision(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	seedSlackIntegration(t, db, p, []string{"audit", "alarm.opened"}, 7)
	if err := db.ConfigureSlackCloudEvents(ctx, "shared-runtime-a"); err != nil {
		t.Fatal(err)
	}
	appID := NewID()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO applications(id,name,project,environment,spec) VALUES($1,'slack-fixture','demo','development',$2)", appID, JSON(emptyTestSpec())); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'fixture.rollback','item')", p.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback captured Slack event: count=%d err=%v", count, err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'fixture.commit','item')", p.ID); err != nil {
		t.Fatal(err)
	}
	var event, actor string
	var revision int64
	if err = db.Pool.QueryRow(ctx, "SELECT event_kind,payload->>'actor_id',integration_revision FROM slack_event_outbox").Scan(&event, &actor, &revision); err != nil {
		t.Fatal(err)
	}
	if event != "audit" || actor != p.ID || revision != 7 {
		t.Fatalf("unexpected committed capture %q %q %d", event, actor, revision)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE slack_integration SET revision=8"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'fixture.reconnect','item')", p.ID); err != nil {
		t.Fatal(err)
	}
	var latest int64
	if err = db.Pool.QueryRow(ctx, "SELECT integration_revision FROM slack_event_outbox WHERE delivery_target='self_hosted' ORDER BY id DESC LIMIT 1").Scan(&latest); err != nil || latest != 8 {
		t.Fatalf("new event did not snapshot reconnect revision: %d %v", latest, err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource,metadata) VALUES($1,'fixture.cloud', $2,$3)", p.ID, appID, JSON(map[string]string{"application_id": appID})); err != nil {
		t.Fatal(err)
	}
	var source, project, environment string
	if err = db.Pool.QueryRow(ctx, "SELECT runtime_source_id,payload->>'project',payload->>'environment' FROM slack_event_outbox WHERE delivery_target='cloud' ORDER BY id DESC LIMIT 1").Scan(&source, &project, &environment); err != nil || source != "shared-runtime-a" || project != "demo" || environment != "development" {
		t.Fatalf("Cloud event scope was not authoritative: %q %q %q %v", source, project, environment, err)
	}

	if _, err = db.Pool.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,credentials) VALUES($1,'demo','development','slack-db',1,$2,$3)", "slack-db-id", JSON(map[string]any{}), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	platformID := strings.Repeat("a", 32)
	if _, err = db.Pool.Exec(ctx, "INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec) VALUES($1,'demo','development','slack-platform','neon',1,$2)", platformID, JSON(map[string]string{"name": "slack-platform", "kind": "neon"})); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES('virtual-network','demo','development','slack-network',1,$1)", JSON(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource string
		metadata []byte
	}{
		{resource: "slack-db-id", metadata: JSON(map[string]any{})},
		{resource: platformID, metadata: JSON(map[string]any{})},
		{resource: "demo/development/slack-network", metadata: JSON(map[string]any{})},
		{resource: "deleted-app", metadata: JSON(map[string]string{"project": "demo", "environment": "development"})},
	} {
		var auditID int64
		action := "fixture.scoped"
		if tc.resource == "deleted-app" {
			action = "application.delete"
		}
		if err = db.Pool.QueryRow(ctx, "INSERT INTO audit_events(identity_id,action,resource,metadata) VALUES($1,$2,$3,$4) RETURNING id", p.ID, action, tc.resource, tc.metadata).Scan(&auditID); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(ctx, "SELECT payload->>'project',payload->>'environment' FROM slack_event_outbox WHERE delivery_target='cloud' AND source_id=$1", auditID).Scan(&project, &environment); err != nil || project != "demo" || environment != "development" {
			t.Fatalf("Cloud audit scope was not resolved from %q: %q %q %v", tc.resource, project, environment, err)
		}
	}
	var unscopedID, unscopedCount int64
	if err = db.Pool.QueryRow(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'fixture.unscoped','installation') RETURNING id", p.ID).Scan(&unscopedID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE delivery_target='cloud' AND source_id=$1", unscopedID).Scan(&unscopedCount); err != nil || unscopedCount != 0 {
		t.Fatalf("unscoped audit reached Cloud: count=%d err=%v", unscopedCount, err)
	}
	if err = db.Pool.QueryRow(ctx, "INSERT INTO audit_events(identity_id,action,resource,metadata) VALUES($1,'fixture.forged','installation',$2) RETURNING id", p.ID, JSON(map[string]string{"project": "demo", "environment": "development"})).Scan(&unscopedID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE delivery_target='cloud' AND source_id=$1", unscopedID).Scan(&unscopedCount); err != nil || unscopedCount != 0 {
		t.Fatalf("forged metadata reached Cloud: count=%d err=%v", unscopedCount, err)
	}
}

func TestSlackDeliveryHistoryCursorReturnsEveryRecord(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	principal.CredentialType = "browser"
	ctx := context.Background()
	ids := make([]int64, 0, 3)
	for source := 1; source <= 3; source++ {
		var id int64
		if err := db.Pool.QueryRow(ctx, "INSERT INTO slack_event_outbox(delivery_target,event_kind,source_id,payload) VALUES('self_hosted','test',$1,$2) RETURNING id", source, JSON(map[string]any{"schema_version": 1})).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	first, next, err := db.SlackDeliveryHistory(ctx, principal, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] || next != ids[1] {
		t.Fatalf("first Slack history page=%#v next=%d err=%v", first, next, err)
	}
	second, after, err := db.SlackDeliveryHistory(ctx, principal, next, 2)
	if err != nil || len(second) != 1 || second[0].ID != ids[0] || after != 0 {
		t.Fatalf("second Slack history page=%#v next=%d err=%v", second, after, err)
	}
}

func TestSlackClaimsAreExclusiveAndPruned(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	seedSlackIntegration(t, db, p, []string{"audit"}, 1)
	if _, err := db.Pool.Exec(ctx, "INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload) VALUES('self_hosted',1,'test',1,$1)", JSON(map[string]any{"schema_version": 1})); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan *SlackDelivery, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := db.ClaimSlackDelivery(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			claims <- job
		}()
	}
	wg.Wait()
	close(claims)
	claimed := 0
	var job *SlackDelivery
	for value := range claims {
		if value != nil {
			claimed++
			job = value
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent claims=%d", claimed)
	}
	if err := db.FinishSlackDelivery(ctx, *job, "sent", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO slack_event_outbox(delivery_target,event_kind,source_id,payload,created_at) VALUES('self_hosted','test',2,$1,now()-interval '25 hours')", JSON(map[string]any{"schema_version": 1})); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSlackDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM slack_event_outbox WHERE source_id=2").Scan(&status); err != nil || status != "failed" {
		t.Fatalf("stale delivery status=%q err=%v", status, err)
	}
	browser := p
	browser.CredentialType = "browser"
	if _, err := db.QueueSlackTest(ctx, browser, 1); !errors.Is(err, ErrLicenseRequired) {
		t.Fatalf("unlicensed test accepted: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO slack_event_outbox(delivery_target,event_kind,source_id,payload,status,attempts,lease_id,lease_until,created_at) VALUES('self_hosted','test',3,$1,'sending',8,'active-lease',now()+interval '1 minute',now()-interval '25 hours')", JSON(map[string]any{"schema_version": 1})); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSlackDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM slack_event_outbox WHERE source_id=3").Scan(&status); err != nil || status != "sending" {
		t.Fatalf("in-flight final attempt was pruned: %q %v", status, err)
	}

	if err := db.ConfigureSlackCloudRelay(ctx, "cloud-source-a", "demo", "development"); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"cloud-source-a", "cloud-source-b"} {
		if _, err := db.Pool.Exec(ctx, "INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload) VALUES('cloud',$1,'audit',99,$2)", source, JSON(map[string]any{"project": "demo", "environment": "development"})); err != nil {
			t.Fatal(err)
		}
	}
	cloudSource := SlackCloudEventSource{ID: "cloud-source-a", Project: "demo", Environment: "development"}
	foreignSource := SlackCloudEventSource{ID: "cloud-source-b", Project: "demo", Environment: "development"}
	wrongScope := SlackCloudEventSource{ID: "cloud-source-a", Project: "demo", Environment: "production"}
	cloudJob, err := db.ClaimSlackCloudEventForSource(ctx, cloudSource)
	if err != nil || cloudJob == nil || cloudJob.SourceID != "cloud-source-a" {
		t.Fatalf("source-bound Cloud claim=%#v err=%v", cloudJob, err)
	}
	if err = db.FinishSlackCloudEventForSource(ctx, foreignSource, *cloudJob, "sent", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign Cloud source acknowledged lease: %v", err)
	}
	if _, err = db.ClaimSlackCloudEventForSource(ctx, wrongScope); err != nil {
		t.Fatalf("scope-mismatched claim returned an error: %v", err)
	}
	if err = db.AckSlackCloudEventLease(ctx, wrongScope, cloudJob.Sequence, cloudJob.LeaseID, "sent", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("scope-mismatched Cloud source acknowledged lease: %v", err)
	}
	if err = db.AckSlackCloudEventLease(ctx, cloudSource, cloudJob.Sequence, cloudJob.LeaseID, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if cloudJob, err = db.ClaimSlackCloudEventForSource(ctx, cloudSource); err != nil || cloudJob != nil {
		t.Fatalf("source-bound claim leaked another source: %#v %v", cloudJob, err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM slack_event_outbox WHERE delivery_target='cloud' AND runtime_source_id='cloud-source-b'").Scan(&status); err != nil || status != "pending" {
		t.Fatalf("other Cloud source was affected: %q %v", status, err)
	}
}

func TestSlackDeploymentEventsAreCommittedScopedAndSelected(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	p := bootstrapPrincipal(t, db)
	seedSlackIntegration(t, db, p, []string{"application.created", "application.configuration.updated", "service.image.updated", "service.ready", "deployment.started", "deployment.succeeded", "deployment.rollback.requested", "deployment.cancellation.requested", "application.deleted", "service.renamed"}, 11)
	if err := db.ConfigureSlackCloudEvents(ctx, "shared-runtime-events"); err != nil {
		t.Fatal(err)
	}
	first, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), 0, "slack-event-accept-1")
	if err != nil {
		t.Fatal(err)
	}
	var created int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='application.created'").Scan(&created); err != nil || created != 2 {
		t.Fatalf("application creation deliveries=%d err=%v", created, err)
	}
	noop, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), 1, "slack-event-noop")
	if err != nil {
		t.Fatal(err)
	}
	var noops int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='application.configuration.updated'").Scan(&noops); err != nil || noops != 0 {
		t.Fatalf("no-op acceptance emitted configuration notification: %d %v", noops, err)
	}

	// A rolled-back deployment event cannot escape through either outbox target.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message,service) VALUES($1,'service.image.updated','secret image must not appear','api')", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var imageCount int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='service.image.updated'").Scan(&imageCount); err != nil || imageCount != 0 {
		t.Fatalf("rolled back image update escaped: %d %v", imageCount, err)
	}

	if _, err = db.Pool.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message,service) VALUES($1,'service.image.updated','redacted','api'),($1,'service.image.updated','redacted','worker')", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='service.image.updated' AND delivery_target='self_hosted'").Scan(&imageCount); err != nil || imageCount != 2 {
		t.Fatalf("per-service self-hosted records=%d err=%v", imageCount, err)
	}
	var service, applicationName, revision, phase string
	if err = db.Pool.QueryRow(ctx, "SELECT payload->>'service',payload->>'application_name',payload->>'revision',payload->>'runtime_phase' FROM slack_event_outbox WHERE event_kind='service.image.updated' AND delivery_target='self_hosted' ORDER BY id LIMIT 1").Scan(&service, &applicationName, &revision, &phase); err != nil || service == "" || applicationName != "store-test" || revision != "1" || phase != "deployment" {
		t.Fatalf("unsafe or incomplete deployment payload service=%q app=%q revision=%q phase=%q err=%v", service, applicationName, revision, phase, err)
	}
	var hasMessage bool
	if err = db.Pool.QueryRow(ctx, "SELECT payload ? 'message' OR payload ? 'spec' FROM slack_event_outbox WHERE event_kind='service.image.updated' LIMIT 1").Scan(&hasMessage); err != nil || hasMessage {
		t.Fatalf("unsafe payload body retained: %v %v", hasMessage, err)
	}

	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='running' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='running' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	var starts int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='deployment.started'").Scan(&starts); err != nil || starts != 2 {
		t.Fatalf("status transition was not idempotent: %d %v", starts, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	var terminal int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='deployment.succeeded'").Scan(&terminal); err != nil || terminal != 2 {
		t.Fatalf("terminal status was not authoritative: %d %v", terminal, err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message) VALUES($1,'succeeded','provisional worker message')", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='deployment.succeeded'").Scan(&terminal); err != nil || terminal != 2 {
		t.Fatalf("provisional event produced duplicate terminal delivery: %d %v", terminal, err)
	}
	rollbackSpec := emptyTestSpec()
	rollbackResolved := rollbackSpec
	rollbackService := rollbackResolved.Services["api"]
	rollbackService.Image = "python:3.13-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rollbackResolved.Services["api"] = rollbackService
	rollback, err := db.AcceptRollback(ctx, p, "demo", "development", rollbackSpec, 2, "slack-event-rollback", rollbackResolved)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := db.AcceptRollback(ctx, p, "demo", "development", rollbackSpec, 2, "slack-event-rollback", rollbackResolved); err != nil || repeated.ID != rollback.ID {
		t.Fatalf("rollback idempotency=%#v %v", repeated, err)
	}
	var rollbacks int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='deployment.rollback.requested'").Scan(&rollbacks); err != nil || rollbacks != 2 {
		t.Fatalf("rollback intent was not durably emitted: %d %v", rollbacks, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET cancel_requested=true WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET cancel_requested=true WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	var cancellation int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE event_kind='deployment.cancellation.requested'").Scan(&cancellation); err != nil || cancellation != 2 {
		t.Fatalf("cancellation request was not idempotent: %d %v", cancellation, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET recovery_state='running',recovery_revision=1 WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message,service) VALUES($1,'ready','safe','api')", first.ID); err != nil {
		t.Fatal(err)
	}
	var recoveryPhase, recoveryRevision string
	if err = db.Pool.QueryRow(ctx, "SELECT payload->>'runtime_phase',payload->>'recovery_revision' FROM slack_event_outbox WHERE event_kind='service.ready' AND delivery_target='cloud'").Scan(&recoveryPhase, &recoveryRevision); err != nil || recoveryPhase != "recovery" || recoveryRevision != "1" {
		t.Fatalf("recovery context=%q %q %v", recoveryPhase, recoveryRevision, err)
	}

	if _, err = db.RenameApplication(ctx, p, first.ApplicationID, "api", "Renamed API", 1); err != nil {
		t.Fatal(err)
	}
	var renamedService string
	if err = db.Pool.QueryRow(ctx, "SELECT payload->>'service' FROM slack_event_outbox WHERE event_kind='service.renamed' AND delivery_target='self_hosted'").Scan(&renamedService); err != nil || renamedService != "api" {
		t.Fatalf("service rename was not scoped: %q %v", renamedService, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='cancelled' WHERE id=ANY($1)", []string{noop.ID, rollback.ID}); err != nil {
		t.Fatal(err)
	}
	empty, err := spec.Normalize(spec.Application{Name: "store-test", Services: map[string]spec.Service{}})
	if err != nil {
		t.Fatal(err)
	}
	emptyDeployment, err := db.Accept(ctx, p, "demo", "development", empty, 3, "slack-event-empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", emptyDeployment.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, p, first.ApplicationID, 4, "store-test"); err != nil {
		t.Fatal(err)
	}
	var project, environment string
	if err = db.Pool.QueryRow(ctx, "SELECT payload->>'project',payload->>'environment' FROM slack_event_outbox WHERE event_kind='application.deleted' AND delivery_target='cloud'").Scan(&project, &environment); err != nil || project != "demo" || environment != "development" {
		t.Fatalf("deleted application lost durable scope: %q %q %v", project, environment, err)
	}
}

func TestSlackCatalogRoundTripsThroughDatabaseChecks(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	p := bootstrapPrincipal(t, db)
	events := make([]string, 0, len(slackevents.Catalog()))
	for _, definition := range slackevents.Catalog() {
		events = append(events, definition.ID)
	}
	seedSlackIntegration(t, db, p, events, 1)
	for i, event := range events {
		if _, err := db.Pool.Exec(ctx, "INSERT INTO slack_event_outbox(delivery_target,event_kind,source_id,payload) VALUES('self_hosted',$1,$2,$3)", event, int64(i+1), JSON(map[string]any{"schema_version": 1})); err != nil {
			t.Fatalf("catalog ID %q rejected by database: %v", event, err)
		}
	}
}
