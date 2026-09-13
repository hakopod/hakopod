package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallationSecretFileRestrictions(t *testing.T) {
	const setting = "HAKOPOD_TEST_INSTALLATION_SECRET"
	t.Setenv(setting, "")
	file := filepath.Join(t.TempDir(), "installation-secret")
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(setting+"_FILE", file)
	if value, err := secretSetting(setting); err != nil || len(value) != 64 {
		t.Fatal("restricted installation file was not accepted")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := secretSetting(setting); err == nil {
		t.Fatal("world-readable installation proof was accepted")
	}
	t.Setenv(setting, "also-set")
	if _, err := secretSetting(setting); err == nil {
		t.Fatal("ambiguous secret configuration was accepted")
	}
}

func TestAuthOriginAndProviderConfiguration(t *testing.T) {
	for _, name := range []string{"HAKOPOD_SETUP_SECRET", "HAKOPOD_SETUP_SECRET_FILE", "HAKOPOD_AUTH_ENCRYPTION_KEY", "HAKOPOD_AUTH_ENCRYPTION_KEY_FILE", "HAKOPOD_GITHUB_CLIENT_ID", "HAKOPOD_GITHUB_CLIENT_SECRET", "HAKOPOD_GOOGLE_CLIENT_ID", "HAKOPOD_GOOGLE_CLIENT_SECRET", "HAKOPOD_GITLAB_CLIENT_ID", "HAKOPOD_GITLAB_CLIENT_SECRET", "HAKOPOD_SMTP_ENABLED", "HAKOPOD_SIGNUP_ENABLED"} {
		t.Setenv(name, "")
	}
	for _, origin := range []string{"http://dashboard.example.com", "https://dashboard.example.com/path", "https://user@dashboard.example.com", "https://dashboard.example.com?token=example"} {
		t.Setenv("HAKOPOD_WEB_ORIGIN", origin)
		if _, err := authConfig(); err == nil {
			t.Errorf("unsafe origin accepted: %s", origin)
		}
	}
	t.Setenv("HAKOPOD_WEB_ORIGIN", "http://127.0.0.1:4173")
	c, err := authConfig()
	if err != nil || c.SetupSecret != "" || c.SignupEnabled {
		t.Fatal("loopback installation should load without inventing an owner credential")
	}
	t.Setenv("HAKOPOD_GITHUB_CLIENT_ID", "configured-client")
	if _, err = authConfig(); err == nil {
		t.Fatal("partially configured OAuth provider was accepted")
	}
	t.Setenv("HAKOPOD_GITHUB_CLIENT_ID", "")
	t.Setenv("HAKOPOD_GITLAB_CLIENT_ID", "gitlab-client")
	if _, err = authConfig(); err == nil {
		t.Fatal("partial GitLab provider configuration accepted")
	}
	t.Setenv("HAKOPOD_GITLAB_CLIENT_SECRET", "gitlab-secret")
	c, err = authConfig()
	if err != nil || c.GitLabClientID != "gitlab-client" || c.GitLabClientSecret != "gitlab-secret" {
		t.Fatal("GitLab provider configuration did not load")
	}
}

func TestRegistrationRequiresCloudBuildAndConfiguredPolicy(t *testing.T) {
	for _, mode := range []string{"", "self-hosted", "managed-cloud"} {
		for _, value := range []string{"", "false", "TRUE", "1", "true"} {
			settings := map[string]string{"HAKOPOD_DEPLOYMENT_MODE": mode, "HAKOPOD_SIGNUP_ENABLED": value}
			config, err := authConfigFrom(func(key string) string { return settings[key] })
			want := cloudSignupAvailable && mode == "managed-cloud" && value == "true"
			if err != nil || config.PublicSignupEnabled() != want {
				t.Fatalf("signup configuration mode=%q enabled=%q: %v", mode, value, err)
			}
		}
	}
	if _, err := authConfigFrom(func(key string) string {
		if key == "HAKOPOD_DEPLOYMENT_MODE" {
			return "unknown"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid deployment mode accepted by auth configuration")
	}
}

func TestRegistrationOperatorPolicyCannotEnableSelfHostedBuild(t *testing.T) {
	for _, mode := range []string{"self-hosted", "managed-cloud"} {
		input := []byte("schema_version=1\n[server]\ndeployment_mode='" + mode + "'\n[auth]\nsignup_enabled=true")
		settings, err := operatorSettings(input, t.TempDir(), noOperatorEnvironment)
		if err != nil {
			t.Fatal(err)
		}
		config, err := authConfigFrom(func(key string) string { return settings[key] })
		if err != nil || config.PublicSignupEnabled() != (cloudSignupAvailable && mode == "managed-cloud") {
			t.Fatalf("operator signup policy escaped build restriction: %v", err)
		}
	}
}
