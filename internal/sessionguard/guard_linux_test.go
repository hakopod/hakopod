//go:build linux

package sessionguard

import (
	"bytes"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSessionGuardHardLimits(t *testing.T) {
	if os.Getenv("HAKOPOD_GUARD_CHILD") == "1" {
		if unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 32, Max: 256}) != nil {
			os.Exit(5)
		}
		if err := applyLimits(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		for _, bound := range []struct {
			resource int
			maximum  uint64
		}{{unix.RLIMIT_NPROC, MaxProcesses}, {unix.RLIMIT_NOFILE, MaxOpenFiles}, {unix.RLIMIT_CORE, 0}} {
			var limit unix.Rlimit
			if unix.Getrlimit(bound.resource, &limit) != nil || limit.Max > bound.maximum || limit.Cur > bound.maximum {
				os.Exit(3)
			}
			if unix.Setrlimit(bound.resource, &unix.Rlimit{Cur: bound.maximum + 1, Max: bound.maximum + 1}) == nil {
				os.Exit(4)
			}
		}
		var inherited unix.Rlimit
		if unix.Getrlimit(unix.RLIMIT_NOFILE, &inherited) != nil || inherited.Cur != 32 {
			os.Exit(6)
		}
		fmt.Print("hard limits cannot be raised")
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var cleanup func()
	if os.Geteuid() == 0 {
		directory, err := os.MkdirTemp("/tmp", "hakopod-session-guard-test-")
		if err != nil {
			t.Fatal(err)
		}
		cleanup = func() { os.RemoveAll(directory) }
		defer cleanup()
		if err = os.Chmod(directory, 0755); err != nil {
			t.Fatal(err)
		}
		bytes, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		executable = filepath.Join(directory, "probe")
		if err = os.WriteFile(executable, bytes, 0555); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(executable, "-test.run=^TestSessionGuardHardLimits$")
	command.Env = append(os.Environ(), "HAKOPOD_GUARD_CHILD=1")
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	}
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("hard limits cannot be raised")) {
		t.Fatal("hard limit enforcement failed", err, string(output))
	}
}
func TestSessionGuardRejectsUnsupportedArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"run", "--"}, {"run", "bad", "worker"}} {
		if err := Run(args); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	if os.Geteuid() == 0 {
		if err := applyLimits(); err == nil || !strings.Contains(err.Error(), "nonroot") {
			t.Fatal("root worker accepted", err)
		}
	}
}
