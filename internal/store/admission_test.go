package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestEmbeddingAdmissionIsAtomicAndDoesNotReplay(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	denied := errors.New("approval required")
	db.AdmitDeployment = func(ctx context.Context, tx pgx.Tx, p Principal, project, env, idem string) error { return denied }
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "admission-denied"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 0 {
		t.Fatal("denied admission allocated resources", count, err)
	}
	calls := 0
	db.AdmitDeployment = func(ctx context.Context, tx pgx.Tx, p Principal, project, env, idem string) error {
		calls++
		_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'test.admission',$3)", p.ID, p.KeyID, idem)
		return err
	}
	accepted, err := db.Accept(ctx, p, "demo", "development", app, 0, "admission-approved")
	if err != nil {
		t.Fatal(err)
	}
	db.AdmitDeployment = func(context.Context, pgx.Tx, Principal, string, string, string) error { return denied }
	replay, err := db.Accept(ctx, p, "demo", "development", app, 0, "admission-approved")
	if err != nil || replay.ID != accepted.ID || calls != 1 {
		t.Fatal("idempotent result repeated admission", err)
	}
	if _, err = db.Accept(ctx, p, "demo", "development", app, 1, "admission-new-change"); !errors.Is(err, denied) {
		t.Fatal("new revision bypassed policy", err)
	}
}
