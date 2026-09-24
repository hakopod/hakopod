package auth

import (
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"testing"
)

func TestProductCLIIsBoundAndRevocationIsImmediate(t *testing.T) {
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "hakopod_cli_test_" + store.NewID()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	service, err := Open(ctx, parsed.String(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	id := store.NewID()
	if _, err = service.Pool().Exec(ctx, "INSERT INTO identities(id,name,email,permissions,email_verified) VALUES($1,'Fixture','cli@example.test',ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := service.store.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := DeviceScope{ID: store.NewID(), Project: "customer", Environment: "production", Label: "Customer workspace"}
	enabled := true
	service.ConfigureDeviceScopes(func(_ context.Context, u User) ([]DeviceScope, error) {
		if enabled && u.ID == id && u.Verified && !u.MFARequired {
			return []DeviceScope{scope}, nil
		}
		return nil, nil
	})
	request, err := service.store.StartDevice(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.store.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	session, err := service.store.PollDevice(ctx, request.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Verify(ctx, session.Token); err == nil {
		t.Fatal("CLI accepted as browser")
	}
	user, bound, err := service.VerifyCLI(ctx, session.Token)
	if err != nil || bound.ID != scope.ID || user.Operator || user.ID != id {
		t.Fatal("invalid CLI principal", err)
	}
	enabled = false
	if _, _, err = service.VerifyCLI(ctx, session.Token); err == nil {
		t.Fatal("revoked membership retained access")
	}
	enabled = true
	if err = service.store.RevokeSession(ctx, session.User, session.User.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.VerifyCLI(ctx, session.Token); err == nil {
		t.Fatal("revoked session retained access")
	}
	unbound, err := service.store.NewSession(ctx, id, "cli", scope.Project, scope.Environment, []string{"deployments:read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.VerifyCLI(ctx, unbound.Token); err == nil {
		t.Fatal("unbound CLI accepted by product")
	}
}
