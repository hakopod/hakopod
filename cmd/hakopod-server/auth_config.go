package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/hakopod/hakopod/internal/api"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

func secretSetting(name string) (string, error) {
	value, path := os.Getenv(name), os.Getenv(name+"_FILE")
	if value != "" && path != "" {
		return "", fmt.Errorf("set only %s or %s_FILE", name, name)
	}
	if path == "" {
		return value, nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s_FILE must reference a restricted regular file of at most 4 KiB", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s_FILE", name)
	}
	return strings.TrimSpace(string(data)), nil
}
func authConfig() (api.AuthConfig, error) {
	setup, err := secretSetting("HAKOPOD_SETUP_SECRET")
	if err != nil {
		return api.AuthConfig{}, err
	}
	encryption, err := secretSetting("HAKOPOD_AUTH_ENCRYPTION_KEY")
	if err != nil {
		return api.AuthConfig{}, err
	}
	c := api.AuthConfig{PublicURL: env("HAKOPOD_WEB_ORIGIN", "http://127.0.0.1:4173"), SetupSecret: setup, EncryptionKey: encryption,
		GitHubClientID: os.Getenv("HAKOPOD_GITHUB_CLIENT_ID"), GitHubClientSecret: os.Getenv("HAKOPOD_GITHUB_CLIENT_SECRET"), GoogleClientID: os.Getenv("HAKOPOD_GOOGLE_CLIENT_ID"), GoogleClientSecret: os.Getenv("HAKOPOD_GOOGLE_CLIENT_SECRET"),
		SMTPAddress: os.Getenv("HAKOPOD_SMTP_ADDRESS"), SMTPUsername: os.Getenv("HAKOPOD_SMTP_USERNAME"), SMTPPassword: os.Getenv("HAKOPOD_SMTP_PASSWORD"), SMTPFrom: os.Getenv("HAKOPOD_SMTP_FROM"), SMTPAllowDelivery: os.Getenv("HAKOPOD_SMTP_ENABLED") == "true"}
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
	if (c.GitHubClientID == "") != (c.GitHubClientSecret == "") || (c.GoogleClientID == "") != (c.GoogleClientSecret == "") {
		return c, fmt.Errorf("each OAuth client ID requires its matching client secret")
	}
	if c.SMTPAllowDelivery {
		if _, _, e = net.SplitHostPort(c.SMTPAddress); e != nil {
			return c, fmt.Errorf("SMTP address must be host:port")
		}
		if _, e = mail.ParseAddress(c.SMTPFrom); e != nil {
			return c, fmt.Errorf("SMTP from address is invalid")
		}
	}
	return c, nil
}
