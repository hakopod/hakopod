package store

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationVersionsCannotSilentlySkipSchema(t *testing.T) {
	for _, files := range []fstest.MapFS{
		{"053_actions.sql": {}, "053_databases.sql": {}},
		{"53_actions.sql": {}, "053_databases.sql": {}},
		{"invalid.sql": {}},
		{"000_invalid.sql": {}},
	} {
		entries, err := fs.ReadDir(files, ".")
		if err != nil {
			t.Fatal(err)
		}
		if err = validateMigrationVersions(entries); err == nil {
			t.Fatal("ambiguous or invalid migration versions accepted")
		}
	}
	entries, err := migrations.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateMigrationVersions(entries); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRecordsMatchTheirFilenames(t *testing.T) {
	entries, err := migrations.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	record := regexp.MustCompile(`(?i)INSERT\s+INTO\s+schema_migrations\s*\(\s*version\s*\)\s+VALUES\s*\(\s*(\d+)\s*\)`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		body, err := migrations.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range record.FindAllSubmatch(body, -1) {
			recorded, err := strconv.Atoi(string(match[1]))
			if err != nil || recorded != version {
				t.Errorf("%s records migration %s instead of %d", entry.Name(), match[1], version)
			}
		}
	}
}
