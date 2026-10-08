//go:build linux

package sessionguard

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"runtime"
)

func applyLimits() error {
	if os.Geteuid() == 0 || os.Getuid() == 0 {
		return fmt.Errorf("worker and helper must run as a nonroot user")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("cannot disable privilege escalation")
	}
	for _, bound := range []struct {
		resource int
		maximum  uint64
	}{{unix.RLIMIT_NPROC, MaxProcesses}, {unix.RLIMIT_NOFILE, MaxOpenFiles}, {unix.RLIMIT_CORE, 0}} {
		var current unix.Rlimit
		if unix.Getrlimit(bound.resource, &current) != nil {
			return fmt.Errorf("cannot inspect a required process limit")
		}
		maximum := min(current.Max, bound.maximum)
		if unix.Setrlimit(bound.resource, &unix.Rlimit{Cur: min(current.Cur, maximum), Max: maximum}) != nil {
			return fmt.Errorf("cannot enforce a required hard process limit")
		}
	}
	return nil
}
func execute(args []string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := applyLimits(); err != nil {
		return err
	}
	executable, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("fixed worker executable is unavailable")
	}
	if err = unix.Exec(executable, args, os.Environ()); err != nil {
		return fmt.Errorf("cannot start the fixed worker executable")
	}
	return nil
}
