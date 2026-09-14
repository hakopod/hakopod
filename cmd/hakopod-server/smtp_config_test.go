package main

import (
	"strings"
	"testing"
)

func TestSMTPSecurityOperatorAndEnvironmentConfiguration(t *testing.T) {
	for _, security := range []string{"starttls", "tls"} {
		settings, err := operatorSettings([]byte("schema_version=1\n[smtp]\nsecurity='"+security+"'"), t.TempDir(), noOperatorEnvironment)
		if err != nil || settings["HAKOPOD_SMTP_SECURITY"] != security {
			t.Fatalf("SMTP security TOML mapping failed: %v", err)
		}
		config, err := authConfigFrom(func(key string) string { return settings[key] })
		if err != nil || config.SMTPSecurity != security {
			t.Fatalf("SMTP security environment mapping failed: %v", err)
		}
	}
	config, err := authConfigFrom(func(string) string { return "" })
	if err != nil || config.SMTPSecurity != "starttls" {
		t.Fatal("operator SMTP must retain STARTTLS by default")
	}
	for _, invalid := range []string{"plain", "none", "STARTTLS", "private-value"} {
		_, err := operatorSettings([]byte("schema_version=1\n[smtp]\nsecurity='"+invalid+"'"), t.TempDir(), noOperatorEnvironment)
		if err == nil || strings.Contains(err.Error(), invalid) {
			t.Fatal("invalid SMTP TOML security accepted or echoed")
		}
		_, err = authConfigFrom(func(key string) string {
			if key == "HAKOPOD_SMTP_SECURITY" {
				return invalid
			}
			return ""
		})
		if err == nil || strings.Contains(err.Error(), invalid) {
			t.Fatal("invalid SMTP environment security accepted or echoed")
		}
	}
}
