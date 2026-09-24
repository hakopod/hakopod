package spec

import (
	"encoding/json"
	"testing"
)

func TestVolumeResizePreservesMountsAndOriginal(t *testing.T) {
	source, err := Normalize(Application{Name: "resize-test", Services: map[string]Service{
		"db":  {Image: "postgres:17", Port: 5432, Volume: &Volume{SizeGiB: 10, MountPath: "/data"}},
		"api": {Image: "nginx:stable", Port: 8080},
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(source)
	for _, size := range []int64{5, 20} {
		next, p, err := ResizeVolume(source, "db-data", size, "operation-123")
		if err != nil {
			t.Fatal(err)
		}
		if next.Services["db"].Volume != nil || len(next.Services["db"].Mounts) != 1 || next.Services["db"].Mounts[0].SubPath != "data" || next.Services["db"].Mounts[0].MountPath != "/data" {
			t.Fatal("mount was not safely redirected")
		}
		if next.Volumes[p.TargetName].SizeGiB != size || HasVolumeClaim(next, p.Claim) || !HasVolumeClaim(next, p.TargetClaim) {
			t.Fatal("incorrect volume replacement")
		}
		if next.Services["api"].Image != source.Services["api"].Image || len(p.Services) != 1 || p.Services[0] != "db" {
			t.Fatal("unrelated service changed")
		}
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source spec mutated")
	}
	for _, size := range []int64{0, 10, 201} {
		if _, _, err := ResizeVolume(source, "db-data", size, "id"); err == nil {
			t.Fatal("invalid size accepted", size)
		}
	}
	if _, _, err := ResizeVolume(source, "foreign", 5, "id"); err == nil {
		t.Fatal("unattached volume accepted")
	}
}
func TestVolumeResizeIncludesSharedReaders(t *testing.T) {
	source, err := Normalize(Application{Name: "shared", Volumes: map[string]NamedVolume{"shared": {SizeGiB: 10, AccessMode: "ReadWriteMany", StorageClass: "shared-nfs"}}, Services: map[string]Service{
		"writer": {Image: "nginx:stable", Port: 8080, Mounts: []Mount{{Volume: "shared", MountPath: "/data", SubPath: "writer"}}},
		"reader": {Image: "nginx:stable", Port: 8080, Mounts: []Mount{{Volume: "shared", MountPath: "/read", ReadOnly: true, SubPath: "writer"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	next, p, err := ResizeVolume(source, "hakopod-volume-shared", 5, "id")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Services) != 2 || next.Services["reader"].Mounts[0].SubPath != "data/writer" || !next.Services["reader"].Mounts[0].ReadOnly {
		t.Fatal("shared reader or subdirectory lost")
	}
	reader := source.Services["reader"]
	reader.RunAsUser = 123
	source.Services["reader"] = reader
	if _, _, err = ResizeVolume(source, "hakopod-volume-shared", 5, "id"); err == nil {
		t.Fatal("incompatible filesystem identities accepted")
	}
}
