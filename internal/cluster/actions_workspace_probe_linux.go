//go:build linux

package cluster

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/types"
)

type localActionsWorkspaceProbeHost struct {
	root        *os.File
	rootParts   []string
	runtimeLock *os.File
}

const actionsWorkspaceRuntimeLockName = "hakopod-actions-runtime.lock"

func newActionsWorkspaceProbeHost() (actionsWorkspaceProbeHost, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("workspace probe must run as root on the selected Linux node")
	}
	directory, err := actionsWorkspaceRuntimeLockDirectory()
	if err != nil {
		return nil, fmt.Errorf("Actions runtime lock directory is not safely owned")
	}
	defer directory.Close()
	lock, err := acquireActionsWorkspaceRuntimeLock(directory)
	if err != nil {
		return nil, err
	}
	return &localActionsWorkspaceProbeHost{rootParts: []string{"var", "lib", "kubelet", "pods"}, runtimeLock: lock}, nil
}

// The installer takes the same local lock before changing runtime artifacts,
// then releases it before invoking this command. Holding it through the whole
// probe and cleanup closes the gap between host changes and Kubernetes CAS.
func actionsWorkspaceRuntimeLockDirectory() (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"", "run", "lock"} {
		if part != "" {
			next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if openErr != nil {
				return nil, openErr
			}
			fd = next
		}
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || (stat.Mode&0022 != 0 && stat.Mode&unix.S_ISVTX == 0) {
			unix.Close(fd)
			return nil, fmt.Errorf("runtime lock parent is not safely root-owned")
		}
	}
	return os.NewFile(uintptr(fd), "Actions runtime lock directory"), nil
}

func acquireActionsWorkspaceRuntimeLock(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), actionsWorkspaceRuntimeLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("Actions runtime lock could not be opened safely")
	}
	file := os.NewFile(uintptr(fd), "Actions runtime lock")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !actionsWorkspaceRuntimeLockSafe(stat) {
		file.Close()
		return nil, fmt.Errorf("Actions runtime lock must be a root-owned single-link regular file with mode 0600")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("Actions runtime maintenance or another workspace probe is active; let it finish")
	}
	var current unix.Stat_t
	if unix.Fstatat(int(directory.Fd()), actionsWorkspaceRuntimeLockName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !actionsWorkspaceRuntimeLockSafe(current) || current.Dev != stat.Dev || current.Ino != stat.Ino {
		file.Close()
		return nil, fmt.Errorf("Actions runtime lock identity changed while it was acquired")
	}
	return file, nil
}

func actionsWorkspaceRuntimeLockSafe(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == 0 && stat.Nlink == 1 && stat.Mode&07777 == 0600
}

// Each component is opened relative to its already-open parent. Symlinks are
// refused throughout, including the fixed kubelet root and the final file.
func actionsWorkspaceProbeDirectory(parts []string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "/") {
			unix.Close(fd)
			return nil, fmt.Errorf("invalid workspace observation path")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "owned-workspace-directory"), nil
}

func (*localActionsWorkspaceProbeHost) MachineID() (string, error) {
	root, err := actionsWorkspaceProbeDirectory([]string{"etc"})
	if err != nil {
		return "", err
	}
	defer root.Close()
	fd, err := unix.Openat(int(root.Fd()), "machine-id", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), "machine-id")
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Mode&0022 != 0 || stat.Size > 65 {
		return "", fmt.Errorf("local machine identity is not a protected regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil || len(raw) > 65 {
		return "", fmt.Errorf("local machine identity exceeds its read bound")
	}
	id := strings.TrimSpace(string(raw))
	if !actionsWorkspaceProbeID.MatchString(id) {
		return "", fmt.Errorf("local machine identity is invalid")
	}
	return id, nil
}

func (h *localActionsWorkspaceProbeHost) RootIdentity() (uint64, uint64, error) {
	root, err := actionsWorkspaceProbeDirectory(h.rootParts)
	if err != nil {
		return 0, 0, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &stat); err != nil {
		root.Close()
		return 0, 0, err
	}
	if h.root == nil {
		h.root = root
	} else {
		root.Close()
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func (h *localActionsWorkspaceProbeHost) Close() error {
	var result error
	if h.root != nil {
		result = h.root.Close()
		h.root = nil
	}
	if h.runtimeLock != nil {
		// Closing the retained descriptor releases flock. Never unlink or
		// truncate the shared lock file, which would split the exclusion domain.
		result = errors.Join(result, h.runtimeLock.Close())
		h.runtimeLock = nil
	}
	return result
}

func (h *localActionsWorkspaceProbeHost) checkedRoot() (int, error) {
	if h.root == nil {
		return -1, fmt.Errorf("workspace observation root is not pinned")
	}
	device, inode, err := h.RootIdentity()
	if err != nil {
		return -1, err
	}
	var pinned unix.Stat_t
	if unix.Fstat(int(h.root.Fd()), &pinned) != nil || uint64(pinned.Dev) != device || pinned.Ino != inode {
		return -1, fmt.Errorf("workspace observation root changed")
	}
	return unix.FcntlInt(h.root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
}

func (h *localActionsWorkspaceProbeHost) volume(uid types.UID) (*os.File, error) {
	if !actionsWorkspaceProbeUID.MatchString(string(uid)) {
		return nil, fmt.Errorf("workspace observation requires the exact pod UID")
	}
	fd, err := h.checkedRoot()
	if err != nil {
		return nil, err
	}
	for _, part := range []string{string(uid), "volumes", "kubernetes.io~empty-dir", "runner"} {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "owned-workspace-volume"), nil
}

var actionsWorkspaceFilestoreName = regexp.MustCompile(`^\.gvisor\.filestore\.[0-9a-f]{64}$`)

func (h *localActionsWorkspaceProbeHost) Observe(uid types.UID) (actionsWorkspaceDiskSample, error) {
	var sample actionsWorkspaceDiskSample
	root, err := h.volume(uid)
	if err != nil {
		return sample, err
	}
	defer root.Close()
	entries, err := root.ReadDir(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return sample, err
	}
	if len(entries) != 1 || !actionsWorkspaceFilestoreName.MatchString(entries[0].Name()) {
		return sample, fmt.Errorf("workspace backing must be one exact runtime filestore")
	}
	fd, err := unix.Openat(int(root.Fd()), entries[0].Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return sample, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Fstat(fd, &stat) != nil || unix.Fstatfs(fd, &filesystem) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Ino == 0 || stat.Size <= 0 || stat.Size > 64<<30 || stat.Blocks <= 0 || stat.Blocks > stat.Size/512 {
		return sample, fmt.Errorf("workspace backing file identity or allocation is invalid")
	}
	// The sandbox presents internal tmpfs, but these physical host files must
	// reside on one of the disk filesystems qualified for the runtime profile.
	switch uint64(filesystem.Type) {
	case 0xef53, 0x58465342, 0x9123683e: // ext, XFS, Btrfs
	default:
		return sample, fmt.Errorf("workspace backing is not a supported disk filesystem")
	}
	return actionsWorkspaceDiskSample{Name: entries[0].Name(), Device: uint64(stat.Dev), Inode: stat.Ino, LogicalBytes: uint64(stat.Size), AllocatedBytes: uint64(stat.Blocks) * 512}, nil
}

func (h *localActionsWorkspaceProbeHost) Removed(uid types.UID) (bool, error) {
	if !actionsWorkspaceProbeUID.MatchString(string(uid)) {
		return false, fmt.Errorf("workspace cleanup requires the exact pod UID")
	}
	// The fixed kubelet root must remain present; losing the host mount is not
	// evidence that this particular workspace has been cleaned up.
	fd, err := h.checkedRoot()
	if err != nil {
		return false, err
	}
	for _, part := range []string{string(uid), "volumes", "kubernetes.io~empty-dir", "runner"} {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if errors.Is(openErr, unix.ENOENT) {
			return true, nil
		}
		if openErr != nil {
			return false, openErr
		}
		fd = next
	}
	unix.Close(fd)
	return false, nil
}
