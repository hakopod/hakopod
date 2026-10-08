//go:build linux

package sessionguard

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// networkFilter blocks IP sockets before the worker or helper executes. UNIX
// sockets remain available for communication with the session kernel.
func networkFilter() ([]unix.SockFilter, error) {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return nil, fmt.Errorf("session socket isolation requires amd64 or arm64 Linux")
	}
	load := func(offset uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
	}
	equal := func(value uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: value, Jt: yes, Jf: no}
	}
	result := func(value uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value} }
	return []unix.SockFilter{
		load(4),
		equal(arch, 1, 0),
		result(unix.SECCOMP_RET_KILL_PROCESS),
		load(0),
		// x32 uses the amd64 audit architecture with this syscall-number bit set.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1},
		result(unix.SECCOMP_RET_KILL_PROCESS),
		equal(uint32(unix.SYS_IO_URING_SETUP), 8, 0),
		equal(uint32(unix.SYS_IO_URING_ENTER), 7, 0),
		equal(uint32(unix.SYS_IO_URING_REGISTER), 6, 0),
		equal(uint32(unix.SYS_SOCKET), 2, 0),
		equal(uint32(unix.SYS_SOCKETPAIR), 1, 0),
		result(unix.SECCOMP_RET_ALLOW),
		load(16),
		equal(unix.AF_UNIX, 0, 1),
		result(unix.SECCOMP_RET_ALLOW),
		result(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
	}, nil
}

func restrictSockets() error {
	filter, err := networkFilter()
	if err != nil {
		return err
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	runtime.KeepAlive(filter)
	if errno != 0 {
		return fmt.Errorf("cannot enforce session socket isolation: %w", errno)
	}
	return nil
}
