package api

import (
	"strings"
	"testing"
)

func TestProjectMetadataValidation(t *testing.T) {
	name, description := "  My project  ", "  A useful workspace.\nDetails  "
	n, d, err := projectMetadata("my-project", &name, &description)
	if err != nil || n != "My project" || d != "A useful workspace.\nDetails" {
		t.Fatal(n, d, err)
	}
	for _, bad := range []string{"  ", strings.Repeat("x", 81), "name\nwith control"} {
		if _, _, err := projectMetadata("id", &bad, nil); err == nil {
			t.Fatal("invalid display name accepted")
		}
	}
	for _, bad := range []string{strings.Repeat("x", 1001), "text\x00"} {
		if _, _, err := projectMetadata("id", nil, &bad); err == nil {
			t.Fatal("invalid description accepted")
		}
	}
}
