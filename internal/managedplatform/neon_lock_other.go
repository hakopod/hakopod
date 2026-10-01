//go:build !linux && !darwin && !windows

package managedplatform

import (
	"fmt"
	"os"
)

func lockNeonStateFile(_ *os.File) (func(), error) {
	return nil, fmt.Errorf("Neon test state locking is unsupported on this operating system")
}
