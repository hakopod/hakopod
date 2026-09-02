package api

import (
	"strings"
	"testing"
)

func TestManagementDumpUsesResolvedLibpqEnvironment(t *testing.T) {
	t.Setenv("PGDATABASE", "unrelated-default")
	environment, err := managementDumpEnvironment("postgresql://operator:test%40secret@127.0.0.1:55432/control?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	counts := map[string]int{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
			counts[key]++
		}
	}
	for name, expected := range map[string]string{"PGHOST": "127.0.0.1", "PGPORT": "55432", "PGUSER": "operator", "PGPASSWORD": "test@secret", "PGDATABASE": "control", "PGSSLMODE": "disable"} {
		if values[name] != expected || counts[name] != 1 {
			t.Fatalf("libpq field %s was not resolved exactly once", name)
		}
	}
	if strings.Contains(values["PGDATABASE"], "://") {
		t.Fatal("pg_dump received an unexpanded URI through PGDATABASE")
	}
}
