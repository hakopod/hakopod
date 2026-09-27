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
		"three/web.toml":    "image = 'nginx'\n",
		"three/worker.toml": "image = 'busybox'\n",
		"three/cron.toml":   "image = 'alpine'\n",
		// hakopod.toml wins outright: api.toml beside it is ignored, not merged.
		"mixed/api.toml":     "image = 'nginx'\n",
		"mixed/hakopod.toml": "name = 'mixed'\n[services.worker]\nimage = 'busybox'\n[services.cron]\nimage = 'alpine'\n",
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
	// A hakopod.toml keeps the legacy path: name from the document, env files read.
	legacy := byDir["legacy"]
	if legacy.Merged || legacy.Spec.Name != "renamed" || len(legacy.Data) == 0 || legacy.EnvFiles[".env"] != "TOKEN=abc\n" {
		t.Fatalf("legacy folder changed: %+v", legacy)
	}
	// ...and it is not merged with the file beside it.
	if mixed := byDir["mixed"]; mixed.Merged || len(mixed.Spec.Services) != 2 || mixed.Spec.Services["api"].Image != "" {
		t.Fatalf("api.toml was merged into mixed/hakopod.toml: %+v", mixed)
	}
}

// Merged mode is opt-in by the ABSENCE of hakopod.toml. A folder that has one never enters
// it, whatever else is dropped beside it.
func TestLoadFolderHakopodTOMLWins(t *testing.T) {
	root := writeTree(t, map[string]string{
		// A backup copy kept beside the live file must not contribute services.
		"backup/hakopod.toml":     "name = 'backup'\n[services.web]\nimage = 'nginx'\n",
		"backup/hakopod.old.toml": "[services.stale]\nimage = 'busybox'\n",
		// Merged mode would force the application name to the folder name; legacy must not.
		"web/hakopod.toml": treeAppTOML("frontend", ""),
		"web/extra.toml":   "[services.stale]\nimage = 'busybox'\n",
		// A per-application copy of a virtual network document is documentation, ignored.
		"docs/hakopod.toml": treeAppTOML("docs", ""),
		"docs/network.toml": treeNetwork,
	})
	tree, err := loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]treeApp{}
	for _, app := range tree.Apps {
		byDir[app.Dir] = app
	}
	if len(byDir) != 3 {
		t.Fatalf("expected 3 applications, got %+v", tree.Apps)
	}
	for dir, wantName := range map[string]string{"backup": "backup", "web": "frontend", "docs": "docs"} {
		app := byDir[dir]
		if app.Merged || app.Spec.Name != wantName || len(app.Data) == 0 || !strings.EqualFold(filepath.Base(app.Path), "hakopod.toml") {
			t.Fatalf("%s: expected the legacy path named %q, got %+v", dir, wantName, app)
		}
		// The regression that matters: nothing from the sibling file reached the spec.
		if _, ok := app.Spec.Services["stale"]; ok {
			t.Fatalf("%s: a sibling .toml was merged into the live application: %v", dir, app.Spec.Services)
		}
		if len(app.Spec.Services) != 1 {
			t.Fatalf("%s: services %v, want exactly one from hakopod.toml", dir, app.Spec.Services)
		}
	}
}

func TestLoadFolderMergedNeedsLegalFolderName(t *testing.T) {
	root := writeTree(t, map[string]string{
		"ok/web.toml":     "image = 'nginx'\n",
		"ok/api.toml":     "image = 'nginx'\n",
		"my_app/web.toml": "image = 'nginx'\n",
	})
	_, err := loadTree(root, nil)
	if err == nil || !strings.Contains(err.Error(), "my_app") || !strings.Contains(err.Error(), "becomes the application name") {
		t.Fatalf("illegal merged folder name must name the folder, got %v", err)
	}
	tree, err := loadTree(root, []string{"ok"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Apps) != 1 || !tree.Apps[0].Merged || tree.Apps[0].Spec.Name != "ok" || len(tree.Apps[0].Spec.Services) != 2 {
		t.Fatalf("merged folder wrong: %+v", tree.Apps)
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
	if err == nil {
		t.Fatal("conflicting folder accepted")
	}
	// Both problems are reported, each prefixed with the folder, and merge messages name
	// the bare filenames: an absolute path must never reach the message.
	for _, want := range []string{"clash: a.toml and b.toml both set name", "broken: api.toml: "} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "/a.toml") {
		t.Fatalf("absolute path leaked into %v", err)
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

// A mixed repository has folders that are not applications but do hold a .toml. Without
// --only every one of them must fail loudly; --only is the escape hatch.
func TestLoadTreeOnlyRescuesUnrelatedFolder(t *testing.T) {
	files := map[string]string{
		"good/hakopod.toml": treeAppTOML("good", ""),
		"mylib/Cargo.toml":  "[package]\nname = \"mylib\"\n",
	}
	root := writeTree(t, files)

	_, err := loadTree(root, nil)
	if err == nil || !strings.Contains(err.Error(), "mylib: Cargo.toml") {
		t.Fatalf("stray Cargo.toml must fail the tree by default, got %v", err)
	}
	t.Logf("no --only: %v", err)

	tree, err := loadTree(root, []string{"good"})
	if err != nil {
		t.Fatalf("--only good must skip the unselectable sibling: %v", err)
	}
	if len(tree.Apps) != 1 || tree.Apps[0].Dir != "good" {
		t.Fatalf("--only good: %+v", tree.Apps)
	}

	// Explicitly asking for the broken folder still reports it.
	if _, err := loadTree(root, []string{"mylib"}); err == nil || !strings.Contains(err.Error(), "mylib: Cargo.toml") {
		t.Fatalf("--only mylib must surface the failure, got %v", err)
	}

	// --only naming nothing real still gives the no-match error.
	if _, err := loadTree(root, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "--only nope: no application folder or name matches") {
		t.Fatalf("--only nope: %v", err)
	}

	// A root network.toml failure is never suppressed by --only.
	files["network.toml"] = "not toml at all"
	if _, err := loadTree(writeTree(t, files), []string{"good"}); err == nil || !strings.Contains(err.Error(), "network.toml:") {
		t.Fatalf("broken root network.toml must fail even with --only, got %v", err)
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
		t.Logf("network=%s app dir=%s name=%s merged=%v path=%s", tree.Network.Name, app.Dir, app.Spec.Name, app.Merged, filepath.Base(app.Path))
		if app.Merged {
			t.Errorf("%s: real tree must keep the legacy hakopod.toml path", app.Dir)
		}
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
