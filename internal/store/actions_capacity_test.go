package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func testCapacity(name string) ActionsCapacity {
	return ActionsCapacity{Name: name, Cloud: "aws", Region: "eu-central-1", RoleARN: "arn:aws:iam::123456789012:role/hakopod-actions",
		SubnetIDs: []string{"subnet-0abc1234"}, SecurityGroupIDs: []string{"sg-0abc1234"}, InstanceTypes: []string{"c7i.xlarge", "m7i.xlarge"},
		Architecture: "amd64", Spot: true, MaxInstances: 2, Environments: []string{"demo/development"}}
}

func TestActionsCapacityValidation(t *testing.T) {
	for name, mutate := range map[string]func(*ActionsCapacity){
		"cloud":    func(c *ActionsCapacity) { c.Cloud = "gcp" },
		"region":   func(c *ActionsCapacity) { c.Region = "EU" },
		"role":     func(c *ActionsCapacity) { c.RoleARN = "arn:aws:iam::1:user/x" },
		"subnets":  func(c *ActionsCapacity) { c.SubnetIDs = nil },
		"groups":   func(c *ActionsCapacity) { c.SecurityGroupIDs = []string{"sg-1", "sg-1"} },
		"types":    func(c *ActionsCapacity) { c.InstanceTypes = make([]string, 21) },
		"arch":     func(c *ActionsCapacity) { c.Architecture = "x86" },
		"disk":     func(c *ActionsCapacity) { c.DiskGiB = 5 },
		"max":      func(c *ActionsCapacity) { c.MaxInstances = 0 },
		"scope":    func(c *ActionsCapacity) { c.Environments = []string{"demo"} },
		"external": func(c *ActionsCapacity) { c.ExternalID = "a b" },
	} {
		c := testCapacity("aws-spot")
		mutate(&c)
		if err := c.normalize(); !errors.Is(err, ErrInput) {
			t.Fatal(name, err)
		}
	}
	c := testCapacity("aws-spot")
	if err := c.normalize(); err != nil || c.DiskGiB != 100 {
		t.Fatal(c, err)
	}
	if !c.Allows("demo", "development") || c.Allows("demo", "production") {
		t.Fatal("environment scope")
	}
}

func TestActionsCapacitySettingsAndVMSlots(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	first, rev, err := db.ActionsCapacitySettings(ctx)
	if err != nil || rev != 1 || !strings.HasPrefix(first.ExternalID, "hakopod-") || len(first.Capacities) != 0 {
		t.Fatal(first, rev, err)
	}
	again, _, err := db.ActionsCapacitySettings(ctx)
	if err != nil || again.ExternalID != first.ExternalID {
		t.Fatal("external ID must be stable", err)
	}
	if _, _, err = db.PutActionsCapacity(ctx, principal, []ActionsCapacity{testCapacity("aws-spot")}, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	saved, rev, err := db.PutActionsCapacity(ctx, principal, []ActionsCapacity{testCapacity("aws-spot")}, 1)
	if err != nil || rev != 2 || saved.ExternalID != first.ExternalID {
		t.Fatal(saved, rev, err)
	}
	if c, ok := saved.Find("aws-spot"); !ok || c.ExternalID != first.ExternalID {
		t.Fatal("installation external ID not applied", c)
	}
	var audits int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='settings.actions_capacity'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("capacity change not audited", audits, err)
	}

	// Acceptance refuses a capacity the environment may not use.
	pool := spec.Service{Replicas: 1, Actions: &spec.Actions{Repository: "team/repo", Credential: "github", Capacity: "aws-spot"}}
	release := spec.Application{Name: "runners", Services: map[string]spec.Service{"ci": pool}}
	for _, scope := range []struct {
		project, env string
		ok           bool
	}{{"demo", "development", true}, {"demo", "production", false}} {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = requireActionsCapacityTx(ctx, tx, scope.project, scope.env, release)
		_ = tx.Rollback(ctx)
		if (err == nil) != scope.ok {
			t.Fatal(scope, err)
		}
	}

	deployment, err := db.Accept(ctx, principal, "demo", "development", emptyTestSpec(), 0, "vm-slot-fixture")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, deployment.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SyncActions(ctx, app, release, 1); err != nil {
		t.Fatal(err)
	}
	p, err := db.ActionsPool(ctx, app.ID, "ci")
	if err != nil || p == nil {
		t.Fatal(err)
	}
	if _, _, err = db.PutActionsCapacity(ctx, principal, []ActionsCapacity{}, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("removed a capacity a pool still uses", err)
	}

	// max_instances is a hard ceiling across slots.
	var slots []ActionsSlot
	for range 3 {
		v, err := db.NewActionsSlot(ctx, *p)
		if err != nil {
			t.Fatal(err)
		}
		slots = append(slots, v)
	}
	for i, want := range []bool{true, true, false} {
		ok, err := db.ReserveActionsInstance(ctx, slots[i].ID, "aws-spot", 2)
		if err != nil || ok != want {
			t.Fatal(i, ok, err)
		}
	}
	if ok, err := db.ReserveActionsInstance(ctx, slots[0].ID, "aws-spot", 2); err != nil || !ok {
		t.Fatal("a reserved slot must keep its place", err)
	}
	if n, err := db.ActionsCapacityInUse(ctx, "aws-spot"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if err = db.SaveActionsVMConfig(ctx, slots[0].ID, make([]byte, 40)); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordActionsInstance(ctx, slots[0].ID, "i-0abc", "c7i.xlarge", "spot"); err != nil {
		t.Fatal(err)
	}
	v, err := db.ActionsSlot(ctx, slots[0].ID)
	if err != nil || v == nil || v.InstanceID != "i-0abc" || v.EncryptedRegistration != nil || v.InstanceLifecycle != "spot" {
		t.Fatal("instance not recorded or configuration kept", v, err)
	}
	found, err := db.ActionsVMSlots(ctx, []string{slots[0].ID, NewID()})
	if err != nil || len(found) != 1 || found[slots[0].ID].PoolRemoved {
		t.Fatal(found, err)
	}

	// Three evictions in 30 minutes rest the type; the run is attached once.
	for i, slot := range slots {
		if err = db.RecordActionsEviction(ctx, slot, "aws-spot", "i-"+slot.ID[:8], "c7i.xlarge"); err != nil {
			t.Fatal(i, err)
		}
	}
	hot, err := db.EvictedActionsInstanceTypes(ctx, "aws-spot")
	if err != nil || len(hot) != 1 || hot[0] != "c7i.xlarge" {
		t.Fatal(hot, err)
	}
	if err = db.RecordActionsEvictedJob(ctx, slots[0].ID, "other/repo", 9, 1, "", ""); err != nil {
		t.Fatal(err)
	}
	if pending, _ := db.PendingActionsReruns(ctx); len(pending) != 0 {
		t.Fatal("a repository outside the pool target attached a run")
	}
	if err = db.RecordActionsEvictedJob(ctx, slots[0].ID, "team/repo", 9, 1, "demo", "production"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := db.PendingActionsReruns(ctx); len(pending) != 0 {
		t.Fatal("a delivery from another environment attached a run")
	}
	for _, slot := range slots[:2] {
		if err = db.RecordActionsEvictedJob(ctx, slot.ID, "team/repo", 9, 1, "demo", "development"); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := db.PendingActionsReruns(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatal(pending, err)
	}
	covered, err := db.ClaimActionsRerun(ctx, pending[0], 1)
	if err != nil || covered != 2 {
		t.Fatal("one rerun covers every lost job of the attempt", covered, err)
	}
	jobs, err := db.ClaimActionsRerun(ctx, pending[1], 1)
	if err != nil || jobs != 0 {
		t.Fatal("attempt rerun twice", jobs, err)
	}
	if err = db.FinishActionsRerun(ctx, pending[0], covered, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, true, false} {
		if ok, err := db.TakeActionsOnDemandLaunch(ctx, app.ID, "ci"); err != nil || ok != want {
			t.Fatal("on-demand grant", ok, err)
		}
	}
	// The next attempt is past the pool's retry budget.
	if err = db.RecordActionsEvictedJob(ctx, slots[2].ID, "team/repo", 9, 2, "", ""); err != nil {
		t.Fatal(err)
	}
	pending, _ = db.PendingActionsReruns(ctx)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if jobs, err = db.ClaimActionsRerun(ctx, pending[0], 1); err != nil || jobs != 0 {
		t.Fatal("rerun beyond spot_retries", jobs, err)
	}
	evictions, err := db.ActionsEvictions(ctx, app.ID, "ci")
	if err != nil || len(evictions) != 3 {
		t.Fatal(evictions, err)
	}
	for _, e := range evictions {
		if e.RunAttempt == 2 && !strings.Contains(e.RerunError, "spot retries") {
			t.Fatal("exhausted retries not explained", e)
		}
	}
}
