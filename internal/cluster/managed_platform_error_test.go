package cluster

import (
	"errors"
	"strings"
	"testing"
)

func TestManagedPlatformRuntimeErrorRedactsCause(t *testing.T) {
	const secret = "password=must-not-escape"
	for _, test := range []struct {
		category string
		want     string
	}{
		{category: "supabase_apply_statefulset", want: "Supabase StatefulSet reconciliation failed"},
		{category: "supabase_rotate_database_credentials", want: "Supabase database credential rotation failed"},
		{category: "unapproved-stage", want: "managed platform reconciliation failed"},
	} {
		err := (&ManagedPlatformRuntimeError{Category: test.category, Err: errors.New(secret)}).Error()
		if err != test.want {
			t.Fatalf("Error() = %q, want %q", err, test.want)
		}
		if strings.Contains(err, secret) {
			t.Fatal("wrapped cause escaped into the safe error")
		}
	}
}
