package spec

import (
	"strings"
	"testing"
)

func TestContainerDaemonTOMLAndDiff(t *testing.T) {
	base := "schema_version = 1\nname = 'labs'\n[services.broker]\nimage = 'python:3.13'\n"
	before, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse([]byte(base + "container_daemon = 'notebook-builder'\n"))
	if err != nil || after.Services["broker"].ContainerDaemon != "notebook-builder" {
		t.Fatal("daemon binding did not parse", err)
	}
	changes := Diff(&before, after)
	if len(changes) != 1 || changes[0].Field != "container_daemon" || changes[0].After != "notebook-builder" || changes[0].Sensitive {
		t.Fatalf("daemon permission change not reviewable: %+v", changes)
	}
	for _, bad := range []string{"tcp://10.0.0.1:2376", "../admin", "*", "Builder", strings.Repeat("a", 64)} {
		if _, err := Parse([]byte(base + "container_daemon = '" + bad + "'\n")); err == nil {
			t.Fatalf("unscoped daemon reference accepted: %s", bad)
		}
	}
	if !HasDeliveryCapabilities(after) {
		t.Fatal("a named daemon binding must reach operator and cluster validation")
	}
}

func TestContainerDaemonClientOverridesRejected(t *testing.T) {
	for _, key := range []string{"DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_CONFIG", "DOCKER_API_VERSION", "BUILDKIT_HOST", "docker_host"} {
		for _, where := range []string{"env", "secrets", "bindings"} {
			s := Service{ContainerDaemon: "builder"}
			switch where {
			case "env":
				s.Env = map[string]string{key: "tcp://attacker:2376"}
			case "secrets":
				s.Secrets = map[string]SecretRef{key: {Ref: "other"}}
			case "bindings":
				s.Bindings = map[string]Binding{key: {Service: "other"}}
			}
			if validateContainerDaemon(s) == nil {
				t.Fatalf("client override accepted: %s in %s", key, where)
			}
		}
	}
	if err := validateContainerDaemon(Service{ContainerDaemon: "builder", Env: map[string]string{"DOCKER_BUILDKIT": "1", "APP_NAME": "broker"}}); err != nil {
		t.Fatal("unrelated settings rejected", err)
	}
}

func TestContainerDaemonReservedMounts(t *testing.T) {
	for _, path := range []string{ContainerDaemonDirectory, ContainerDaemonDirectory + "/client.pem", "/var/run", "/var/run/secrets/hakopod"} {
		for _, kind := range []string{"volume", "mounts", "certificate_mounts", "temporary_mounts"} {
			s := Service{ContainerDaemon: "builder"}
			switch kind {
			case "volume":
				s.Volume = &Volume{MountPath: path}
			case "mounts":
				s.Mounts = []Mount{{MountPath: path}}
			case "certificate_mounts":
				s.CertificateMounts = []CertificateMount{{MountPath: path}}
			case "temporary_mounts":
				s.TemporaryMounts = []TemporaryMount{{MountPath: path}}
			}
			if validateContainerDaemon(s) == nil {
				t.Fatalf("trust material could be hidden by a %s at %s", kind, path)
			}
		}
	}
	if err := validateContainerDaemon(Service{ContainerDaemon: "builder", Mounts: []Mount{{MountPath: "/data"}}}); err != nil {
		t.Fatal("unrelated mount rejected", err)
	}
}

func TestContainerDaemonAbsentIsUnaffected(t *testing.T) {
	s := Service{
		Env:               map[string]string{"DOCKER_HOST": "tcp://local:2375"},
		Secrets:           map[string]SecretRef{"DOCKER_CONFIG": {Ref: "other"}},
		Bindings:          map[string]Binding{"BUILDKIT_HOST": {Service: "other"}},
		TemporaryMounts:   []TemporaryMount{{MountPath: ContainerDaemonDirectory}},
		CertificateMounts: []CertificateMount{{MountPath: "/var/run"}},
	}
	if err := validateContainerDaemon(s); err != nil {
		t.Fatal("a service without the field must be unaffected", err)
	}
	if HasDeliveryCapabilities(Application{Services: map[string]Service{"web": {}}}) {
		t.Fatal("a plain service must not need delivery validation")
	}
}

func TestContainerDaemonRefusedOutsideItsScope(t *testing.T) {
	app := Application{SchemaVersion: 1, Name: "labs", Services: map[string]Service{
		"broker": {Image: "python:3.13", Size: "small", Replicas: 1, ContainerDaemon: "builder"},
	}}
	if err := ValidatePreview(app); err == nil {
		t.Fatal("a preview inherited a daemon binding")
	}
	job := Service{Replicas: 1, Job: &Job{}, ContainerDaemon: "builder"}
	if err := normalizeJobAndFiles(&job); err == nil {
		t.Fatal("a job kept a daemon binding")
	}
	sl := Service{Public: true, Port: 8080, Replicas: 1, Serverless: &Serverless{}, ContainerDaemon: "builder"}
	if err := normalizeServerless(&sl); err == nil {
		t.Fatal("a serverless service kept a daemon binding")
	}
	pool := Service{Actions: &Actions{}, ContainerDaemon: "builder"}
	if err := normalizeActions(&pool); err == nil {
		t.Fatal("an actions pool kept a daemon binding")
	}
}

func TestContainerDaemonBlocksServiceTransfer(t *testing.T) {
	source, destination := transferFixture("source"), transferFixture("destination")
	svc := source.Services["api"]
	svc.ContainerDaemon = "builder"
	source.Services["api"] = svc
	if _, _, err := MoveService(source, destination, "api", "worker"); err == nil {
		t.Fatal("a daemon grant followed a transferred service")
	}
}
