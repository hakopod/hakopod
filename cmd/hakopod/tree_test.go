package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const treeNetwork = "name = \"shared\"\n[segments.telemetry]\napplications = [\"alpha\", \"beta\"]\n"

func treeAppTOML(name, segment string) string {
	s := "name = '" + name + "'\n[services.web]\nimage = 'nginx'\n"
	if segment != "" {
		s += "[networks.t]\ninternal = true\nvirtual_network = 'shared'\nsegment = '" + segment + "'\n"
	}
	return s
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadTreeHappyPath(t *testing.T) {
	root := writeTree(t, map[string]string{
		"Network.TOML":             treeNetwork,
		"b/HAKOPOD.toml":           treeAppTOML("beta", "telemetry"),
		"a/hakopod.toml":           treeAppTOML("alpha", "telemetry"),
		"empty/readme.txt":         "no config",
		".hidden/hakopod.toml":     "broken",
		"a/deeper/c/hakopod.toml":  treeAppTOML("gamma", ""),
		"nested/deep/hakopod.toml": treeAppTOML("delta", ""),
	})
	tree, err := loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Network == nil || tree.Network.Name != "shared" || len(tree.Apps) != 2 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	if tree.Apps[0].Dir != "a" || tree.Apps[0].Spec.Name != "alpha" || tree.Apps[1].Dir != "b" || !strings.HasSuffix(tree.Apps[1].Path, "HAKOPOD.toml") {
		t.Fatalf("apps not sorted or wrong: %+v", tree.Apps)
	}
}

func TestLoadTreeMergesFolders(t *testing.T) {
	root := writeTree(t, map[string]string{
		"three/web.toml":     "image = 'nginx'\n",
		"three/worker.toml":  "image = 'busybox'\n",
		"three/cron.toml":    "image = 'alpine'\n",
		"mixed/api.toml":     "image = 'nginx'\n",
		"mixed/hakopod.toml": "[services.worker]\nimage = 'busybox'\n[services.cron]\nimage = 'alpine'\n",
		"lone/api.toml":      "image = 'nginx'\n",
		"legacy/hakopod.toml": "name = 'renamed'\nenv_file = '.env'\n" +
			"[services.web]\nimage = 'nginx'\n",
		"legacy/.env": "TOKEN=abc\n",
	})
	tree, err := loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]treeApp{}
	for _, app := range tree.Apps {
		byDir[app.Dir] = app
	}
	if len(byDir) != 4 {
		t.Fatalf("expected 4 applications, got %+v", tree.Apps)
	}
	for dir, want := range map[string][]string{
		"three": {"web", "worker", "cron"},
		"mixed": {"api", "worker", "cron"},
		"lone":  {"api"},
	} {
		app := byDir[dir]
		if !app.Merged || app.Spec.Name != dir || app.Data != nil || app.EnvFiles != nil {
			t.Fatalf("%s: expected a merged folder named after itself, got %+v", dir, app)
		}
		if len(app.Spec.Services) != len(want) {
			t.Fatalf("%s: services %v, want %v", dir, app.Spec.Services, want)
		}
		for _, name := range want {
			if _, ok := app.Spec.Services[name]; !ok {
				t.Fatalf("%s: missing service %s in %v", dir, name, app.Spec.Services)
			}
		}
	}
	// A single hakopod.toml keeps the legacy path: name from the document, env files read.
	legacy := byDir["legacy"]
	if legacy.Merged || legacy.Spec.Name != "renamed" || len(legacy.Data) == 0 || legacy.EnvFiles[".env"] != "TOKEN=abc\n" {
		t.Fatalf("legacy folder changed: %+v", legacy)
	}
}

func TestLoadTreeReportsMergeConflictsWithOtherErrors(t *testing.T) {
	root := writeTree(t, map[string]string{
		"clash/a.toml":      "name = 'clash'\n[services.web]\nimage = 'nginx'\n",
		"clash/b.toml":      "name = 'clash'\n[services.other]\nimage = 'nginx'\n",
		"broken/api.toml":   "this is not toml",
		"fine/hakopod.toml": treeAppTOML("fine", ""),
	})
	_, err := loadTree(root, nil)
	for _, want := range []string{"clash", "a.toml", "b.toml", "both set name", "broken", "api.toml"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v does not mention %s", err, want)
		}
	}
}

func TestLoadTreeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		only  []string
		want  []string
	}{
		"ungranted segment": {map[string]string{"network.toml": treeNetwork, "g/hakopod.toml": treeAppTOML("gamma", "telemetry")},
			nil, []string{"g/hakopod.toml", `"gamma"`, `"telemetry"`, `"shared"`}},
		"duplicate name": {map[string]string{"a/hakopod.toml": treeAppTOML("alpha", ""), "b/hakopod.toml": treeAppTOML("alpha", "")},
			nil, []string{"b/hakopod.toml", "also used by a/"}},
		"no apps":      {map[string]string{"x/readme": ""}, nil, []string{"no application folders"}},
		"unknown only": {map[string]string{"a/hakopod.toml": treeAppTOML("alpha", "")}, []string{"alpha", "zeta"}, []string{"--only zeta"}},
		"collects all": {map[string]string{"network.toml": "bad", "a/hakopod.toml": "bad"}, nil, []string{"network.toml:", "a/hakopod.toml:"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTree(writeTree(t, tc.files), tc.only)
			for _, want := range tc.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error %v does not mention %s", err, want)
				}
			}
		})
	}
}

func TestLoadTreeOnly(t *testing.T) {
	root := writeTree(t, map[string]string{"a/hakopod.toml": treeAppTOML("alpha", ""), "folder-b/hakopod.toml": treeAppTOML("beta", "")})
	for _, only := range [][]string{{"a"}, {"alpha"}} {
		tree, err := loadTree(root, only)
		if err != nil || len(tree.Apps) != 1 || tree.Apps[0].Dir != "a" {
			t.Fatalf("only %v: %v %+v", only, err, tree.Apps)
		}
	}
}

func TestFindConfigFileAmbiguous(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"hakopod.toml", "HAKOPOD.toml"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 {
		t.Skip("case-insensitive filesystem cannot hold both names")
	}
	if _, err := findConfigFile(dir); err == nil {
		t.Fatal("two config files accepted")
	}
	// Case variants are ambiguous, never two files to merge.
	if _, err := loadFolder("app", dir); err == nil {
		t.Fatal("two config files merged instead of rejected")
	}
}

func TestLoadTreeRealDevops(t *testing.T) {
	root := "/Users/theboringhumane/Projects/synehq/syne-stack/devops/hakopod"
	if _, err := os.Stat(root); err != nil {
		t.Skip("devops tree absent")
	}
	tree, err := loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Network == nil || len(tree.Apps) < 4 {
		t.Fatalf("network %v, %d apps", tree.Network, len(tree.Apps))
	}
	for _, app := range tree.Apps {
		t.Logf("network=%s app dir=%s name=%s", tree.Network.Name, app.Dir, app.Spec.Name)
	}
}

func TestLoadTreeFollowsSymlinks(t *testing.T) {
	shared := writeTree(t, map[string]string{
		"network.toml":       "name = \"shared\"\n[segments.telemetry]\napplications = [\"alpha\"]\n",
		"gamma/hakopod.toml": treeAppTOML("gamma", ""),
	})
	root := writeTree(t, map[string]string{
		"a/hakopod.toml": treeAppTOML("alpha", "telemetry"),
		"b/hakopod.toml": treeAppTOML("beta", "telemetry"),
	})
	for link, target := range map[string]string{"network.toml": "network.toml", "gamma": "gamma"} {
		if err := os.Symlink(filepath.Join(shared, target), filepath.Join(root, link)); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
	}
	// A symlinked network must still gate grants: beta is not granted telemetry.
	_, err := loadTree(root, nil)
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("symlinked network.toml was ignored: %v", err)
	}
	tree, err := loadTree(root, []string{"alpha", "gamma"})
	if err == nil {
		t.Fatalf("beta's missing grant must fail the whole tree, got %+v", tree)
	}
	os.Remove(filepath.Join(root, "b", "hakopod.toml"))
	tree, err = loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Network == nil || len(tree.Apps) != 2 || tree.Apps[1].Dir != "gamma" {
		t.Fatalf("symlinked folder or network not loaded: network=%v apps=%+v", tree.Network, tree.Apps)
	}
}
