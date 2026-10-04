package managedplatform

import (
	"strings"
	"testing"
)

func TestSupabaseReleaseImageInventory(t *testing.T) {
	images := map[string]string{}
	for name, image := range supabaseReleaseImages {
		images[name] = image
	}
	original := SupabaseImageInventorySHA256(images)
	if original != SupabaseReleaseImageInventorySHA256 || !SupabaseReleaseImagesMatch(images) {
		t.Fatal("compiled Supabase release inventory digest changed")
	}
	images[SupabaseComponentNames()[0]] = "registry.example.test/changed@sha256:" + strings.Repeat("b", 64)
	if SupabaseImageInventorySHA256(images) == original || SupabaseReleaseImagesMatch(images) {
		t.Fatal("changed Supabase image inventory retained its binding digest")
	}
}
