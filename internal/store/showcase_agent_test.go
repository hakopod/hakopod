package store

import (
	"context"
	"errors"
	"testing"
)

func TestShowcaseAgentRemovalAuthority(t *testing.T) {
	s := isolatedDatabase(t)
	p := Principal{Admin: true, CredentialType: "machine", Permissions: []string{"admin"}}
	if err := s.RequestShowcaseRemoval(context.Background(), p, 1, "", 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin-only machine accepted", err)
	}
	p.Permissions = []string{"admin", "agent:admin"}
	p.Project = "foreign"
	if err := s.RequestShowcaseRemoval(context.Background(), p, 1, "", 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped admin accepted", err)
	}
	p.Project = ""
	if err := s.RequestShowcaseRemoval(context.Background(), p, 1, "", 0); errors.Is(err, ErrForbidden) {
		t.Fatal("explicit installation rejected", err)
	}
}
