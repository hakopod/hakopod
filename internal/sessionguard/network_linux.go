//go:build linux

package sessionguard

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// networkFilter blocks IP sockets before the worker or helper executes. UNIX
// sockets remain available for communication with the session kernel. With
// allowIP, AF_INET and AF_INET6 are also allowed; Kubernetes network policy
// then limits where they can reach.
func networkFilter(allowIP bool) ([]unix.SockFilter, error) {
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
	// Layout: arch/x32 checks, syscall checks, ALLOW, family load, family
	// checks, ALLOW, ERRNO. Every jump is computed from its own index to the
	// shared ALLOW or ERRNO, so adding a family never retargets a syscall jump.
	families := []uint32{unix.AF_UNIX}
	if allowIP {
		families = append(families, unix.AF_INET, unix.AF_INET6)
	}
	uring := []uint32{uint32(unix.SYS_IO_URING_SETUP), uint32(unix.SYS_IO_URING_ENTER), uint32(unix.SYS_IO_URING_REGISTER)}
	sockets := []uint32{uint32(unix.SYS_SOCKET), uint32(unix.SYS_SOCKETPAIR)}
	const head = 6
	familyLoad := head + len(uring) + len(sockets) + 1
	allow := familyLoad + 1 + len(families)
	deny := allow + 1
	to := func(from, target int) uint8 { return uint8(target - from - 1) }
	filter := []unix.SockFilter{
		load(4),
		equal(arch, 1, 0),
		result(unix.SECCOMP_RET_KILL_PROCESS),
		load(0),
		// x32 uses the amd64 audit architecture with this syscall-number bit set.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1},
		result(unix.SECCOMP_RET_KILL_PROCESS),
	}
	for _, number := range uring {
		filter = append(filter, equal(number, to(len(filter), deny), 0))
	}
	for _, number := range sockets {
		filter = append(filter, equal(number, to(len(filter), familyLoad), 0))
	}
	filter = append(filter, result(unix.SECCOMP_RET_ALLOW), load(16))
	for i, family := range families {
		if i == len(families)-1 {
			filter = append(filter, equal(family, to(len(filter), allow), to(len(filter), deny)))
		} else {
			filter = append(filter, equal(family, to(len(filter), allow), 0))
		}
	}
	return append(filter, result(unix.SECCOMP_RET_ALLOW), result(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM))), nil
}

func restrictSockets(allowIP bool) error {
	filter, err := networkFilter(allowIP)
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
