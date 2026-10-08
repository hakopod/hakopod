//go:build linux

package sessionguard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSessionGuardSocketIsolation(t *testing.T) {
	if os.Getenv("HAKOPOD_GUARD_NETWORK_CHILD") == "1" {
		runtime.LockOSThread()
		if err := applyLimits(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := restrictSockets(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			os.Exit(4)
		}
		unix.Close(fd)
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			os.Exit(5)
		}
		unix.Close(pair[0])
		unix.Close(pair[1])
		for _, family := range []int{unix.AF_INET, unix.AF_INET6, unix.AF_PACKET} {
			fd, err = unix.Socket(family, unix.SOCK_STREAM, 0)
			if err != unix.EPERM {
				if fd >= 0 {
					unix.Close(fd)
				}
				os.Exit(6)
			}
			_, err = unix.Socketpair(family, unix.SOCK_STREAM, 0)
			if err != unix.EPERM {
				os.Exit(7)
			}
		}
		for _, number := range []uintptr{unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER} {
			_, _, errno := unix.Syscall(number, 0, 0, 0)
			if errno != unix.EPERM {
				os.Exit(8)
			}
		}
		fmt.Print("UNIX sockets allowed; IP sockets and io_uring denied")
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		directory, err := os.MkdirTemp("/tmp", "hakopod-session-network-test-")
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
	command := exec.Command(executable, "-test.run=^TestSessionGuardSocketIsolation$")
	command.Env = append(os.Environ(), "HAKOPOD_GUARD_NETWORK_CHILD=1")
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	}
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("UNIX sockets allowed; IP sockets and io_uring denied")) {
		t.Fatal("socket isolation failed", err, string(output))
	}
}

// Evaluate every filter branch without executing a forbidden compatibility call.
func TestSessionGuardSocketFilterBranches(t *testing.T) {
	filter, err := networkFilter()
	if err != nil {
		t.Fatal(err)
	}
	arch := uint32(unix.AUDIT_ARCH_X86_64)
	if runtime.GOARCH == "arm64" {
		arch = unix.AUDIT_ARCH_AARCH64
	}
	evaluate := func(a, n, f uint32) uint32 {
		var accumulator uint32
		for pc := 0; pc < len(filter); pc++ {
			instruction := filter[pc]
			switch instruction.Code {
			case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
				switch instruction.K {
				case 4:
					accumulator = a
				case 0:
					accumulator = n
				case 16:
					accumulator = f
				default:
					t.Fatal("unexpected load")
				}
			case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
				if accumulator == instruction.K {
					pc += int(instruction.Jt)
				} else {
					pc += int(instruction.Jf)
				}
			case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
				if accumulator&instruction.K != 0 {
					pc += int(instruction.Jt)
				} else {
					pc += int(instruction.Jf)
				}
			case unix.BPF_RET | unix.BPF_K:
				return instruction.K
			default:
				t.Fatal("unexpected instruction")
			}
		}
		t.Fatal("filter fell through")
		return 0
	}
	for _, tc := range []struct{ arch, number, family, want uint32 }{
		{0, uint32(unix.SYS_SOCKET), unix.AF_UNIX, unix.SECCOMP_RET_KILL_PROCESS},
		{arch, 0x40000000 | uint32(unix.SYS_SOCKET), unix.AF_UNIX, unix.SECCOMP_RET_KILL_PROCESS},
		{arch, uint32(unix.SYS_SOCKET), unix.AF_UNIX, unix.SECCOMP_RET_ALLOW},
		{arch, uint32(unix.SYS_SOCKETPAIR), unix.AF_UNIX, unix.SECCOMP_RET_ALLOW},
		{arch, uint32(unix.SYS_SOCKET), unix.AF_INET, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{arch, uint32(unix.SYS_SOCKETPAIR), unix.AF_INET6, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{arch, uint32(unix.SYS_IO_URING_SETUP), 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{arch, uint32(unix.SYS_IO_URING_ENTER), 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{arch, uint32(unix.SYS_IO_URING_REGISTER), 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{arch, uint32(unix.SYS_READ), 0, unix.SECCOMP_RET_ALLOW},
	} {
		if got := evaluate(tc.arch, tc.number, tc.family); got != tc.want {
			t.Fatalf("filter result %x; expected %x", got, tc.want)
		}
	}
}
