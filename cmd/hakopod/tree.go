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
	Path     string            // config file, or the folder itself when Merged
	Data     []byte            // raw TOML bytes; nil when Merged
	Spec     spec.Application  // validateLocalConfiguration, or the merged and normalized folder
	EnvFiles map[string]string // from validateLocalConfiguration; nil when Merged
	Merged   bool              // several .toml files merged into one application; submitted as JSON spec
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

// findTOMLFiles returns every regular *.toml file directly inside dir, sorted by path so
// that merge errors are deterministic. Dotfiles and subdirectories are skipped; symlinks
// are followed like findNamed does.
func findTOMLFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// loadFolder reads one application folder. Exactly one hakopod.toml keeps the legacy TOML
// path (env_file support, name from the document); anything else is merged into one
// application named after the folder and submitted as a JSON spec.
func loadFolder(name, dir string) (treeApp, error) {
	// Two case variants of hakopod.toml are ambiguous, not two services.
	if _, err := findConfigFile(dir); err != nil {
		return treeApp{}, err
	}
	paths, err := findTOMLFiles(dir)
	if err != nil || len(paths) == 0 {
		return treeApp{}, err
	}
	app := treeApp{Dir: name, Path: paths[0]}
	if len(paths) == 1 && strings.EqualFold(filepath.Base(paths[0]), "hakopod.toml") {
		if app.Data, err = os.ReadFile(app.Path); err != nil {
			return treeApp{}, err
		}
		app.Spec, app.EnvFiles, err = validateLocalConfiguration(app.Path, app.Data)
		return app, err
	}
	files := make([]mergeFile, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return treeApp{}, err
		}
		files = append(files, mergeFile{Path: path, Data: data})
	}
	merged, err := mergeFolder(name, files)
	if err != nil {
		return treeApp{}, err
	}
	app.Merged, app.Path = true, dir
	app.Spec, err = spec.Normalize(merged)
	return app, err
}

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
		app, err := loadFolder(e.Name(), dir)
		if err != nil {
			at := app.Path
			if at == "" {
				at = dir
			}
			fail(at, err)
			continue
		}
		if app.Path == "" { // no *.toml directly inside: not an application folder
			continue
		}
		path := app.Path
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
