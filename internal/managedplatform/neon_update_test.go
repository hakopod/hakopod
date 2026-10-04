package managedplatform

import (
	"encoding/json"
	"testing"
)

func TestNeonResourceUpdatePreservesStorageAndMembership(t *testing.T) {
	previous := neonCandidateSpec()
	clone := func() Spec {
		raw, err := json.Marshal(previous)
		if err != nil {
			t.Fatal(err)
		}
		var next Spec
		if err = json.Unmarshal(raw, &next); err != nil {
			t.Fatal(err)
		}
		return next
	}
	next := clone()
	next.Resources["compute"] = Resources{CPU: "600m", Memory: "3Gi"}
	if err := ValidateNeonResourceUpdate(previous, next); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Spec){
		"compute count":    func(s *Spec) { s.Neon.ComputeReplicas++ },
		"pageserver count": func(s *Spec) { s.Neon.Pageservers++ },
		"placement":        func(s *Spec) { s.Placement.NodeNames[0] = "other-node" },
		"volume size":      func(s *Spec) { s.Storage["pageserver"]++ },
		"object storage":   func(s *Spec) { s.Neon.ObjectStoragePrefix = "another" },
		"secret revision":  func(s *Spec) { ref := s.Secrets["compute-auth"]; ref.Revision++; s.Secrets["compute-auth"] = ref },
		"TLS ownership":    func(s *Spec) { s.TLSMode = "managed" },
		"version":          func(s *Spec) { s.Version = "another" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := clone()
			change(&candidate)
			if err := ValidateNeonResourceUpdate(previous, candidate); err == nil {
				t.Fatal("unsupported update accepted")
			}
		})
	}
}
