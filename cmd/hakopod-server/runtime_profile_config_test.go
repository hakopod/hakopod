package main

import (
	"os"
	"testing"
)

func TestOperatorRuntimeProfileFile(t *testing.T) {
	dir := t.TempDir()
	path := operatorSecret(t, dir, "runtime-profiles.toml", "schema_version=1\n")
	input := []byte("schema_version=1\n[runtime_profiles]\nfile='runtime-profiles.toml'\n")
	settings, err := operatorSettings(input, dir, noOperatorEnvironment)
	if err != nil || settings["HAKOPOD_RUNTIME_PROFILES_FILE"] != path {
		t.Fatal("the runtime profile file was not resolved relative to the operator file", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorSettings(input, dir, noOperatorEnvironment); err == nil {
		t.Fatal("a public operator binding file was accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	cloud := append([]byte("schema_version=1\n[server]\ndeployment_mode='managed-cloud'\n"), []byte("[runtime_profiles]\nfile='runtime-profiles.toml'\n")...)
	if _, err := operatorSettings(cloud, dir, noOperatorEnvironment); err == nil {
		t.Fatal("managed cloud accepted an application runtime binding file")
	}
	override := func(key string) (string, bool) {
		if key == "HAKOPOD_RUNTIME_PROFILES_FILE" {
			return "", true
		}
		return "", false
	}
	if _, err := operatorSettings(cloud, dir, override); err != nil {
		t.Fatal("the explicit empty environment override did not take precedence", err)
	}
}
