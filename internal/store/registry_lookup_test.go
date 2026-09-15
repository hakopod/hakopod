package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRegistryLookupExactScopeAndHost(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('one'),('other'); INSERT INTO environments(project,name) VALUES('one','dev'),('one','production'),('other','dev')"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ kind, project, environment, name, host string }{
		{"registry", "one", "dev", "second", "ghcr.io"}, {"registry", "one", "dev", "first", "ghcr.io"},
		{"registry", "other", "dev", "other-project", "ghcr.io"}, {"registry", "one", "production", "other-environment", "ghcr.io"},
		{"registry", "one", "dev", "other-host", "gcr.io"}, {"registry", "one", "dev", "suffix-host", "ghcr.io.example"}, {"other", "one", "dev", "other-kind", "ghcr.io"},
	} {
		if _, err := db.Pool.Exec(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES($1,$2,$3,$4,1,$5)", item.kind, item.project, item.environment, item.name, JSON(map[string]string{"registry": item.host})); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.RegistryCredentialNames(ctx, "one", "dev", "ghcr.io")
	if err != nil || !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatal("credential lookup crossed scope or host", got, err)
	}
}
