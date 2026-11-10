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
		"disk-temporary-big": func(s *Service) { s.TemporaryMounts = []TemporaryMount{{MountPath: "/workspace", SizeMiB: 16385}} },
		"memory-temporary-big": func(s *Service) {
			s.TemporaryMounts = []TemporaryMount{{MountPath: "/a", SizeMiB: 100, Memory: true}, {MountPath: "/b", SizeMiB: 29, Memory: true}}
		},
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

func TestSessionEgressProxyAndDiskScratch(t *testing.T) {
	withProxy := func(proxy Service, name string) Application {
		app := sessionSpecFixture()
		app.Services["proxy"] = proxy
		worker := app.Services["worker"]
		session := *worker.Session
		session.EgressProxy = name
		worker.Session = &session
		worker.TemporaryMounts = []TemporaryMount{{MountPath: "/workspace", SizeMiB: 16000}, {MountPath: "/tmp", SizeMiB: 128, Memory: true}}
		app.Services["worker"] = worker
		return app
	}
	proxy := Service{Image: "example/proxy:1", Port: 3128}
	got, err := Normalize(withProxy(proxy, "proxy"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Services["worker"].Session.EgressProxy != "proxy" {
		t.Fatal("egress_proxy lost")
	}
	again, err := Normalize(got)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("egress_proxy is not stable", err)
	}
	session := sessionSpecFixture().Services["worker"]
	for name, app := range map[string]Application{
		"missing": withProxy(proxy, "nope"),
		"session": withProxy(proxy, "worker"),
		"public":  withProxy(Service{Image: "example/proxy:1", Port: 3128, Public: true}, "proxy"),
		"no-port": withProxy(Service{Image: "example/proxy:1"}, "proxy"),
		"other-session": func() Application {
			a := withProxy(proxy, "proxy")
			a.Services["proxy"] = session
			return a
		}(),
	} {
		if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "egress_proxy") {
			t.Errorf("%s: accepted or wrong error: %v", name, err)
		}
	}
}

func TestSessionEgressProxyMustBePlain(t *testing.T) {
	for name, change := range map[string]func(*Application, *Service){
		"container-daemon": func(_ *Application, p *Service) { p.ContainerDaemon = "daemon" },
		"private-egress":   func(_ *Application, p *Service) { p.PrivateEgress = []string{"corp"} },
		"managed-db": func(_ *Application, p *Service) {
			p.Bindings = map[string]Binding{"DATABASE_URL": {ManagedDatabase: "db"}}
		},
		"public-tcp":       func(_ *Application, p *Service) { p.PublicTCP = []PublicTCPListener{{}} },
		"http":             func(_ *Application, p *Service) { p.HTTP = map[string]HTTPEndpoint{"web": {}} },
		"tls":              func(_ *Application, p *Service) { p.TLS = &TLSConfig{} },
		"custom-hostnames": func(_ *Application, p *Service) { p.CustomHostnames = &CustomHostnames{} },
		"kubernetes":       func(_ *Application, p *Service) { p.Kubernetes = &KubernetesPatches{} },
		"serverless":       func(_ *Application, p *Service) { p.Serverless = &Serverless{} },
		"actions":          func(_ *Application, p *Service) { p.Actions = &Actions{} },
		"internal-network": func(a *Application, p *Service) {
			a.Networks = map[string]Network{"back": {Internal: true}}
			p.Networks = []string{"back"}
		},
		"virtual-network": func(a *Application, p *Service) {
			a.Networks = map[string]Network{"vn": {VirtualNetwork: "corp", Segment: "a"}}
			p.Networks = []string{"vn"}
		},
	} {
		app := sessionSpecFixture()
		proxy := Service{Image: "example/proxy:1", Port: 3128}
		change(&app, &proxy)
		if err := plainEgressProxy(app, proxy); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	app := sessionSpecFixture()
	app.Networks = map[string]Network{"edge": {}}
	if err := plainEgressProxy(app, Service{Image: "example/proxy:1", Port: 3128, Networks: []string{"edge"}}); err != nil {
		t.Fatal("plain proxy rejected", err)
	}
}
