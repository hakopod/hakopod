package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelletier/go-toml/v2"
	"k8s.io/apimachinery/pkg/util/validation"
)

const operatorConfigLimit = 64 << 10

type operatorConfig struct {
	SchemaVersion int `toml:"schema_version"`
	Server        struct {
		DeploymentMode      *string `toml:"deployment_mode"`
		Listen              *string `toml:"listen"`
		WebOrigin           *string `toml:"web_origin"`
		DatabaseURLFile     *string `toml:"database_url_file"`
		KubeconfigFile      *string `toml:"kubeconfig_file"`
		AppDomain           *string `toml:"app_domain"`
		IngressClass        *string `toml:"ingress_class"`
		RolloutTimeout      *string `toml:"rollout_timeout"`
		PublicPort          *int    `toml:"public_port"`
		PublicHTTPSPort     *int    `toml:"public_https_port"`
		PublicTCPPorts      *[]int  `toml:"public_tcp_ports"`
		ReadinessProbeImage *string `toml:"readiness_probe_image"`
		TLSIssuer           *string `toml:"tls_issuer"`
		TLSCertFile         *string `toml:"tls_cert_file"`
		TLSKeyFile          *string `toml:"tls_key_file"`
		TrustProxy          *bool   `toml:"trust_proxy"`
		SupervisorURL       *string `toml:"k3s_supervisor_url"`
		HAProxyNamespace    *string `toml:"haproxy_namespace"`
		HAProxyConfigMap    *string `toml:"haproxy_configmap"`
		HAProxyRelease      *string `toml:"haproxy_release"`
	} `toml:"server"`
	Auth struct {
		SignupEnabled     *bool   `toml:"signup_enabled"`
		SetupSecretFile   *string `toml:"setup_secret_file"`
		EncryptionKeyFile *string `toml:"encryption_key_file"`
	} `toml:"auth"`
	OAuth struct {
		GitHub operatorProvider `toml:"github"`
		GitLab operatorProvider `toml:"gitlab"`
		Google operatorProvider `toml:"google"`
		OIDC   struct {
			ClientID         *string `toml:"client_id"`
			ClientSecretFile *string `toml:"client_secret_file"`
			IssuerURL        *string `toml:"issuer_url"`
		} `toml:"oidc"`
	} `toml:"oauth"`
	SMTP struct {
		Enabled      *bool   `toml:"enabled"`
		Address      *string `toml:"address"`
		From         *string `toml:"from"`
		Username     *string `toml:"username"`
		PasswordFile *string `toml:"password_file"`
	} `toml:"smtp"`
	AWS struct {
		IdentitiesFile *string `toml:"identities_file"`
	} `toml:"aws"`
	Backups struct {
		PGDumpPath      *string `toml:"pg_dump_path"`
		StateDir        *string `toml:"state_dir"`
		ManagedPostgres *bool   `toml:"managed_postgres"`
	} `toml:"backups"`
}
type operatorProvider struct {
	ClientID         *string `toml:"client_id"`
	ClientSecretFile *string `toml:"client_secret_file"`
}
type operatorSetting struct {
	field, env string
	value      *string
	file       bool
	restricted bool
	secret     bool
}

func boolSetting(value *bool) *string {
	if value == nil {
		return nil
	}
	out := strconv.FormatBool(*value)
	return &out
}
func intSetting(value *int) *string {
	if value == nil {
		return nil
	}
	out := strconv.Itoa(*value)
	return &out
}

func portsSetting(value *[]int) *string {
	if value == nil {
		return nil
	}
	ports := make([]string, 0, len(*value))
	for _, port := range *value {
		ports = append(ports, strconv.Itoa(port))
	}
	out := strings.Join(ports, ",")
	return &out
}

// This runs once before opening PostgreSQL or starting any worker. No setting is
// applied until the complete file and its effective secret references validate.
func loadOperatorConfig() error {
	path := os.Getenv("HAKOPOD_CONFIG_FILE")
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read HAKOPOD_CONFIG_FILE")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > operatorConfigLimit {
		return fmt.Errorf("HAKOPOD_CONFIG_FILE must be a regular file of at most 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, operatorConfigLimit+1))
	if err != nil || len(data) > operatorConfigLimit {
		return fmt.Errorf("cannot read HAKOPOD_CONFIG_FILE within its 64 KiB limit")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("invalid HAKOPOD_CONFIG_FILE location")
	}
	settings, err := operatorSettings(data, base, os.LookupEnv)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err = os.Setenv(key, settings[key]); err != nil {
			return fmt.Errorf("cannot apply operator configuration")
		}
	}
	return nil
}

func operatorSettings(data []byte, base string, lookup func(string) (string, bool)) (map[string]string, error) {
	if len(data) > operatorConfigLimit {
		return nil, fmt.Errorf("operator configuration exceeds 64 KiB")
	}
	var c operatorConfig
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c); err != nil {
		return nil, fmt.Errorf("operator configuration must be valid TOML with supported settings; plaintext credential settings are not accepted")
	}
	if c.SchemaVersion != 1 {
		return nil, fmt.Errorf("operator configuration requires schema_version = 1")
	}
	settings := []operatorSetting{
		{"server.deployment_mode", "HAKOPOD_DEPLOYMENT_MODE", c.Server.DeploymentMode, false, false, false},
		{"server.listen", "HAKOPOD_LISTEN", c.Server.Listen, false, false, false},
		{"server.web_origin", "HAKOPOD_WEB_ORIGIN", c.Server.WebOrigin, false, false, false},
		{"server.database_url_file", "HAKOPOD_DATABASE_URL_FILE", c.Server.DatabaseURLFile, true, true, true},
		{"server.kubeconfig_file", "HAKOPOD_KUBECONFIG", c.Server.KubeconfigFile, true, true, false},
		{"server.app_domain", "HAKOPOD_APP_DOMAIN", c.Server.AppDomain, false, false, false},
		{"server.ingress_class", "HAKOPOD_INGRESS_CLASS", c.Server.IngressClass, false, false, false},
		{"server.rollout_timeout", "HAKOPOD_ROLLOUT_TIMEOUT", c.Server.RolloutTimeout, false, false, false},
		{"server.public_port", "HAKOPOD_PUBLIC_PORT", intSetting(c.Server.PublicPort), false, false, false},
		{"server.public_https_port", "HAKOPOD_PUBLIC_HTTPS_PORT", intSetting(c.Server.PublicHTTPSPort), false, false, false},
		{"server.public_tcp_ports", "HAKOPOD_PUBLIC_TCP_PORTS", portsSetting(c.Server.PublicTCPPorts), false, false, false},
		{"server.readiness_probe_image", "HAKOPOD_READINESS_PROBE_IMAGE", c.Server.ReadinessProbeImage, false, false, false},
		{"server.tls_issuer", "HAKOPOD_TLS_ISSUER", c.Server.TLSIssuer, false, false, false},
		{"server.tls_cert_file", "HAKOPOD_TLS_CERT", c.Server.TLSCertFile, true, false, false},
		{"server.tls_key_file", "HAKOPOD_TLS_KEY", c.Server.TLSKeyFile, true, true, false},
		{"server.trust_proxy", "HAKOPOD_TRUST_PROXY", boolSetting(c.Server.TrustProxy), false, false, false},
		{"server.k3s_supervisor_url", "HAKOPOD_K3S_SUPERVISOR_URL", c.Server.SupervisorURL, false, false, false},
		{"server.haproxy_namespace", "HAKOPOD_HAPROXY_NAMESPACE", c.Server.HAProxyNamespace, false, false, false},
		{"server.haproxy_configmap", "HAKOPOD_HAPROXY_CONFIGMAP", c.Server.HAProxyConfigMap, false, false, false},
		{"server.haproxy_release", "HAKOPOD_HAPROXY_RELEASE", c.Server.HAProxyRelease, false, false, false},
		{"auth.signup_enabled", "HAKOPOD_SIGNUP_ENABLED", boolSetting(c.Auth.SignupEnabled), false, false, false},
		{"auth.setup_secret_file", "HAKOPOD_SETUP_SECRET_FILE", c.Auth.SetupSecretFile, true, true, true},
		{"auth.encryption_key_file", "HAKOPOD_AUTH_ENCRYPTION_KEY_FILE", c.Auth.EncryptionKeyFile, true, true, true},
		{"oauth.github.client_id", "HAKOPOD_GITHUB_CLIENT_ID", c.OAuth.GitHub.ClientID, false, false, false},
		{"oauth.github.client_secret_file", "HAKOPOD_GITHUB_CLIENT_SECRET_FILE", c.OAuth.GitHub.ClientSecretFile, true, true, true},
		{"oauth.gitlab.client_id", "HAKOPOD_GITLAB_CLIENT_ID", c.OAuth.GitLab.ClientID, false, false, false},
		{"oauth.gitlab.client_secret_file", "HAKOPOD_GITLAB_CLIENT_SECRET_FILE", c.OAuth.GitLab.ClientSecretFile, true, true, true},
		{"oauth.google.client_id", "HAKOPOD_GOOGLE_CLIENT_ID", c.OAuth.Google.ClientID, false, false, false},
		{"oauth.google.client_secret_file", "HAKOPOD_GOOGLE_CLIENT_SECRET_FILE", c.OAuth.Google.ClientSecretFile, true, true, true},
		{"oauth.oidc.client_id", "HAKOPOD_OIDC_CLIENT_ID", c.OAuth.OIDC.ClientID, false, false, false},
		{"oauth.oidc.client_secret_file", "HAKOPOD_OIDC_CLIENT_SECRET_FILE", c.OAuth.OIDC.ClientSecretFile, true, true, true},
		{"oauth.oidc.issuer_url", "HAKOPOD_OIDC_ISSUER_URL", c.OAuth.OIDC.IssuerURL, false, false, false},
		{"smtp.enabled", "HAKOPOD_SMTP_ENABLED", boolSetting(c.SMTP.Enabled), false, false, false},
		{"smtp.address", "HAKOPOD_SMTP_ADDRESS", c.SMTP.Address, false, false, false},
		{"smtp.from", "HAKOPOD_SMTP_FROM", c.SMTP.From, false, false, false},
		{"smtp.username", "HAKOPOD_SMTP_USERNAME", c.SMTP.Username, false, false, false},
		{"smtp.password_file", "HAKOPOD_SMTP_PASSWORD_FILE", c.SMTP.PasswordFile, true, true, true},
		{"aws.identities_file", "HAKOPOD_AWS_IDENTITIES_FILE", c.AWS.IdentitiesFile, true, true, false},
		{"backups.pg_dump_path", "HAKOPOD_PG_DUMP_PATH", c.Backups.PGDumpPath, false, false, false},
		{"backups.state_dir", "HAKOPOD_BACKUP_STATE_DIR", c.Backups.StateDir, false, false, false},
		{"backups.managed_postgres", "HAKOPOD_MANAGED_POSTGRES", boolSetting(c.Backups.ManagedPostgres), false, false, false},
	}
	result := make(map[string]string, len(settings))
	for _, setting := range settings {
		if setting.value == nil {
			continue
		}
		value := *setting.value
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("%s has an invalid length or control character", setting.field)
		}
		if err := validateOperatorValue(setting.field, value); err != nil {
			return nil, err
		}
		if _, present := lookup(setting.env); present {
			continue
		}
		if setting.secret {
			if _, present := lookup(strings.TrimSuffix(setting.env, "_FILE")); present {
				continue
			}
		}
		if setting.file && value != "" {
			if !filepath.IsAbs(value) {
				value = filepath.Join(base, value)
			}
			value = filepath.Clean(value)
			file, err := os.Open(value)
			if err != nil {
				return nil, fmt.Errorf("cannot open %s", setting.field)
			}
			info, err := file.Stat()
			_ = file.Close()
			limit := int64(operatorConfigLimit)
			if setting.secret {
				limit = 4096
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() > limit || (setting.restricted && info.Mode().Perm()&0077 != 0) {
				return nil, fmt.Errorf("%s must reference a readable regular file within its size and permission limits", setting.field)
			}
		}
		result[setting.env] = value
	}
	get := func(key string) string {
		if value, present := lookup(key); present {
			return value
		}
		return result[key]
	}
	mode, err := cluster.ParseDeploymentMode(get("HAKOPOD_DEPLOYMENT_MODE"))
	if err != nil {
		return nil, err
	}
	if mode == cluster.DeploymentManagedCloud && get("HAKOPOD_PUBLIC_TCP_PORTS") != "" {
		return nil, fmt.Errorf("managed-cloud installations cannot configure public TCP ports")
	}
	if _, err := authConfigFrom(get); err != nil {
		return nil, err
	}
	database, err := secretSettingFrom("HAKOPOD_DATABASE_URL", get)
	if err != nil {
		return nil, err
	}
	if database != "" {
		if _, err = pgxpool.ParseConfig(database); err != nil {
			return nil, fmt.Errorf("database connection settings are invalid")
		}
	}
	if (get("HAKOPOD_TLS_CERT") == "") != (get("HAKOPOD_TLS_KEY") == "") {
		return nil, fmt.Errorf("server TLS requires both certificate and key files")
	}
	if get("HAKOPOD_TLS_CERT") != "" {
		if _, err := tls.LoadX509KeyPair(get("HAKOPOD_TLS_CERT"), get("HAKOPOD_TLS_KEY")); err != nil {
			return nil, fmt.Errorf("server TLS certificate and key are invalid or do not match")
		}
	}
	listen := get("HAKOPOD_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("server.listen must be host:port")
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && get("HAKOPOD_TLS_CERT") == "" && get("HAKOPOD_TRUST_PROXY") != "true" {
		return nil, fmt.Errorf("non-loopback API requires TLS or server.trust_proxy behind an HTTPS reverse proxy")
	}
	return result, nil
}

func validateOperatorValue(field, value string) error {
	if value == "" {
		return nil
	}
	switch field {
	case "server.readiness_probe_image":
		return cluster.ValidateReadinessProbeImage(value)
	case "server.deployment_mode":
		_, err := cluster.ParseDeploymentMode(value)
		return err
	case "server.public_tcp_ports":
		_, err := cluster.ParsePublicTCPPorts(value)
		return err
	case "server.public_port", "server.public_https_port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535", field)
		}
	case "server.rollout_timeout":
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 15*time.Second || duration > 15*time.Minute {
			return fmt.Errorf("server.rollout_timeout must be between 15s and 15m")
		}
	case "server.app_domain":
		if len(value) > 190 || len(validation.IsDNS1123Subdomain(value)) != 0 {
			return fmt.Errorf("server.app_domain must be a DNS domain of at most 190 characters")
		}
	case "server.haproxy_namespace":
		if len(validation.IsDNS1123Label(value)) != 0 {
			return fmt.Errorf("server.haproxy_namespace must be a Kubernetes namespace name")
		}
	case "server.ingress_class", "server.tls_issuer", "server.haproxy_configmap", "server.haproxy_release":
		if len(validation.IsDNS1123Subdomain(value)) != 0 {
			return fmt.Errorf("%s must be a valid Kubernetes name", field)
		}
	case "server.listen", "smtp.address":
		host, port, err := net.SplitHostPort(value)
		number, e := strconv.Atoi(port)
		if err != nil || e != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%s must contain a host and a numeric port between 1 and 65535", field)
		}
		if strings.ContainsAny(host, "@/\\ ") || (field == "smtp.address" && host == "") {
			return fmt.Errorf("%s must not contain credentials or an empty SMTP host", field)
		}
	case "server.web_origin":
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("server.web_origin must be a dashboard origin without credentials or a path")
		}
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
		if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
			return fmt.Errorf("server.web_origin requires HTTPS except on loopback")
		}
	case "server.k3s_supervisor_url":
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("server.k3s_supervisor_url must be an HTTPS URL without credentials, a query or a fragment")
		}
	case "backups.state_dir":
		if !filepath.IsAbs(value) {
			return fmt.Errorf("backups.state_dir must be an absolute path")
		}
	}
	return nil
}
