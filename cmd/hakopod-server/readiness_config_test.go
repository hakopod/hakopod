package main

import (
	"strings"
	"testing"
)

func TestOperatorReadinessImageConfiguration(t *testing.T) {
	image := "registry.example.com/hakopod/probe@sha256:" + strings.Repeat("a", 64)
	settings, err := operatorSettings([]byte("schema_version=1\n[server]\nreadiness_probe_image='"+image+"'"), t.TempDir(), noOperatorEnvironment)
	if err != nil || settings["HAKOPOD_READINESS_PROBE_IMAGE"] != image {
		t.Fatalf("probe image setting: %v", err)
	}
	for _, invalid := range []string{"registry.example.com/probe:latest", "https://registry.example.com/probe@sha256:" + strings.Repeat("a", 64)} {
		if _, err := operatorSettings([]byte("schema_version=1\n[server]\nreadiness_probe_image='"+invalid+"'"), t.TempDir(), noOperatorEnvironment); err == nil {
			t.Fatal("invalid helper image accepted")
		}
	}
}
