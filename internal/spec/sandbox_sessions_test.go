package spec

import (
	"reflect"
	"strings"
	"testing"
)

func sessionSpecFixture() Application {
	return Application{Name: "sessions", Services: map[string]Service{"worker": {Image: "example/worker@sha256:" + strings.Repeat("a", 64), Command: []string{"python", "-I", "-m", "worker"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "sandbox", Session: &SandboxSession{AllowedIdentities: []string{strings.Repeat("a", 32)}, ReadyCommand: []string{"ready"}, HelperCommand: []string{"python", "-I", "-m", "helper"}}}}}
}
func TestSessionSpecificationIsStableAndRejectsAmbientAuthority(t *testing.T) {
	original := sessionSpecFixture()
	first, err := Normalize(original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(first)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("session normalization changed frozen source", err)
	}
	if len(first.Services["worker"].Networks) != 0 || first.Services["worker"].Session.IdleSeconds != 900 || first.Services["worker"].Session.CallTimeoutSeconds != 150 {
		t.Fatal("session gained network access or invalid defaults")
	}
	for name, change := range map[string]func(*Service){
		"network":            func(s *Service) { s.Networks = []string{"default"} },
		"host-daemon":        func(s *Service) { s.ContainerDaemon = "daemon" },
		"secret":             func(s *Service) { s.Secrets = map[string]SecretRef{"TOKEN": {Provider: "fixture", Key: "token"}} },
		"port":               func(s *Service) { s.Port = 8080 },
		"mutable-image":      func(s *Service) { s.Image = "example/worker:latest" },
		"unbounded-lifetime": func(s *Service) { s.Session.LifetimeSeconds = 3601 },
		"wildcard-identity":  func(s *Service) { s.Session.AllowedIdentities = []string{"*"} },
		"missing-ready":      func(s *Service) { s.Session.ReadyCommand = nil },
		"missing-helper":     func(s *Service) { s.Session.HelperCommand = nil },
		"root":               func(s *Service) { s.RunAsUser = 0 },
		"ordinary-runtime":   func(s *Service) { s.RuntimeProfile = "" },
		"reserved-env":       func(s *Service) { s.Env = map[string]string{"HAKOPOD_POD_UID": "other"} },
		"disk-temporary":     func(s *Service) { s.TemporaryMounts = []TemporaryMount{{MountPath: "/workspace", SizeMiB: 32}} },
		"guard-overlap": func(s *Service) {
			s.TemporaryMounts = []TemporaryMount{{MountPath: "/run/hakopod-session", SizeMiB: 32, Memory: true}}
		},
		"guard-parent":      func(s *Service) { s.TemporaryMounts = []TemporaryMount{{MountPath: "/run", SizeMiB: 32, Memory: true}} },
		"persistent-volume": func(s *Service) { s.Volume = &Volume{MountPath: "/data", SizeGiB: 1} },
		"short-call":        func(s *Service) { s.Session.CallTimeoutSeconds = 9 },
		"call-beyond-idle":  func(s *Service) { s.Session.IdleSeconds = 300; s.Session.CallTimeoutSeconds = 301 },
	} {
		t.Run(name, func(t *testing.T) {
			app := sessionSpecFixture()
			svc := app.Services["worker"]
			change(&svc)
			app.Services["worker"] = svc
			if _, err := Normalize(app); err == nil {
				t.Fatal("unsafe session accepted")
			}
		})
	}
	app := sessionSpecFixture()
	app.InjectEnv = true
	if _, err := Normalize(app); err == nil {
		t.Fatal("injected application environment accepted")
	}
}

func TestSessionCannotBeAServiceDependency(t *testing.T) {
	app := sessionSpecFixture()
	app.Services["api"] = Service{Image: "example/api:1", DependsOn: []string{"worker"}}
	if _, err := Normalize(app); err == nil {
		t.Fatal("service dependency accepted a session template")
	}
}

func TestSessionCallTimeoutIsBoundedByIdleDeadline(t *testing.T) {
	app := sessionSpecFixture()
	svc := app.Services["worker"]
	svc.Session.CallTimeoutSeconds = 900
	app.Services["worker"] = svc
	got, err := Normalize(app)
	if err != nil || got.Services["worker"].Session.CallTimeoutSeconds != 900 {
		t.Fatal("call timeout at the idle limit rejected", err)
	}
}
