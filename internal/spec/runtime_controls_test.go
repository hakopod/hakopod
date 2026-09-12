package spec

import (
	"reflect"
	"strings"
	"testing"
)

func controlsFixture(t *testing.T) Application {
	t.Helper()
	a, err := Parse([]byte(`schema_version = 1
name = "controls"
[volumes.data]
size_gib = 1
[networks.private]
internal = true
[services.api]
image = "python:3.13-alpine"
port = 8080
networks = ["private"]
ports = [{name = "metrics", port = 9090, target_port = 8081}, {name = "discovery", port = 5353, protocol = "UDP"}]
run_as_user = 12345
run_as_group = 23456
fs_group = 23456
read_only_root_filesystem = true
working_dir = "/data"
mounts = [{volume = "data", mount_path = "/data"}, {volume = "data", mount_path = "/readback", read_only = true}]
temporary_mounts = [{mount_path = "/tmp", size_mib = 16, memory = true}]
[services.api.network_access]
from = ["client"]
[services.client]
image = "python:3.13-alpine"
networks = ["private"]
[services.stranger]
image = "python:3.13-alpine"
networks = ["private"]
`))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRuntimeControlsNormalizeAndRoundTrip(t *testing.T) {
	a := controlsFixture(t)
	if !AllowsPeer(a, "client", "api") || AllowsPeer(a, "stranger", "api") || AllowsPeer(a, "api", "api") {
		t.Fatal("service allowlist not enforced")
	}
	if a.Volumes["data"].AccessMode != "ReadWriteOnce" {
		t.Fatal("missing storage default")
	}
	b, err := Normalize(a)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("normalization is not stable", err)
	}
	api := b.Services["api"]
	api.NetworkAccess.From = []string{}
	b.Services["api"] = api
	b, err = Normalize(b)
	if err != nil || AllowsPeer(b, "client", "api") {
		t.Fatal("empty deny list became unrestricted", err)
	}
	if !AllowsPeer(a, "client", "api") {
		t.Fatal("normalization changed immutable input")
	}
	changed := map[string]bool{}
	for _, c := range Diff(&a, b) {
		changed[c.Field] = true
	}
	if !changed["network_access"] {
		t.Fatal("network access not reviewable in deployment plan")
	}
}

func TestRuntimeControlsRejectUnsafeOrUnboundedValues(t *testing.T) {
	cases := map[string]func(*Application){
		"undeclared-volume": func(a *Application) { delete(a.Volumes, "data") },
		"host-directory":    func(a *Application) { s := a.Services["api"]; s.Mounts[0].MountPath = "/proc"; a.Services["api"] = s },
		"traversal":         func(a *Application) { s := a.Services["api"]; s.Mounts[0].SubPath = "../secret"; a.Services["api"] = s },
		"overlap": func(a *Application) {
			s := a.Services["api"]
			s.Mounts[1].MountPath = "/data/nested"
			a.Services["api"] = s
		},
		"nonadjacent-overlap": func(a *Application) {
			s := a.Services["api"]
			s.Mounts = []Mount{{Volume: "data", MountPath: "/data"}, {Volume: "data", MountPath: "/data-backups"}, {Volume: "data", MountPath: "/data/cache"}}
			a.Services["api"] = s
		},
		"claim-name-collision": func(a *Application) {
			a.Volumes["foo-data"] = NamedVolume{SizeGiB: 1}
			a.Services["hakopod-volume-foo"] = Service{Image: "python:3.13-alpine", Volume: &Volume{MountPath: "/data", SizeGiB: 1}}
		},
		"unbounded-temporary-memory": func(a *Application) {
			s := a.Services["api"]
			s.TemporaryMounts[0].SizeMiB = 129
			a.Services["api"] = s
		},
		"invalid-group":   func(a *Application) { s := a.Services["api"]; s.RunAsGroup = -1; a.Services["api"] = s },
		"bad-working-dir": func(a *Application) { s := a.Services["api"]; s.WorkingDir = "relative"; a.Services["api"] = s },
		"excessive-grace": func(a *Application) { s := a.Services["api"]; s.TerminationGraceSeconds = 301; a.Services["api"] = s },
		"unknown-peer": func(a *Application) {
			s := a.Services["api"]
			s.NetworkAccess.From = []string{"unknown"}
			a.Services["api"] = s
		},
		"disjoint-peer": func(a *Application) {
			s := a.Services["client"]
			s.Networks = []string{"default"}
			a.Services["client"] = s
		},
		"duplicate-port": func(a *Application) {
			s := a.Services["api"]
			s.Ports[0].Port = 8080
			s.Ports[0].Protocol = "TCP"
			a.Services["api"] = s
		},
		"invalid-protocol": func(a *Application) { s := a.Services["api"]; s.Ports[0].Protocol = "SCTP"; a.Services["api"] = s },
		"shared-rwo": func(a *Application) {
			s := a.Services["client"]
			s.Mounts = []Mount{{Volume: "data", MountPath: "/data"}}
			a.Services["client"] = s
		},
		"shared-without-driver": func(a *Application) { v := a.Volumes["data"]; v.AccessMode = "ReadWriteMany"; a.Volumes["data"] = v },
		"persistent-replicas":   func(a *Application) { s := a.Services["api"]; s.Replicas = 2; a.Services["api"] = s },
		"total-quota":           func(a *Application) { a.Volumes["one"] = NamedVolume{SizeGiB: 200} },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			a := controlsFixture(t)
			modify(&a)
			if _, err := Normalize(a); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	if _, err := Parse([]byte("name='controls'\n[services.main]\nimage='python:3.13'\nprivileged=true")); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal("privileged control accepted")
	}
}

func TestSharedVolumeRequiresCompatibleGroups(t *testing.T) {
	a := controlsFixture(t)
	a.Volumes["data"] = NamedVolume{SizeGiB: 1, StorageClass: "shared-csi", AccessMode: "ReadWriteMany"}
	s := a.Services["client"]
	s.Mounts = []Mount{{Volume: "data", MountPath: "/data", ReadOnly: true}}
	s.FSGroup = 23456
	a.Services["client"] = s
	if _, err := Normalize(a); err != nil {
		t.Fatal(err)
	}
	s.FSGroup = 11111
	a.Services["client"] = s
	if _, err := Normalize(a); err == nil {
		t.Fatal("shared volume ownership can oscillate between services")
	}
}
