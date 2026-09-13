package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestDeliveryAcceptanceCannotBypassRuntimeValidation(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.AWSIdentity = "smtp-sender"
	app.Services["api"] = svc
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-not-configured"); err == nil {
		t.Fatal("delivery accepted without configured runtime validation")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications WHERE name=$1", app.Name).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed validation persisted application: count=%d err=%v", count, err)
	}
	denied := errors.New("operator binding revoked")
	calls := 0
	db.ValidateDeployment = func(_ context.Context, stored Application, next spec.Application) error {
		calls++
		if stored.ID == "" || stored.Project != "demo" || stored.Environment != "development" || next.Services["api"].AWSIdentity != "smtp-sender" {
			t.Fatal("runtime validation lost accepted scope")
		}
		return denied
	}
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-scope-denied"); !errors.Is(err, denied) {
		t.Fatalf("validation not enforced: %v", err)
	}
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { calls++; return nil }
	accepted, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-valid-accept")
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { calls++; return denied }
	replay, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-valid-accept")
	if err != nil || replay.ID != accepted.ID {
		t.Fatalf("lost durable idempotency result: %v", err)
	}
	if _, err := db.Accept(ctx, p, "demo", "development", app, 1, "runtime-revoked-next"); !errors.Is(err, denied) {
		t.Fatalf("new revision bypassed revoked grant: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 validations, got %d", calls)
	}
}
