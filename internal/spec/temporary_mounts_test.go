package spec

import (
	"strings"
	"testing"
)

func TestTemporaryMountSizes(t *testing.T) {
	base := "schema_version = 1\nname = \"scratch\"\n[services.api]\nimage = \"python:3.13-alpine\"\nsize = \"small\"\n"
	for name, c := range map[string]struct {
		mounts string
		ok     bool
	}{
		"large disk":          {`temporary_mounts = [{mount_path = "/tmp", size_mib = 20480}]`, true},
		"over the sanity cap": {`temporary_mounts = [{mount_path = "/tmp", size_mib = 1048577}]`, false},
		"tmpfs under memory":  {`temporary_mounts = [{mount_path = "/tmp", size_mib = 256, memory = true}]`, true},
		"tmpfs fills memory":  {`temporary_mounts = [{mount_path = "/tmp", size_mib = 65536, memory = true}]`, false},
	} {
		_, err := Parse([]byte(base + c.mounts + "\n"))
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "temporary_mounts") {
			t.Errorf("%s: unrelated error %v", name, err)
		}
	}
	s := Service{TemporaryMounts: []TemporaryMount{{SizeMiB: 300}, {SizeMiB: 50, Memory: true}}}
	if TemporaryDiskMiB(s) != 300 {
		t.Fatal("memory-backed mounts counted as disk")
	}
}
