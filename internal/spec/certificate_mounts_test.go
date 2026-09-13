package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackendCertificateMountsSchema(t *testing.T) {
	input := `name='mail'
[services.smtp]
image='example/smtp:1'
run_as_user=1001
fs_group=2001
certificate_mounts=[{certificate='hp-cert-smtp-abc', hostname='mail.example.com', mount_path='/certs/smtp'}]
`
	app, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Services["smtp"].CertificateMounts[0]; got.Hostname != "mail.example.com" || got.Certificate != "hp-cert-smtp-abc" {
		t.Fatalf("certificate reference changed: %+v", got)
	}
	if _, err = Parse([]byte(input + "certificate_pem='sensitive fixture'\n")); err == nil || strings.Contains(err.Error(), "sensitive fixture") {
		t.Fatal("inline certificate data accepted or leaked")
	}
	for name, body := range map[string]string{
		"host path": strings.ReplaceAll(input, "/certs/smtp", "/etc/ssl/private"),
		"relative":  strings.ReplaceAll(input, "/certs/smtp", "certs/smtp"),
		"traversal": strings.ReplaceAll(input, "/certs/smtp", "/certs/../smtp"),
		"wildcard":  strings.ReplaceAll(input, "mail.example.com", "*.example.com"),
		"URI":       strings.ReplaceAll(input, "mail.example.com", "https://mail.example.com"),
		"raw name":  strings.ReplaceAll(input, "hp-cert-smtp-abc", "../other-key"),
		"overlap":   input + "temporary_mounts=[{mount_path='/certs',size_mib=1}]\n",
		"duplicate": strings.ReplaceAll(input, "mount_path='/certs/smtp'}]", "mount_path='/certs/smtp'}, {certificate='another',hostname='mail.example.com',mount_path='/certs/smtp'}]"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("invalid certificate mount accepted")
			}
		})
	}
	before, _ := Normalize(app)
	service := app.Services["smtp"]
	service.CertificateMounts[0].Certificate = "hp-cert-smtp-rotated"
	app.Services["smtp"] = service
	changes := Diff(&before, app)
	if len(changes) != 1 || changes[0].Field != "certificate_mounts" {
		t.Fatalf("rotation missing from review: %+v", changes)
	}
	encoded, _ := json.Marshal(app)
	if strings.Contains(string(encoded), "private_key") || strings.Contains(string(encoded), "certificate_pem") {
		t.Fatal("revision contains key material fields")
	}
}

func TestAutomaticCertificateMountSchema(t *testing.T) {
	input := `name='mail'
[services.smtp]
image='example/smtp:1'
port=8080
public=true
certificate_mounts=[{source='ingress',hostname='mail.example.com',mount_path='/certificates/smtp'}]
`
	app, err := Parse([]byte(input))
	if err != nil || !HasAutomaticCertificates(app) {
		t.Fatal("automatic source rejected", err)
	}
	for _, body := range []string{strings.ReplaceAll(input, "public=true", "public=false"), strings.ReplaceAll(input, "source='ingress'", "source='arbitrary'"), strings.ReplaceAll(input, "source='ingress'", "source='ingress',certificate='manual'"), strings.ReplaceAll(input, "source='ingress'", "certificate='hp-auto-cert-reserved'")} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
