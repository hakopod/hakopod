package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalEnvironmentFilesStayInsideConfigurationDirectory(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.toml")
	data := []byte("name='app'\nenv_file='.env'\n[services.web]\nimage='nginx'")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MODE=local"), 0600); err != nil {
		t.Fatal(err)
	}
	app, files, err := validateLocalConfiguration(config, data)
	if err != nil || files[".env"] != "MODE=local" || app.Env["MODE"] != "local" {
		t.Fatal("local import failed", err)
	}
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("MODE=outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvironmentFiles(config, data); err == nil {
		t.Fatal("symlink escaped the configuration directory")
	}
}
