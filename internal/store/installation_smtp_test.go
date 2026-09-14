package store

import (
	"errors"
	"strings"
	"testing"
)

func TestSMTPSettingsValidation(t *testing.T) {
	valid := SMTPSettings{Enabled: true, Host: "smtp.example.test", Port: 587, Security: "starttls", Username: "mailer", FromEmail: "hakopod@example.test"}
	for _, mutate := range []func(*SMTPSettings){func(c *SMTPSettings) { c.Host = "https://smtp.example.test" }, func(c *SMTPSettings) { c.Host = "smtp.example.test:587" }, func(c *SMTPSettings) { c.Host = "user:secret@smtp.example.test" }, func(c *SMTPSettings) { c.Host = "bad\nhost" }, func(c *SMTPSettings) { c.Host = "-bad.test" }, func(c *SMTPSettings) { c.Host = strings.Repeat("x", 64) + ".test" }, func(c *SMTPSettings) { c.Host = "" }, func(c *SMTPSettings) { c.Port = 65536 }, func(c *SMTPSettings) { c.Port = 0 }, func(c *SMTPSettings) { c.Security = "plain" }, func(c *SMTPSettings) { c.Username = "user\x00other" }, func(c *SMTPSettings) { c.Username = strings.Repeat("u", 257) }, func(c *SMTPSettings) { c.FromEmail = "bad" }, func(c *SMTPSettings) { c.FromEmail = "a@example.test\r\nBcc: b@example.test" }, func(c *SMTPSettings) { c.FromEmail = "" }, func(c *SMTPSettings) { c.EncryptedPassword = []byte("unencrypted") }} {
		c := valid
		mutate(&c)
		if !errors.Is(c.Validate(), ErrInput) {
			t.Fatal("invalid SMTP settings accepted")
		}
	}
	for _, host := range []string{"smtp.example.test", "localhost", "127.0.0.1", "::1"} {
		c := valid
		c.Host = host
		if err := c.Validate(); err != nil {
			t.Fatalf("valid host %q rejected: %v", host, err)
		}
	}
	disabled := SMTPSettings{Port: 587, Security: "starttls"}
	if err := disabled.Validate(); err != nil {
		t.Fatal("empty disabled configuration rejected", err)
	}
}
