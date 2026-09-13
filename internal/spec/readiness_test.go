package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestReadinessStrictRoundTripAndDiff(t *testing.T) {
	input := []byte(`schema_version=1
name="mail"
[services.smtp]
image="example.org/mail:latest"
port=8080
healthcheck="/health"
ports=[{name="smtp",port=2525,target_port=2525,protocol="TCP"}]
[services.smtp.readiness]
protocol="smtp_starttls"
port=2525
tls_server_name="smtp.example.com"
tls_ca_file="/certificates/smtp/tls.crt"
`)
	app, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	r := app.Services["smtp"].Readiness
	if r.TimeoutSeconds != 3 || r.PeriodSeconds != 5 || r.FailureThreshold != 3 || !NeedsReadinessHelper(app.Services["smtp"]) {
		t.Fatalf("unexpected normalized readiness: %+v", r)
	}
	data, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := Parse(data)
	if err != nil || !reflect.DeepEqual(roundTrip, app) {
		t.Fatalf("TOML readiness round trip: %v", err)
	}
	data, _ = json.Marshal(app)
	if err := json.Unmarshal(data, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, app) {
		t.Fatalf("JSON readiness round trip: %v", err)
	}
	if _, err := Parse(append(input, []byte("insecure_skip_verify=true\n")...)); err == nil {
		t.Fatal("accepted unsupported TLS bypass")
	}
	before := app
	after, _ := Normalize(app)
	after.Services["smtp"].Readiness.Port = 2526
	changes := Diff(&before, after)
	if len(changes) != 1 || changes[0].Field != "readiness" || before.Services["smtp"].Readiness.Port != 2525 {
		t.Fatalf("readiness diff or immutability failed: %+v", changes)
	}
}

func TestReadinessValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readiness Readiness
		valid     bool
	}{
		{"tcp", Readiness{Protocol: "tcp", Port: 2525}, true},
		{"smtp", Readiness{Protocol: "smtp", Port: 2525}, true},
		{"starttls", Readiness{Protocol: "smtp_starttls", Port: 2525, TLSServerName: "mail.example.com"}, true},
		{"missing-port", Readiness{Protocol: "tcp"}, false},
		{"undeclared-port", Readiness{Protocol: "tcp", Port: 2526}, false},
		{"unsupported", Readiness{Protocol: "exec", Port: 2525}, false},
		{"missing-hostname", Readiness{Protocol: "smtp_starttls", Port: 2525}, false},
		{"tls-on-plaintext", Readiness{Protocol: "smtp", Port: 2525, TLSServerName: "mail.example.com"}, false},
		{"bad-ca", Readiness{Protocol: "smtp_starttls", Port: 2525, TLSServerName: "mail.example.com", TLSCAFile: "../ca"}, false},
		{"long-timeout", Readiness{Protocol: "smtp", Port: 2525, TimeoutSeconds: 11}, false},
		{"overlap", Readiness{Protocol: "smtp", Port: 2525, TimeoutSeconds: 5, PeriodSeconds: 5}, false},
		{"rapid-period", Readiness{Protocol: "smtp", Port: 2525, PeriodSeconds: 1}, false},
		{"huge-threshold", Readiness{Protocol: "smtp", Port: 2525, FailureThreshold: 11}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tcpTestApp()
			svc := app.Services["smtp"]
			svc.Readiness = &tc.readiness
			app.Services["smtp"] = svc
			_, err := Normalize(app)
			if (err == nil) != tc.valid || err != nil && !strings.Contains(err.Error(), "readiness") {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestUpdateStrategyRoundTripAndDiff(t *testing.T) {
	for _, strategy := range []string{"", "rolling", "recreate", "other"} {
		input := tcpTestApp()
		svc := input.Services["smtp"]
		svc.UpdateStrategy = strategy
		input.Services["smtp"] = svc
		app, err := Normalize(input)
		if strategy == "other" {
			if err == nil {
				t.Fatal("unsupported update strategy accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := toml.Marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := Parse(data)
		if err != nil || restored.Services["smtp"].UpdateStrategy != strategy {
			t.Fatalf("update strategy round trip: %v", err)
		}
		if strategy == "recreate" {
			before := tcpTestApp()
			changes := Diff(&before, input)
			if len(changes) != 1 || changes[0].Field != "update_strategy" {
				t.Fatalf("strategy diff: %+v", changes)
			}
		}
	}
}

func TestReadinessUsesNormalizedPrivatePorts(t *testing.T) {
	for _, protocol := range []string{"", "tcp", "TCP"} {
		app, err := Normalize(Application{Name: "mail", Services: map[string]Service{"smtp": {Image: "example/smtp:1", Ports: []Port{{Name: "smtp", Port: 2525, Protocol: protocol}}, Readiness: &Readiness{Protocol: "smtp", Port: 2525}}}})
		if err != nil {
			t.Fatal("private port defaults rejected by readiness", err)
		}
		if app.Services["smtp"].Ports[0].TargetPort != 2525 || app.Services["smtp"].Ports[0].Protocol != "TCP" {
			t.Fatal("private ports were not normalized")
		}
	}
}
