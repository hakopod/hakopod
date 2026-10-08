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

func TestSessionGuardConfirmsIdentityBeforeExec(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if mode := os.Getenv("HAKOPOD_GUARD_READY_CHILD"); mode != "" {
		args := []string{"run", "--expected-pod-uid", "pod-one", "--expected-generation", "generation-one", "--ready-token", token, "--", "/bin/echo", "fixed-helper-executed"}
		if mode == "changed" {
			args[2] = "replaced-pod"
		}
		if err := Run(args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(3) // A successful exec does not return.
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		directory, err := os.MkdirTemp("/tmp", "hakopod-session-ready-test-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(directory)
		if err = os.Chmod(directory, 0755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		executable = filepath.Join(directory, "probe")
		if err = os.WriteFile(executable, data, 0555); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"matching", "changed"} {
		command := exec.Command(executable, "-test.run=^TestSessionGuardConfirmsIdentityBeforeExec$")
		command.Env = append(os.Environ(), "HAKOPOD_GUARD_READY_CHILD="+mode, "HAKOPOD_POD_UID=pod-one", "HAKOPOD_SESSION_GENERATION=generation-one")
		if os.Geteuid() == 0 {
			command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
		}
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if mode == "matching" {
			if err != nil || stdout.String() != "HAKOPOD_SESSION_READY "+token+"\nfixed-helper-executed\n" {
				t.Fatal("guard did not confirm identity before helper output", err, stdout.String(), stderr.String())
			}
		} else if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "identity or generation changed") {
			t.Fatal("changed Pod received readiness or helper execution", err, stdout.String(), stderr.String())
		}
	}
}
