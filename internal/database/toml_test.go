package database

import (
	"strings"
	"testing"
)

func TestDatabaseTOMLIsStrictAndBounded(t *testing.T) {
	text := `schema_version=1
name="orders"
engine="redis"
version="8"
mode="cluster"
shards=3
replicas=1
cpu="100m"
memory="128Mi"
storage_gib=1
`
	got, err := Parse([]byte(text))
	if err != nil || got.Members() != 6 {
		t.Fatal("valid cluster", err)
	}
	for _, bad := range []string{text + "password='secret'\n", strings.Replace(text, "schema_version=1", "schema_version=2", 1), strings.Replace(text, "shards=3", "shards=2", 1), strings.Repeat("#", (64<<10)+1)} {
		if _, err = Parse([]byte(bad)); err == nil {
			t.Fatal("invalid TOML accepted")
		}
	}
}
