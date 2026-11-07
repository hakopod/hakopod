package spec

import (
	"testing"
	"time"
)

func TestTerminationGraceBoundsAndRecreate(t *testing.T) {
	for grace, ok := range map[int64]bool{0: true, 1: true, 300: true, 3600: true, 3601: false, -1: false} {
		err := validateMountControls(Service{TerminationGraceSeconds: grace})
		if (err == nil) != ok {
			t.Errorf("termination_grace_seconds %d: %v", grace, err)
		}
	}
	if TerminationGrace(Service{}) != 30*time.Second || TerminationGrace(Service{TerminationGraceSeconds: 900}) != 15*time.Minute {
		t.Fatal("termination grace default or value is wrong")
	}
	for name, s := range map[string]Service{
		"strategy":     {UpdateStrategy: "recreate"},
		"volume":       {Volume: &Volume{MountPath: "/data", SizeGiB: 1}},
		"mounts":       {Mounts: []Mount{{Volume: "data", MountPath: "/data"}}},
		"node profile": {NodeProfile: "gpu"},
	} {
		if !Recreates(s) {
			t.Errorf("%s does not recreate", name)
		}
	}
	if Recreates(Service{}) {
		t.Fatal("a rolling update was treated as recreate")
	}
}
