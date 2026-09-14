package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
)

func secretSetting(name string) (string, error) { return secretSettingFrom(name, os.Getenv) }
func secretSettingFrom(name string, get func(string) string) (string, error) {
	value, path := get(name), get(name+"_FILE")
	if value != "" && path != "" {
		return "", fmt.Errorf("set only %s or %s_FILE", name, name)
	}
	if path == "" {
		return value, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s_FILE", name)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s_FILE must reference a restricted regular file of at most 4 KiB", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return "", fmt.Errorf("cannot read %s_FILE within its 4 KiB limit", name)
	}
	return strings.TrimSpace(string(data)), nil
}

func authConfig() (api.AuthConfig, error) { return authConfigFrom(os.Getenv) }
func authConfigFrom(get func(string) string) (api.AuthConfig, error) {
	mode, err := cluster.ParseDeploymentMode(get("HAKOPOD_DEPLOYMENT_MODE"))
	if err != nil {
		return api.AuthConfig{}, err
	}
	setup, err := secretSettingFrom("HAKOPOD_SETUP_SECRET", get)
	if err != nil {
		return api.AuthConfig{}, err
	}
	encryption, err := secretSettingFrom("HAKOPOD_AUTH_ENCRYPTION_KEY", get)
	if err != nil {
		return api.AuthConfig{}, err
	}
	secrets := map[string]string{}
	for _, name := range []string{"HAKOPOD_GITHUB_CLIENT_SECRET", "HAKOPOD_GITLAB_CLIENT_SECRET", "HAKOPOD_GOOGLE_CLIENT_SECRET", "HAKOPOD_SMTP_PASSWORD", "HAKOPOD_OIDC_CLIENT_SECRET"} {
		value, e := secretSettingFrom(name, get)
		if e != nil {
			return api.AuthConfig{}, e
		}
		secrets[name] = value
	}
	origin := get("HAKOPOD_WEB_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:4173"
	}
	c := api.AuthConfig{CloudSignupAvailable: cloudSignupAvailable, DeploymentMode: mode, SignupEnabled: get("HAKOPOD_SIGNUP_ENABLED") == "true", PublicURL: origin, SetupSecret: setup, EncryptionKey: encryption,
		GitHubClientID: get("HAKOPOD_GITHUB_CLIENT_ID"), GitHubClientSecret: secrets["HAKOPOD_GITHUB_CLIENT_SECRET"], GoogleClientID: get("HAKOPOD_GOOGLE_CLIENT_ID"), GoogleClientSecret: secrets["HAKOPOD_GOOGLE_CLIENT_SECRET"],
		GitLabClientID: get("HAKOPOD_GITLAB_CLIENT_ID"), GitLabClientSecret: secrets["HAKOPOD_GITLAB_CLIENT_SECRET"],
		OIDCClientID: get("HAKOPOD_OIDC_CLIENT_ID"), OIDCClientSecret: secrets["HAKOPOD_OIDC_CLIENT_SECRET"], OIDCIssuerURL: get("HAKOPOD_OIDC_ISSUER_URL"),
		SMTPAddress: get("HAKOPOD_SMTP_ADDRESS"), SMTPSecurity: get("HAKOPOD_SMTP_SECURITY"), SMTPUsername: get("HAKOPOD_SMTP_USERNAME"), SMTPPassword: secrets["HAKOPOD_SMTP_PASSWORD"], SMTPFrom: get("HAKOPOD_SMTP_FROM"), SMTPAllowDelivery: get("HAKOPOD_SMTP_ENABLED") == "true"}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, fmt.Errorf("HAKOPOD_WEB_ORIGIN must be a dashboard origin without a path")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return c, fmt.Errorf("dashboard origin requires HTTPS except on loopback")
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if setup != "" && len(setup) < 24 {
		return c, fmt.Errorf("setup secret must contain at least 24 random characters")
	}
	if encryption != "" {
		valid := false
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			b, e := encoding.DecodeString(encryption)
			valid = valid || (e == nil && len(b) == 32)
		}
		b, e := hex.DecodeString(encryption)
		valid = valid || (e == nil && len(b) == 32)
		if !valid {
			return c, fmt.Errorf("auth encryption key must encode exactly 32 random bytes as base64 or hex")
		}
	}
	if (c.GitHubClientID == "") != (c.GitHubClientSecret == "") || (c.GoogleClientID == "") != (c.GoogleClientSecret == "") || (c.GitLabClientID == "") != (c.GitLabClientSecret == "") {
		return c, fmt.Errorf("each OAuth client ID requires its matching client secret")
	}
	if c.OIDCClientID != "" || c.OIDCClientSecret != "" || c.OIDCIssuerURL != "" {
		issuer, err := url.Parse(c.OIDCIssuerURL)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || c.OIDCClientID == "" || c.OIDCClientSecret == "" {
			return c, fmt.Errorf("OIDC requires a client ID, client secret and HTTPS issuer URL")
		}
	}
	if c.SMTPSecurity == "" {
		c.SMTPSecurity = "starttls"
	}
	if c.SMTPSecurity != "starttls" && c.SMTPSecurity != "tls" {
		return c, fmt.Errorf("SMTP security must be starttls or tls")
	}
	if c.SMTPAllowDelivery {
		if _, _, e = net.SplitHostPort(c.SMTPAddress); e != nil {
			return c, fmt.Errorf("SMTP address must be host:port")
		}
		if _, e = store.NormalizeEmail(c.SMTPFrom); e != nil {
			return c, fmt.Errorf("SMTP from address is invalid")
		}
	}
	return c, nil
}
