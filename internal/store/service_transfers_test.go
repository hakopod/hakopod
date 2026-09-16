package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestServiceTransferWaitsForSuccessAndKeepsRevisionGuards(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	create := func(name string) Application {
		a := emptyTestSpec()
		a.Name = name
		d, err := db.Accept(ctx, p, "demo", "development", a, 0, "create-"+name)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		pin, _ := spec.Normalize(a)
		svc := pin.Services["api"]
		svc.Image = "python@sha256:" + strings.Repeat("a", 64)
		pin.Services["api"] = svc
		if _, err = claim.SetResolved(ctx, pin); err != nil {
			t.Fatal(err)
		}
		if err = claim.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
			t.Fatal(err)
		}
		claim.Release()
		app, err := db.Application(ctx, d.ApplicationID)
		if err != nil {
			t.Fatal(err)
		}
		return app
	}
	source, destination := create("source"), create("destination")
	left, right, err := spec.MoveService(source.Spec, destination.Spec, "api", "moved")
	if err != nil {
		t.Fatal(err)
	}
	move := ServiceTransfer{SourceID: source.ID, DestinationID: destination.ID, SourceRevision: source.Revision, DestinationRevision: destination.Revision, Service: "api", DestinationService: "moved"}
	d, err := db.AcceptServiceTransfer(ctx, p, source, destination, right, move, "start-move")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := db.AcceptServiceTransfer(ctx, p, source, destination, right, move, "start-move")
	if err != nil || replayed.ID != d.ID {
		t.Fatal("start retry", err)
	}
	move, err = db.ServiceTransfer(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptServiceTransfer(ctx, p, source, destination, left, move, "finish-before-success"); !errors.Is(err, ErrConflict) {
		t.Fatal("removed original before destination succeeded", err)
	}
	kept, err := db.Application(ctx, source.ID)
	if err != nil || len(kept.Spec.Services) != 1 || kept.Revision != 1 {
		t.Fatal("original changed", err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	if _, err = db.Pool.Exec(ctx, "UPDATE applications SET revision=revision+1 WHERE id=$1", destination.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptServiceTransfer(ctx, p, source, destination, left, move, "finish-stale-target"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale destination accepted", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE applications SET revision=revision-1 WHERE id=$1", destination.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := db.AcceptServiceTransfer(ctx, p, source, destination, left, move, "finish-move")
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.AcceptServiceTransfer(ctx, p, source, destination, left, move, "finish-move")
	if err != nil || again.ID != removed.ID {
		t.Fatal("finish retry", err)
	}
	if _, err = db.AcceptServiceTransfer(ctx, p, source, destination, left, move, "finish-move-again"); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate removal accepted", err)
	}
	kept, err = db.Application(ctx, source.ID)
	if err != nil || len(kept.Spec.Services) != 0 || kept.Revision != 2 {
		t.Fatal("removal missing", err)
	}
}
