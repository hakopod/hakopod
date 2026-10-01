package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestManagedPlatformGlobalListFiltersBeforeLimit(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	for _, project := range []string{"aaa-hidden", "zzz-readable"} {
		if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES($1);", project); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES($1,'development')", project); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(project, name string) {
		t.Helper()
		body := JSON(map[string]any{"name": name, "kind": "supabase"})
		if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec) VALUES($1,$2,'development',$3,'supabase',1,$4)", NewID(), project, name, body); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < MaxManagedPlatforms+1; i++ {
		insert("aaa-hidden", fmt.Sprintf("hidden-%03d", i))
	}
	insert("zzz-readable", "second")
	insert("zzz-readable", "first")
	member := Principal{ID: p.ID, KeyID: p.KeyID, Email: "member@example.test", Permissions: []string{"deployments:read"}, ProjectRoles: []ProjectRole{{Project: "zzz-readable", Role: "custom:read", Permissions: []string{"deployments:read"}}}}
	items, err := s.ManagedPlatforms(ctx, member, "", "")
	if err != nil || len(items) != 2 || items[0].Spec.Name != "first" || items[1].Spec.Name != "second" {
		t.Fatalf("readable ordered global list: %v, %v", items, err)
	}
	if _, err = s.ManagedPlatforms(ctx, p, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("global overflow was not bounded", err)
	}
	if _, err = s.ManagedPlatforms(ctx, member, "aaa-hidden", "development"); !errors.Is(err, ErrForbidden) {
		t.Fatal("inaccessible exact scope accepted", err)
	}
	if _, err = s.ManagedPlatforms(ctx, member, "zzz-readable", ""); !errors.Is(err, ErrInput) {
		t.Fatal("partial scope accepted", err)
	}
	member.Application = "app"
	if _, err = s.ManagedPlatforms(ctx, member, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("application principal listed platforms", err)
	}
	member.Application = ""
	member.IdentityProject = "aaa-hidden"
	items, err = s.ManagedPlatforms(ctx, member, "", "")
	if err != nil || len(items) != 0 {
		t.Fatal("identity scope did not intersect membership", err)
	}
	member.IdentityProject = ""
	member.MFARequired = true
	if _, err = s.ManagedPlatforms(ctx, member, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("MFA-required principal listed platforms", err)
	}
}
