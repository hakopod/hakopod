package store

import (
	"context"
	"errors"
	"strings"
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

func TestEveryAcceptedRevisionChecksRuntimePolicy(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	denied := errors.New("managed-cloud resource limit")
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { return denied }
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "ordinary-policy-denied"); !errors.Is(err, denied) {
		t.Fatal("ordinary spec bypassed runtime validator", err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications WHERE name=$1", app.Name).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed acceptance left application", err)
	}
}

func TestResolvedRollbackRevisionAlsoChecksRuntimePolicy(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	resolved := emptyTestSpec()
	svc := resolved.Services["api"]
	svc.Image = "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	svc.Size = "compute"
	resolved.Services["api"] = svc
	denied := errors.New("resolved revision exceeds cloud limits")
	calls := 0
	db.ValidateDeployment = func(_ context.Context, _ Application, next spec.Application) error {
		calls++
		if next.Services["api"].Size == "compute" {
			return denied
		}
		return nil
	}
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "seed-policy-denied", resolved); !errors.Is(err, denied) {
		t.Fatal("seed revision bypassed policy", err)
	}
	if calls != 2 {
		t.Fatal("both desired and resolved revisions must be validated", calls)
	}
}
