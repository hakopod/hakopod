package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveSlackEventOutboxAcceptance(t *testing.T) {
	if os.Getenv("HAKOPOD_SLACK_LIVE_TEST") != "1" {
		t.Skip("set HAKOPOD_SLACK_LIVE_TEST=1 for real-cluster Slack outbox acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, principal := workerDatabase(t)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO slack_integration(client_configuration,bot_configuration,team_id,team_name,channel_id,channel_name,events,revision,connected_by)
	 VALUES($1,$2,'Tlive','Live fixture','Clive','live-fixture',$3,1,$4)`, []byte("client"), []byte("bot"), []string{
		"application.created", "service.added", "service.scale.updated", "service.restart.requested",
		"service.ready", "deployment.queued", "deployment.started", "deployment.succeeded",
	}, principal.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.ConfigureSlackCloudEvents(ctx, "live-slack-source"); err != nil {
		t.Fatal(err)
	}
	client, err := cluster.New(kubeconfig, cluster.Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "slack-live", Services: map[string]spec.Service{
		"api": liveSlackService(), "worker": liveSlackService(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: db, Cluster: client, Timeout: 2 * time.Minute}
	first, err := db.Accept(ctx, principal, "demo", "development", app, 0, "live-slack-events-first")
	if err != nil {
		t.Fatal(err)
	}
	cleanupTarget := cluster.Target{ApplicationID: first.ApplicationID, Project: "demo", Environment: "development", OperationID: "live-slack-events-cleanup", Revision: 2, Spec: app}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		// DeletePreview verifies Hakopod ownership and namespace UID before it
		// removes this test's namespace; it never enumerates other applications.
		if err := client.DeletePreview(clean, cleanupTarget); err != nil {
			t.Error("owned Slack live fixture cleanup", err)
		}
	})
	run := func(id string) store.Deployment {
		claim, claimErr := db.Claim(ctx)
		if claimErr != nil || claim == nil {
			t.Fatal("claim live deployment", claimErr)
		}
		w.run(ctx, claim)
		deployed, readErr := db.Deployment(ctx, id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return deployed
	}
	if deployed := run(first.ID); deployed.Status != "succeeded" {
		t.Fatalf("first real deployment status=%s error=%q", deployed.Status, deployed.Error)
	}
	observed, err := client.Observe(ctx, cluster.Target{ApplicationID: first.ApplicationID, Project: "demo", Environment: "development", OperationID: first.ID, Revision: 1, Spec: app})
	if err != nil || observed.Status != "healthy" || len(observed.Services) != 2 {
		t.Fatalf("real services not ready: status=%s services=%d err=%v", observed.Status, len(observed.Services), err)
	}

	next := app
	next.Services = map[string]spec.Service{}
	for name, service := range app.Services {
		service.Replicas = 2
		service.RestartNonce = "live-slack-r2"
		next.Services[name] = service
	}
	second, err := db.Accept(ctx, principal, "demo", "development", next, 1, "live-slack-events-update")
	if err != nil {
		t.Fatal(err)
	}
	cleanupTarget.Spec = next
	if deployed := run(second.ID); deployed.Status != "succeeded" {
		t.Fatalf("updated real deployment status=%s error=%q", deployed.Status, deployed.Error)
	}
	assertLiveSlackOutboxExact(t, ctx, db, "self_hosted", "application.created", "", 1)
	assertLiveSlackOutboxExact(t, ctx, db, "self_hosted", "service.added", "", 2)
	assertLiveSlackOutboxExact(t, ctx, db, "self_hosted", "service.scale.updated", "2", 2)
	assertLiveSlackOutboxExact(t, ctx, db, "self_hosted", "service.restart.requested", "2", 2)
	assertLiveSlackOutboxExact(t, ctx, db, "self_hosted", "deployment.succeeded", "", 2)
	assertLiveSlackOutboxExact(t, ctx, db, "cloud", "service.scale.updated", "2", 2)
	var readyCount, readySources, services int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT source_id),count(DISTINCT payload->>'service') FROM slack_event_outbox WHERE delivery_target='self_hosted' AND event_kind='service.ready' AND payload->>'revision'='2'`).Scan(&readyCount, &readySources, &services); err != nil || readyCount != 2 || readySources != 2 || services != 2 {
		t.Fatalf("revision 2 ready records count=%d sources=%d services=%d err=%v", readyCount, readySources, services, err)
	}
	t.Log("real k3d-hakopod-dev services were observed ready and produced selected self-hosted plus Cloud outbox records; no Slack transport was started")
}

func liveSlackService() spec.Service {
	return spec.Service{
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a",
		Port:  8080, Command: []string{"python", "-B", "-m", "http.server", "8080"},
		RunAsUser: 12345, RunAsGroup: 12345, FSGroup: 12345, ReadOnlyRootFilesystem: true,
		Resources: &spec.Resources{CPURequest: "10m", CPULimit: "100m", MemoryRequest: "64Mi", MemoryLimit: "128Mi"},
	}
}

func assertLiveSlackOutboxExact(t *testing.T, ctx context.Context, db *store.Store, target, event, revision string, want int) {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM slack_event_outbox WHERE delivery_target=$1 AND event_kind=$2 AND ($3='' OR payload->>'revision'=$3)", target, event, revision).Scan(&count); err != nil || count != want {
		t.Fatalf("%s %s revision=%q records=%d want=%d err=%v", target, event, revision, count, want, err)
	}
}
