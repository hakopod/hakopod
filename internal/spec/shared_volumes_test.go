package spec

import (
	"reflect"
	"strings"
	"testing"
)

func sharedReadWriteOnceFixture() Application {
	return Application{Name: "shared", Volumes: map[string]NamedVolume{
		"first": {SizeGiB: 1}, "second": {SizeGiB: 1}, "shared": {SizeGiB: 1, AccessMode: "ReadWriteMany", StorageClass: "nfs"},
	}, Services: map[string]Service{
		"writer": {Image: "busybox:1", Mounts: []Mount{{Volume: "first", MountPath: "/first"}}},
		"bridge": {Image: "busybox:1", Mounts: []Mount{{Volume: "first", MountPath: "/first"}, {Volume: "second", MountPath: "/second"}, {Volume: "shared", MountPath: "/shared"}}},
		"reader": {Image: "busybox:1", Mounts: []Mount{{Volume: "second", MountPath: "/second", ReadOnly: true}}},
		"remote": {Image: "busybox:1", Mounts: []Mount{{Volume: "shared", MountPath: "/shared"}}},
	}}
}

func TestSharedReadWriteOnceGroupsAreTransitiveAndExcludeRWX(t *testing.T) {
	app := sharedReadWriteOnceFixture()
	svc := app.Services["writer"]
	svc.NodeName, svc.Architecture = "storage-node", "arm64"
	app.Services["writer"] = svc
	app, err := Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	wanted := [][]string{{"bridge", "reader", "writer"}}
	if got := SharedReadWriteOnceGroups(app); !reflect.DeepEqual(got, wanted) {
		t.Fatalf("groups = %v, want %v", got, wanted)
	}
	for _, name := range wanted[0] {
		if app.Services[name].NodeName != "storage-node" || app.Services[name].Architecture != "arm64" || app.Services[name].Replicas != 1 {
			t.Fatalf("placement or replicas diverged for %s", name)
		}
	}
	if app.Services["remote"].NodeName != "" || app.Services["remote"].Architecture != "" {
		t.Fatal("ReadWriteMany peer inherited local placement")
	}
}

func TestSharedReadWriteOnceRejectsIncompatibleConsumers(t *testing.T) {
	cases := map[string]struct {
		change func(*Application)
		want   string
	}{
		"nodes": {func(app *Application) {
			first, second := app.Services["writer"], app.Services["reader"]
			first.NodeName, second.NodeName = "first", "second"
			app.Services["writer"], app.Services["reader"] = first, second
		}, "same node"},
		"architecture": {func(app *Application) {
			first, second := app.Services["writer"], app.Services["reader"]
			first.Architecture, second.Architecture = "amd64", "arm64"
			app.Services["writer"], app.Services["reader"] = first, second
		}, "same architecture"},
		"job": {func(app *Application) {
			svc := app.Services["reader"]
			svc.Job = &Job{TimeoutSeconds: 30}
			app.Services["reader"] = svc
		}, "jobs and Managed Actions"},
		"replicas": {func(app *Application) {
			svc := app.Services["reader"]
			svc.Replicas = 2
			app.Services["reader"] = svc
		}, "one replica"},
		"filesystem-group": {func(app *Application) {
			svc := app.Services["reader"]
			svc.FSGroup = 23456
			app.Services["reader"] = svc
		}, "same fs_group"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app := sharedReadWriteOnceFixture()
			tc.change(&app)
			if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
