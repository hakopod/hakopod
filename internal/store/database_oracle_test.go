package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestOracleDurableEditionAndApplicationAuthority(t *testing.T) {
	s, p, d := databaseFixture(t)
	d.Spec.Engine, d.Spec.Version = "oracle", "23.26"
	d.Spec.CPU, d.Spec.Memory, d.Spec.StorageGiB = "1", "4Gi", 10
	d.Spec.Oracle = &database.OracleConfig{Edition: "free"}
	ctx := context.Background()
	op, err := s.AcceptDatabase(ctx, p, d, 0, "oracle-development-create", "create")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Database(ctx, p, d.ID, true)
	if err != nil || stored.Spec.Oracle == nil || stored.Spec.Oracle.Edition != "free" {
		t.Fatal("Oracle edition was not durable", err)
	}
	replay, err := s.AcceptDatabase(ctx, p, d, 0, "oracle-development-create", "create")
	if err != nil || replay.ID != op.ID {
		t.Fatal("Oracle creation replay changed identity", err)
	}
	app := p
	app.Application = "unrelated-app"
	if _, err = s.AcceptDatabase(ctx, app, d, 0, "oracle-application-create", "create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("application key gained database administration", err)
	}
	if DatabaseStorageReservation(d.Spec) != 20 {
		t.Fatal("Oracle omitted backup staging from its allocation")
	}
	d.Status, d.Observation.Status = "ready", "ready"
	if err = validateDatabaseBinding(d, spec.Binding{Protocol: "oracle", Endpoint: "read_write"}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []spec.Binding{{Protocol: "mysql", Endpoint: "read_write"}, {Protocol: "oracle", Endpoint: "read_only"}, {Protocol: "oracle", Endpoint: "cluster", ClusterAware: true}, {Protocol: "oracle", Endpoint: "read_write", ClusterAware: true}} {
		if validateDatabaseBinding(d, binding) == nil {
			t.Fatal("Oracle accepted an incompatible application route")
		}
	}
	d.Recovery = &database.Recovery{JobID: NewID()}
	if validateDatabaseBinding(d, spec.Binding{Protocol: "oracle", Endpoint: "read_write"}) == nil {
		t.Fatal("Oracle exposed an uninspected recovery")
	}
}
