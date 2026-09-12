package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noOperatorEnvironment(string) (string, bool) { return "", false }
func operatorSecret(t *testing.T, dir, name, value string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOperatorTOMLStrictVersionTypesAndBounds(t *testing.T) {
	for _, input := range []string{
		"", "schema_version=2", "schema_version=1\nsecret='sensitive-value'", "schema_version=1\n[auth]\nsignup_enabled='true'", "schema_version=1\n[oauth.github]\nclient_secret='sensitive-value'", "schema_version=1\n[server]\ndatabase_url='postgres://user:sensitive-value@localhost/db'", "schema_version=1\n[smtp]\npassword='sensitive-value'", "schema_version=1\n[server]\npublic_port=70000", "schema_version=1\n[server]\nrollout_timeout='1s'", "schema_version=1\n[server]\napp_domain='not a domain'", "schema_version=1\n[auth]\nunknown='sensitive-value'", "schema_version=1\n[backups]\nstate_dir='relative'", "schema_version=1\n[oauth.other]\nclient_id='unknown'", "schema_version=1\n[server]\nk3s_supervisor_url='https://user:sensitive-value@host.test/'", "schema_version=1\n[server]\nlisten='0.0.0.0:8080'", "schema_version=1\n[server]\nweb_origin='http://public.example.test'", "schema_version=1\n" + strings.Repeat("#", operatorConfigLimit),
	} {
		settings, err := operatorSettings([]byte(input), t.TempDir(), noOperatorEnvironment)
		if err == nil || settings != nil {
			t.Fatalf("invalid operator file accepted (length %d)", len(input))
		}
		if strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal("operator error exposed a credential value")
		}
	}
	settings, err := operatorSettings([]byte("schema_version=1\n[auth]\nsignup_enabled=false"), t.TempDir(), noOperatorEnvironment)
	if err != nil || settings["HAKOPOD_SIGNUP_ENABLED"] != "false" {
		t.Fatalf("explicit false was lost: %v", err)
	}
}

func TestOperatorTOMLEnvironmentOverridesAndSecretAliases(t *testing.T) {
	input := []byte("schema_version=1\n[auth]\nsignup_enabled=true\n[server]\nlisten='127.0.0.1:8081'\n[oauth.github]\nclient_id='configured-client'\nclient_secret_file='missing-but-overridden'")
	existing := map[string]string{"HAKOPOD_SIGNUP_ENABLED": "false", "HAKOPOD_LISTEN": "", "HAKOPOD_GITHUB_CLIENT_SECRET": "environment-secret"}
	lookup := func(key string) (string, bool) { value, present := existing[key]; return value, present }
	settings, err := operatorSettings(input, t.TempDir(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HAKOPOD_SIGNUP_ENABLED", "HAKOPOD_LISTEN", "HAKOPOD_GITHUB_CLIENT_SECRET_FILE"} {
		if _, present := settings[key]; present {
			t.Fatalf("environment override was replaced for %s", key)
		}
	}
	if settings["HAKOPOD_GITHUB_CLIENT_ID"] != "configured-client" {
		t.Fatal("unconfigured environment key did not take the file default")
	}
	dir := t.TempDir()
	path := operatorSecret(t, dir, "client-secret", "file-secret")
	existing = map[string]string{"HAKOPOD_GITHUB_CLIENT_SECRET_FILE": path}
	settings, err = operatorSettings(input, dir, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := settings["HAKOPOD_GITHUB_CLIENT_SECRET_FILE"]; present {
		t.Fatal("existing secret file was replaced")
	}
}

func TestOperatorTOMLRestrictedReferencesAndMappings(t *testing.T) {
	dir := t.TempDir()
	operatorSecret(t, dir, "database", "postgres://operator:local-test-password@127.0.0.1/hakopod")
	operatorSecret(t, dir, "setup", strings.Repeat("s", 32))
	operatorSecret(t, dir, "encryption", strings.Repeat("a", 64))
	path := operatorSecret(t, dir, "client-secret", "local-client-secret")
	operatorSecret(t, dir, "smtp-secret", "local-smtp-secret")
	input := []byte(`schema_version = 1
[server]
listen = "127.0.0.1:8088"
web_origin = "https://dashboard.example.test"
database_url_file = "database"
app_domain = "apps.example.test"
public_port = 80
public_https_port = 443
rollout_timeout = "3m"
ingress_class = "haproxy"
tls_issuer = "letsencrypt"
haproxy_namespace = "haproxy-controller"
haproxy_configmap = "hakopod-ingress"
haproxy_release = "hakopod-ingress"
k3s_supervisor_url = "https://control.example.test:6443"
[auth]
signup_enabled = true
setup_secret_file = "setup"
encryption_key_file = "encryption"
[oauth.github]
client_id = "github-client"
client_secret_file = "client-secret"
[oauth.gitlab]
client_id = "gitlab-client"
client_secret_file = "client-secret"
[oauth.google]
client_id = "google-client"
client_secret_file = "client-secret"
[smtp]
enabled = true
address = "smtp.example.test:587"
from = "accounts@example.test"
username = "mail-user"
password_file = "smtp-secret"
[backups]
pg_dump_path = "/usr/bin/pg_dump"
state_dir = "/var/lib/hakopod/backups"
managed_postgres = true
`)
	settings, err := operatorSettings(input, dir, noOperatorEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"HAKOPOD_DATABASE_URL_FILE": filepath.Join(dir, "database"), "HAKOPOD_GITHUB_CLIENT_SECRET_FILE": path, "HAKOPOD_SMTP_PASSWORD_FILE": filepath.Join(dir, "smtp-secret"), "HAKOPOD_SIGNUP_ENABLED": "true", "HAKOPOD_MANAGED_POSTGRES": "true", "HAKOPOD_PUBLIC_PORT": "80", "HAKOPOD_PUBLIC_HTTPS_PORT": "443", "HAKOPOD_ROLLOUT_TIMEOUT": "3m"}
	for key, want := range expected {
		if settings[key] != want {
			t.Fatalf("unexpected mapping for %s", key)
		}
	}
	for _, value := range settings {
		if value == "local-client-secret" || value == "local-smtp-secret" || strings.Contains(value, "postgres://") {
			t.Fatal("secret value was copied into planned environment defaults")
		}
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = operatorSettings(input, dir, noOperatorEnvironment); err == nil {
		t.Fatal("group-readable credential reference was accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = operatorSettings(input, dir, noOperatorEnvironment); err == nil {
		t.Fatal("oversized credential reference was accepted")
	}
}

func TestOperatorConfigRejectsWholeFileBeforeEnvironmentMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.toml")
	if err := os.WriteFile(path, []byte("schema_version=1\n[auth]\nsignup_enabled=true\n[oauth.github]\nclient_id='missing-secret'"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKOPOD_CONFIG_FILE", path)
	t.Setenv("HAKOPOD_GITHUB_CLIENT_ID", "missing-secret")
	t.Setenv("HAKOPOD_GITHUB_CLIENT_SECRET", "")
	t.Setenv("HAKOPOD_GITHUB_CLIENT_SECRET_FILE", "")
	before, present := os.LookupEnv("HAKOPOD_SIGNUP_ENABLED")
	if err := loadOperatorConfig(); err == nil {
		t.Fatal("partial OAuth configuration was accepted")
	}
	after, afterPresent := os.LookupEnv("HAKOPOD_SIGNUP_ENABLED")
	if before != after || present != afterPresent {
		t.Fatal("a setting was applied before validation completed")
	}
}

func TestOperatorExampleAndValidatedDefaultApplication(t *testing.T) {
	example, err := os.ReadFile("../../examples/hakopod-server.toml")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := operatorSettings(example, t.TempDir(), noOperatorEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if settings["HAKOPOD_SIGNUP_ENABLED"] != "false" || settings["HAKOPOD_SMTP_ENABLED"] != "false" {
		t.Fatal("example must leave registration and mail opt-in")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err = os.WriteFile(path, []byte("schema_version=1\n[server]\nrollout_timeout='5m'\n[auth]\nsignup_enabled=true"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKOPOD_CONFIG_FILE", path)
	t.Setenv("HAKOPOD_ROLLOUT_TIMEOUT", "")
	if err = os.Unsetenv("HAKOPOD_ROLLOUT_TIMEOUT"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKOPOD_SIGNUP_ENABLED", "false")
	if err = loadOperatorConfig(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HAKOPOD_ROLLOUT_TIMEOUT") != "5m" || os.Getenv("HAKOPOD_SIGNUP_ENABLED") != "false" {
		t.Fatal("validated defaults did not preserve environment precedence")
	}
}

func TestAuthConfigReadsProviderAndSMTPSecretFiles(t *testing.T) {
	dir := t.TempDir()
	path := operatorSecret(t, dir, "secret", "restricted-client-secret")
	values := map[string]string{"HAKOPOD_GITHUB_CLIENT_ID": "github-client", "HAKOPOD_GITHUB_CLIENT_SECRET_FILE": path, "HAKOPOD_SMTP_PASSWORD_FILE": path}
	get := func(key string) string { return values[key] }
	config, err := authConfigFrom(get)
	if err != nil || config.GitHubClientSecret != "restricted-client-secret" || config.SMTPPassword != "restricted-client-secret" {
		t.Fatal("auth did not load restricted secret references")
	}
	values["HAKOPOD_GITHUB_CLIENT_SECRET"] = "ambiguous"
	if _, err = authConfigFrom(get); err == nil {
		t.Fatal("two credential sources were accepted")
	}
}
