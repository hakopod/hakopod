package store

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// Check the entire embedded set before opening a transaction. Duplicate
// versions otherwise look already applied and silently skip required schema.
func validateMigrationVersions(entries []fs.DirEntry) error {
	versions := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil || version < 1 {
			return fmt.Errorf("invalid migration filename %s", entry.Name())
		}
		if previous, exists := versions[version]; exists {
			return fmt.Errorf("duplicate migration version %d: %s and %s", version, previous, entry.Name())
		}
		versions[version] = entry.Name()
	}
	return nil
}
