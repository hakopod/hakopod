package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/dnsprovider"
)

func sealedDNSCredentials(t *testing.T, p dnsprovider.Provider) []byte {
	t.Helper()
	sealed, err := dnsprovider.SealCredentials(bytes.Repeat([]byte{9}, 32), p, dnsprovider.Credentials{Token: "private-test-dns-token"})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestDNSProviderScopeRevisionCredentialsAndDeleteGuard(t *testing.T) {
	db := isolatedDatabase(t)
	admin := bootstrapPrincipal(t, db)
	ctx := context.Background()

	// The scoping CHECK: a row is installation-wide or belongs to one project and
	// one environment, never to a project alone.
	if _, err := db.Pool.Exec(ctx, "INSERT INTO dns_providers(id,name,kind,project,environment,zone_filter,credentials) VALUES('half','Half','cloudflare','demo','',ARRAY['example.com'],$1)", bytes.Repeat([]byte{1}, 40)); err == nil {
		t.Fatal("half-scoped provider accepted")
	}

	p := dnsprovider.Provider{Name: "Records", Kind: dnsprovider.KindCloudflare, Enabled: true, ZoneFilter: []string{"example.com"}}
	p.EncryptedCredentials = sealedDNSCredentials(t, p)
	if _, err := db.PutDNSProvider(ctx, Principal{}, p, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("unauthenticated principal configured a DNS provider")
	}
	stored, err := db.PutDNSProvider(ctx, admin, p, 0)
	if err != nil || stored.Revision != 1 || stored.ID == "" {
		t.Fatalf("create provider: %v", err)
	}

	// The unique index is per scope and case insensitive.
	duplicate := p
	duplicate.Name = "records"
	duplicate.EncryptedCredentials = sealedDNSCredentials(t, duplicate)
	if _, err = db.PutDNSProvider(ctx, admin, duplicate, 0); err == nil {
		t.Fatal("duplicate provider name accepted in one scope")
	}

	// A stale expected revision conflicts.
	if _, err = db.PutDNSProvider(ctx, admin, stored, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted")
	}

	// Blank credentials preserve the stored ones while the scope changes.
	rescoped := stored
	rescoped.Project, rescoped.Environment = "demo", "development"
	rescoped.EncryptedCredentials = nil
	rescoped, err = db.PutDNSProvider(ctx, admin, rescoped, 1)
	if err != nil {
		t.Fatalf("rescope with blank credentials: %v", err)
	}
	if !bytes.Equal(rescoped.EncryptedCredentials, stored.EncryptedCredentials) || len(rescoped.EncryptedCredentials) == 0 {
		t.Fatal("blank credentials did not preserve the stored credentials")
	}

	// A changed zone filter or kind must not carry the stored credentials.
	widened := rescoped
	widened.ZoneFilter = []string{"example.com", "other.example"}
	widened.EncryptedCredentials = nil
	if _, err = db.PutDNSProvider(ctx, admin, widened, 2); !errors.Is(err, dnsprovider.ErrInput) {
		t.Fatal("widened zone filter carried the stored credentials forward")
	}
	renamed := rescoped
	renamed.Name = "Renamed"
	renamed.EncryptedCredentials = nil
	if _, err = db.PutDNSProvider(ctx, admin, renamed, 2); !errors.Is(err, dnsprovider.ErrInput) {
		t.Fatal("rename carried the stored credentials forward")
	}

	// No management read path returns the sealed credentials.
	list, err := db.DNSProviders(ctx, admin)
	if err != nil || len(list) != 1 {
		t.Fatalf("list providers: %v", err)
	}
	if len(list[0].EncryptedCredentials) != 0 || bytes.Contains(JSON(list), []byte("credential")) {
		t.Fatal("credentials exposed through a read path")
	}
	internal, err := db.DNSProviderCredential(ctx, rescoped.ID)
	if err != nil || !bytes.Equal(internal.EncryptedCredentials, stored.EncryptedCredentials) {
		t.Fatalf("record writer cannot read the sealed credentials: %v", err)
	}

	// The delete guard refuses while records still reference the provider. No
	// table references one in this slice, so the test creates the table the guard
	// looks for.
	if _, err = db.Pool.Exec(ctx, "CREATE TABLE dns_records(provider_id text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO dns_records(provider_id) VALUES($1)", rescoped.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteDNSProvider(ctx, admin, rescoped.ID, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced provider deleted: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, "DELETE FROM dns_records"); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteDNSProvider(ctx, admin, rescoped.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision deleted a provider")
	}
	if err = db.DeleteDNSProvider(ctx, admin, rescoped.ID, 2); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
}

func TestDNSProviderScopedPrincipalManagesOnlyItsOwnScope(t *testing.T) {
	db := isolatedDatabase(t)
	admin := bootstrapPrincipal(t, db)
	ctx := context.Background()
	scoped := Principal{ID: admin.ID, Email: "dev@example.test", CredentialType: "browser", Project: "demo", Environment: "development",
		Permissions: []string{"deployments:write"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "admin"}}}
	if !scoped.CanManageDNSProviders() || scoped.IsAdmin() {
		t.Fatal("scoped project administrator cannot manage DNS providers")
	}
	machine := scoped
	machine.CredentialType, machine.Email = "machine", ""
	machine.Permissions = []string{"git:manage", "deployments:write"}
	if machine.CanManageDNSProviders() {
		t.Fatal("a machine key manages DNS providers without a DNS permission")
	}

	own := dnsprovider.Provider{Name: "Own", Kind: dnsprovider.KindCloudflare, Project: "demo", Environment: "development", Enabled: true, ZoneFilter: []string{"own.example"}}
	own.EncryptedCredentials = sealedDNSCredentials(t, own)
	own, err := db.PutDNSProvider(ctx, scoped, own, 0)
	if err != nil {
		t.Fatalf("scoped create in own scope: %v", err)
	}
	wide := own
	wide.ID, wide.Name, wide.Project, wide.Environment = "", "Wide", "", ""
	wide.EncryptedCredentials = sealedDNSCredentials(t, wide)
	if _, err = db.PutDNSProvider(ctx, scoped, wide, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped principal created an installation-wide provider")
	}
	other := wide
	other.Project, other.Environment = "demo", "production"
	other.EncryptedCredentials = sealedDNSCredentials(t, other)
	if _, err = db.PutDNSProvider(ctx, scoped, other, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped principal wrote into another environment")
	}

	// An installation-wide provider is visible to a delegated principal but not
	// movable or deletable by one.
	installation := dnsprovider.Provider{Name: "Installation", Kind: dnsprovider.KindCloudflare, Enabled: true, ZoneFilter: []string{"shared.example"}}
	installation.EncryptedCredentials = sealedDNSCredentials(t, installation)
	installation, err = db.PutDNSProvider(ctx, admin, installation, 0)
	if err != nil {
		t.Fatalf("administrator create: %v", err)
	}
	if err = db.DeleteDNSProvider(ctx, scoped, installation.ID, 1); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped principal deleted an installation-wide provider")
	}
	list, err := db.DNSProviders(ctx, scoped)
	if err != nil || len(list) != 2 {
		t.Fatalf("scoped list: %v %d", err, len(list))
	}
	if err = db.DeleteDNSProvider(ctx, scoped, own.ID, 1); err != nil {
		t.Fatalf("scoped delete in own scope: %v", err)
	}
}
