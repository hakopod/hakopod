package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestVirtualNetworkGrantsAndSafeRemoval(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	v := VirtualNetworkMetadata{ID: NewID(), Spec: spec.VirtualNetwork{Name: "commerce", Segments: map[string]spec.NetworkSegment{"data": {Applications: []string{"store-test"}}}}}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 0, v); err != nil {
		t.Fatal(err)
	}
	app := emptyTestSpec()
	app.Networks["default"] = spec.Network{VirtualNetwork: "commerce", Segment: "data", Internal: true}
	resolved, err := db.ResolveVirtualNetworks(ctx, "demo", "development", app)
	if err != nil || resolved["default"] != v.ID+"/data" {
		t.Fatal("valid network grant failed", err)
	}
	for _, change := range []string{"application", "environment", "segment"} {
		other, _ := spec.Normalize(app)
		env := "development"
		if change == "application" {
			other.Name = "unlisted"
		} else if change == "environment" {
			env = "production"
		} else {
			other.Networks["default"] = spec.Network{VirtualNetwork: "commerce", Segment: "missing"}
		}
		if _, err := db.Accept(ctx, p, "demo", env, other, 0, "deny-network-"+change); err == nil {
			t.Fatalf("%s bypassed network grant", change)
		}
	}
	restricted := p
	restricted.Application = app.Name
	if _, err := db.PutRuntimeResource(ctx, restricted, "virtual-network", "demo", "development", "commerce", 1, v); !errors.Is(err, ErrForbidden) {
		t.Fatal("application credential changed network allowlists", err)
	}
	if _, err := db.Accept(ctx, restricted, "demo", "development", app, 0, "allowed-network-join"); err != nil {
		t.Fatal("preapproved application could not join", err)
	}
	removed := v
	removed.Spec.Segments = map[string]spec.NetworkSegment{"data": {Applications: []string{}}}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 1, removed); !errors.Is(err, ErrConflict) {
		t.Fatal("queued application lost its network grant", err)
	}
	finish := func(status string) {
		t.Helper()
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal("missing claim", err)
		}
		defer claim.Release()
		if err := claim.Finish(ctx, status, "", map[string]string{"status": status}); err != nil {
			t.Fatal(err)
		}
	}
	finish("failed")
	app.Networks["default"] = spec.Network{Internal: true}
	if _, err := db.Accept(ctx, p, "demo", "development", app, 1, "network-detach-fails"); err != nil {
		t.Fatal(err)
	}
	finish("failed")
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", v.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("partial failed workload was ignored", err)
	}
	if _, err := db.Accept(ctx, p, "demo", "development", app, 2, "network-detach-success"); err != nil {
		t.Fatal(err)
	}
	finish("succeeded")
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", v.ID, 1); err != nil {
		t.Fatal("unused network could not be deleted", err)
	}
	oldID := v.ID
	v.ID = NewID()
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 0, v); err != nil {
		t.Fatal(err)
	}
	app.Networks["default"] = spec.Network{VirtualNetwork: "commerce", Segment: "data"}
	resolved, err = db.ResolveVirtualNetworks(ctx, "demo", "development", app)
	if err != nil || resolved["default"] == oldID+"/data" {
		t.Fatal("recreated network retained the old runtime identity", err)
	}
}

func TestVirtualNetworkRemovalRetiresOlderConnections(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	network := VirtualNetworkMetadata{ID: NewID(), Spec: spec.VirtualNetwork{Name: "commerce", Segments: map[string]spec.NetworkSegment{"data": {Applications: []string{"store-test"}}}}}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 0, network); err != nil {
		t.Fatal(err)
	}
	app := emptyTestSpec()
	attached := spec.Network{VirtualNetwork: "commerce", Segment: "data", Internal: true}
	detached := spec.Network{Internal: true}
	app.Networks["default"] = attached
	revision := int64(0)
	accept := func() {
		t.Helper()
		if _, err := db.Accept(ctx, p, "demo", "development", app, revision, fmt.Sprintf("network-history-%d", revision)); err != nil {
			t.Fatal(err)
		}
		revision++
	}
	finish := func(status string) {
		t.Helper()
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal("missing claim", err)
		}
		defer claim.Release()
		if err := claim.Finish(ctx, status, "", map[string]string{"status": status}); err != nil {
			t.Fatal(err)
		}
	}
	removed := network.Spec
	removed.Segments = map[string]spec.NetworkSegment{"data": {Applications: []string{}}}
	checkRemoval := func(wantConflict bool) {
		t.Helper()
		err := db.CheckVirtualNetworkChange(ctx, "demo", "development", removed)
		if wantConflict && !errors.Is(err, ErrConflict) || !wantConflict && err != nil {
			t.Fatalf("revision %d: removal conflict=%t, got %v", revision, wantConflict, err)
		}
	}
	accept()
	finish("succeeded")
	app.Networks["default"] = detached
	accept()
	checkRemoval(true)
	finish("failed")
	checkRemoval(true)
	accept()
	finish("succeeded")
	checkRemoval(false)
	// An unrelated queued or failed revision must not revive the retired grant.
	accept()
	checkRemoval(false)
	finish("failed")
	checkRemoval(false)
	// A newer partial connection still needs a later successful detach.
	app.Networks["default"] = attached
	accept()
	finish("failed")
	app.Networks["default"] = detached
	accept()
	checkRemoval(true)
	finish("failed")
	checkRemoval(true)
	accept()
	finish("succeeded")
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", network.ID, 1); err != nil {
		t.Fatal("retired network could not be deleted", err)
	}
}

func TestVirtualNetworkIdentityPreventsStaleMutation(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	original := VirtualNetworkMetadata{ID: NewID(), Spec: spec.VirtualNetwork{Name: "commerce", Description: "Original", Segments: map[string]spec.NetworkSegment{"data": {Applications: []string{}}}}}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 0, original); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("network deletion did not require its reviewed identity", err)
	}
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", original.ID, 1); err != nil {
		t.Fatal(err)
	}
	recreated := original
	recreated.ID = NewID()
	recreated.Spec.Description = "Recreated"
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 0, recreated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 1, original); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review overwrote a recreated network", err)
	}
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", original.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review deleted a recreated network", err)
	}
	row, err := db.RuntimeResource(ctx, "virtual-network", "demo", "development", "commerce")
	if err != nil {
		t.Fatal(err)
	}
	var saved VirtualNetworkMetadata
	if json.Unmarshal(row.Metadata, &saved) != nil || saved.ID != recreated.ID || saved.Spec.Description != "Recreated" || row.Revision != 1 {
		t.Fatal("failed stale mutation changed the recreated network")
	}
	if _, err := db.PutRuntimeResource(ctx, p, "virtual-network", "demo", "development", "commerce", 1, recreated); err != nil {
		t.Fatal("current review could not update the network", err)
	}
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", recreated.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("current identity bypassed stale revision", err)
	}
	if err := db.DeleteVirtualNetwork(ctx, p, "demo", "development", "commerce", recreated.ID, 2); err != nil {
		t.Fatal("current review could not delete the network", err)
	}
}
