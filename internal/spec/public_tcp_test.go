package spec

import (
	"strings"
	"testing"
)

func tcpTestApp() Application {
	return Application{SchemaVersion: 1, Name: "mail", Services: map[string]Service{"smtp": {Image: "example.org/smtp:latest", Port: 2525, PublicTCP: []PublicTCPListener{{Port: 587, TargetPort: 2525, SourceCIDRs: []string{"192.0.2.9/24"}}}}}}
}
func TestPublicTCPNormalize(t *testing.T) {
	input := tcpTestApp()
	app, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if app.Services["smtp"].Public || app.Services["smtp"].PublicTCP[0].SourceCIDRs[0] != "192.0.2.0/24" {
		t.Fatal("public TCP changed HTTP exposure or did not canonicalize CIDR")
	}
	if input.Services["smtp"].PublicTCP[0].SourceCIDRs[0] != "192.0.2.9/24" {
		t.Fatal("normalization mutated input")
	}
	svc := app.Services["smtp"]
	svc.Ports = []Port{{Name: "submission", Protocol: "TCP", Port: 2526, TargetPort: 2527}}
	svc.PublicTCP[0].TargetPort = 2527
	if got := PublicTCPServicePort(svc, svc.PublicTCP[0]); got != 2526 {
		t.Fatalf("route targeted container rather than Service port: %d", got)
	}
}
func TestPublicTCPRejectsInvalidExposure(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Service)
	}{
		{"omitted source", func(s *Service) { s.PublicTCP[0].SourceCIDRs = nil }},
		{"invalid cidr", func(s *Service) { s.PublicTCP[0].SourceCIDRs = []string{"0.0.0.0/0\nallow all"} }},
		{"ipv6", func(s *Service) { s.PublicTCP[0].SourceCIDRs = []string{"::/0"} }},
		{"reserved port", func(s *Service) { s.PublicTCP[0].Port = 6443 }},
		{"invalid port", func(s *Service) { s.PublicTCP[0].Port = 65536 }},
		{"missing target", func(s *Service) { s.PublicTCP[0].TargetPort = 2526 }},
		{"UDP target", func(s *Service) { s.Port = 0; s.Ports = []Port{{Name: "udp", Port: 2525, Protocol: "UDP"}} }},
		{"duplicate cidr", func(s *Service) { s.PublicTCP[0].SourceCIDRs = []string{"192.0.2.0/24", "192.0.2.1/24"} }},
		{"duplicate port", func(s *Service) { s.PublicTCP = append(s.PublicTCP, s.PublicTCP[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := tcpTestApp()
			svc := app.Services["smtp"]
			test.change(&svc)
			app.Services["smtp"] = svc
			if _, err := Normalize(app); err == nil {
				t.Fatal("invalid public exposure accepted")
			}
		})
	}
	app := tcpTestApp()
	app.Services["other"] = app.Services["smtp"]
	if _, err := Normalize(app); err == nil {
		t.Fatal("cross-service duplicate public port accepted")
	}
}
func TestPublicTCPStrictTOMLAndDiff(t *testing.T) {
	app, err := Parse([]byte(`schema_version=1
name="smtp"
[services.mail]
image="example.org/mail:latest"
port=2525
[[services.mail.public_tcp]]
port=587
target_port=2525
source_cidrs=["0.0.0.0/0"]
`))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range Diff(nil, app) {
		if change.Field == "public_tcp" {
			found = true
		}
	}
	if !found {
		t.Fatal("public exposure missing from review")
	}
	_, err = Parse([]byte(`name="smtp"
[services.mail]
image="example.org/mail:latest"
port=2525
[[services.mail.public_tcp]]
port=587
target_port=2525
source_cidrs=["0.0.0.0/0"]
ssl=true
`))
	if err == nil || !strings.Contains(err.Error(), "ssl") {
		t.Fatal("unsupported TCP TLS termination accepted")
	}
}

func TestPublicTCPRejectsSharedHTTPBackend(t *testing.T) {
	app := tcpTestApp()
	svc := app.Services["smtp"]
	svc.Public = true
	app.Services["smtp"] = svc
	if _, err := Normalize(app); err == nil {
		t.Fatal("HTTP and TCP backend mode conflict accepted")
	}
}

func TestPublicTCPRequiresReadinessPort(t *testing.T) {
	app := tcpTestApp()
	svc := app.Services["smtp"]
	svc.Port = 0
	svc.Ports = []Port{{Name: "submission", Port: 2525, Protocol: "TCP"}}
	app.Services["smtp"] = svc
	if _, err := Normalize(app); err == nil {
		t.Fatal("public TCP service without readiness accepted")
	}
}
