//go:build hakopod_native_acceptance

package cluster

import (
	"fmt"
	"os"
	"path/filepath"
)

// supabaseRotationAcceptanceBoundary injects one retry only in the explicitly
// tagged native acceptance binary, after SQL commits and before the durable
// completion marker is written.
func supabaseRotationAcceptanceBoundary() error {
	path := os.Getenv("HAKOPOD_NATIVE_SUPABASE_ROTATION_FAIL_ONCE_FILE")
	release := os.Getenv("HAKOPOD_NATIVE_SUPABASE_ROTATION_RELEASE_FILE")
	if path == "" && release == "" {
		return nil
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(release) {
		return fmt.Errorf("native Supabase rotation failpoint paths must be absolute")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		if info, releaseErr := os.Lstat(release); releaseErr == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 {
			return nil
		}
		return fmt.Errorf("native acceptance is holding the Supabase rotation completion marker for restart")
	}
	if err != nil {
		return fmt.Errorf("arm native Supabase rotation retry: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("arm native Supabase rotation retry: %w", closeErr)
	}
	return fmt.Errorf("native acceptance injected a retry after Supabase database credential rotation")
}
