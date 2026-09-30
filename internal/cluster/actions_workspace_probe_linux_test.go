//go:build linux

package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestActionsWorkspaceProbeHostRejectsSymlinkAncestors(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Join(base, "link"), "/"), "/")
	if directory, err := actionsWorkspaceProbeDirectory(parts); err == nil {
		directory.Close()
		t.Fatal("symlink ancestor accepted")
	}
}

func TestActionsWorkspaceProbeHostPinsDirectoryAcrossReplacement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "pods")
	volume := filepath.Join(root, string(probeTestPodUID), "volumes", "kubernetes.io~empty-dir", "runner")
	if err := os.MkdirAll(volume, 0700); err != nil {
		t.Fatal(err)
	}
	host := &localActionsWorkspaceProbeHost{rootParts: strings.Split(strings.TrimPrefix(root, "/"), "/")}
	defer host.Close()
	if _, _, err := host.RootIdentity(); err != nil {
		t.Fatal(err)
	}
	if removed, err := host.Removed(probeTestPodUID); err != nil || removed {
		t.Fatal("present workspace was reported removed", err)
	}
	if err := os.Rename(root, root+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if removed, err := host.Removed(probeTestPodUID); err == nil || removed {
		t.Fatal("empty replacement root was accepted as physical cleanup")
	}
	if _, err := os.Stat(filepath.Join(root+"-retained", string(probeTestPodUID), "volumes", "kubernetes.io~empty-dir", "runner")); err != nil {
		t.Fatal("observer mutated retained workspace", err)
	}
}

func TestActionsWorkspaceRuntimeLockMetadataIsStrict(t *testing.T) {
	valid := unix.Stat_t{Mode: unix.S_IFREG | 0600, Uid: 0, Nlink: 1}
	if !actionsWorkspaceRuntimeLockSafe(valid) {
		t.Fatal("safe runtime lock rejected")
	}
	for name, change := range map[string]func(*unix.Stat_t){
		"wrong owner":    func(s *unix.Stat_t) { s.Uid = 1001 },
		"hard link":      func(s *unix.Stat_t) { s.Nlink = 2 },
		"group writable": func(s *unix.Stat_t) { s.Mode = unix.S_IFREG | 0660 },
		"world readable": func(s *unix.Stat_t) { s.Mode = unix.S_IFREG | 0644 },
		"setuid":         func(s *unix.Stat_t) { s.Mode = unix.S_IFREG | 04600 },
		"symlink":        func(s *unix.Stat_t) { s.Mode = unix.S_IFLNK | 0600 },
		"directory":      func(s *unix.Stat_t) { s.Mode = unix.S_IFDIR | 0600 },
	} {
		t.Run(name, func(t *testing.T) {
			stat := valid
			change(&stat)
			if actionsWorkspaceRuntimeLockSafe(stat) {
				t.Fatal("unsafe runtime lock accepted")
			}
		})
	}
}

func TestActionsWorkspaceRuntimeLockExcludesConcurrentMaintenanceUntilClose(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned runtime lock integration requires the Linux operator user")
	}
	base := t.TempDir()
	directory, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	lockPath := filepath.Join(base, actionsWorkspaceRuntimeLockName)
	if err := os.WriteFile(lockPath, []byte("preserve existing lock contents"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := acquireActionsWorkspaceRuntimeLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	host := &localActionsWorkspaceProbeHost{runtimeLock: first}
	defer host.Close()
	if second, err := acquireActionsWorkspaceRuntimeLock(directory); err == nil {
		second.Close()
		t.Fatal("concurrent runtime maintenance lock was accepted")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireActionsWorkspaceRuntimeLock(directory)
	if err != nil {
		t.Fatal("runtime lock was not released after cleanup", err)
	}
	second.Close()
	contents, err := os.ReadFile(lockPath)
	if err != nil || string(contents) != "preserve existing lock contents" {
		t.Fatal("runtime lock was removed or truncated", err)
	}
}

func TestActionsWorkspaceRuntimeLockRefusesFilesystemIndirection(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned runtime lock integration requires the Linux operator user")
	}
	for _, kind := range []string{"symlink", "hardlink", "mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			directory, err := os.Open(base)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			path := filepath.Join(base, actionsWorkspaceRuntimeLockName)
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(base, "retained")
				if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = os.Symlink(target, path)
				} else {
					err = os.Link(target, path)
				}
			case "mode":
				err = os.WriteFile(path, nil, 0644)
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if lock, err := acquireActionsWorkspaceRuntimeLock(directory); err == nil {
				lock.Close()
				t.Fatal("unsafe runtime lock entry accepted")
			}
		})
	}
}
