package managedplatform

import (
	"embed"
	"fmt"
)

//go:embed all:supabase-assets
var pinnedSupabaseAssetFS embed.FS

// PinnedSupabaseAssets returns a fresh copy of the exact audited upstream
// volume files. RenderSupabase verifies every file hash before using it.
func PinnedSupabaseAssets() (map[string]string, error) {
	assets := make(map[string]string, len(supabaseAssetNames))
	for _, name := range supabaseAssetNames {
		body, err := pinnedSupabaseAssetFS.ReadFile("supabase-assets/" + name)
		if err != nil {
			return nil, fmt.Errorf("read pinned Supabase asset %s: %w", name, err)
		}
		assets[name] = string(body)
	}
	return assets, nil
}
