package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
)

type treeApp struct {
	Dir      string            // folder name, e.g. "syne"
	Path     string            // full path to the config file
	Data     []byte            // raw TOML bytes
	Spec     spec.Application  // from validateLocalConfiguration
	EnvFiles map[string]string // from validateLocalConfiguration
}

type deployTree struct {
	Root        string
	NetworkPath string // "" if no network.toml
	NetworkData []byte
	Network     *spec.VirtualNetwork // nil if no network.toml
	Apps        []treeApp            // sorted by Dir ascending
}

// findNamed returns the single regular file in dir named name case-insensitively.
// Symlinks are followed, so a shared network.toml linked into the tree still counts.
func findNamed(dir, name string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	found := ""
	for _, e := range entries {
		if !strings.EqualFold(e.Name(), name) {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err != nil {
			return "", err
		} else if info.Mode().IsRegular() {
			if found != "" {
				return "", fmt.Errorf("both %s and %s exist; keep only one", found, e.Name())
			}
			found = e.Name()
		}
	}
	if found == "" {
		return "", nil
	}
	return filepath.Join(dir, found), nil
}

func findConfigFile(dir string) (string, error) { return findNamed(dir, "hakopod.toml") }

func loadTree(root string, only []string) (deployTree, error) {
	tree := deployTree{Root: root}
	var errs []error
	fail := func(path string, err error) {
		rel, _ := filepath.Rel(root, path)
		errs = append(errs, fmt.Errorf("%s: %w", filepath.ToSlash(rel), err))
	}

	netPath, err := findNamed(root, "network.toml")
	if err != nil {
		fail(filepath.Join(root, "network.toml"), err)
	}
	if netPath != "" {
		tree.NetworkPath = netPath
		if tree.NetworkData, err = os.ReadFile(netPath); err != nil {
			fail(netPath, err)
		} else if network, err := spec.ParseVirtualNetwork(tree.NetworkData); err != nil {
			fail(netPath, err)
		} else {
			tree.Network = &network
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return tree, err
	}
	names := map[string]string{} // Spec.Name -> Dir
	for _, e := range entries {  // ReadDir is sorted, so Apps are sorted by Dir
		dir := filepath.Join(root, e.Name())
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() { // follows symlinked folders
			continue
		}
		path, err := findConfigFile(dir)
		if err != nil {
			fail(dir, err)
			continue
		}
		if path == "" {
			continue
		}
		app := treeApp{Dir: e.Name(), Path: path}
		if app.Data, err = os.ReadFile(path); err != nil {
			fail(path, err)
			continue
		}
		if app.Spec, app.EnvFiles, err = validateLocalConfiguration(path, app.Data); err != nil {
			fail(path, err)
			continue
		}
		if other, ok := names[app.Spec.Name]; ok {
			fail(path, fmt.Errorf("application name %q is also used by %s/", app.Spec.Name, other))
			continue
		}
		names[app.Spec.Name] = app.Dir
		if tree.Network != nil {
			keys := make([]string, 0, len(app.Spec.Networks))
			for key := range app.Spec.Networks {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				n := app.Spec.Networks[key]
				if n.VirtualNetwork == tree.Network.Name && !tree.Network.Allows(n.Segment, app.Spec.Name) {
					fail(path, fmt.Errorf("networks.%s: application %q is not granted segment %q of virtual network %q in network.toml", key, app.Spec.Name, n.Segment, tree.Network.Name))
				}
			}
		}
		tree.Apps = append(tree.Apps, app)
	}
	if len(tree.Apps) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("no application folders under %s", root))
	}

	if len(only) > 0 {
		kept := tree.Apps[:0]
		matched := map[string]bool{}
		for _, app := range tree.Apps {
			if slices.Contains(only, app.Dir) || slices.Contains(only, app.Spec.Name) {
				kept = append(kept, app)
				matched[app.Dir], matched[app.Spec.Name] = true, true
			}
		}
		tree.Apps = kept
		for _, name := range only {
			if !matched[name] {
				errs = append(errs, fmt.Errorf("--only %s: no application folder or name matches", name))
			}
		}
	}
	return tree, errors.Join(errs...)
}
