package spec

import "testing"

func TestValidateNodeProfileRequiresPinnedSingleton(t *testing.T) {
	ok := Service{NodeProfile: "kelvo-node", NodeName: "kelvo-1", Replicas: 1}
	if err := validateNodeProfile(ok); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Service{
		"no pin":   {NodeProfile: "kelvo-node", Replicas: 1},
		"replicas": {NodeProfile: "kelvo-node", NodeName: "kelvo-1", Replicas: 2},
		"bad name": {NodeProfile: "Kelvo Node", NodeName: "kelvo-1", Replicas: 1},
	} {
		if validateNodeProfile(s) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if validateNodeProfile(Service{}) != nil {
		t.Fatal("services without a profile must pass")
	}
}
