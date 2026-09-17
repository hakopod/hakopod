package spec

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"testing"
)

func TestProfileBudgetsIncreaseTwentyPercent(t *testing.T) {
	// Previous budgets in millicores and MiB. CPU stays exact; memory rounds up.
	previous := map[string][4]int64{
		"small": {100, 500, 128, 256}, "medium": {250, 1000, 256, 512},
		"large": {500, 2000, 512, 1024}, "compute": {1000, 4000, 2048, 4096},
	}
	for name, old := range previous {
		p := Profiles[name]
		for i, value := range []string{p.CPURequest, p.CPULimit, p.MemoryRequest, p.MemoryLimit} {
			q := resource.MustParse(value)
			actual := q.MilliValue()
			if i >= 2 {
				actual = q.Value() / (1 << 20)
				if q.Value()%(1<<20) != 0 {
					t.Fatalf("%s memory is not whole MiB", name)
				}
			}
			want := (old[i]*6 + 4) / 5
			if actual != want {
				t.Fatalf("%s field %d: got %d want %d", name, i, actual, want)
			}
		}
		app, err := Normalize(Application{Name: "profile", Services: map[string]Service{"api": {Image: "nginx:alpine", Size: name}}})
		if err != nil || EffectiveResources(app.Services["api"]) != p {
			t.Fatal(name, err)
		}
	}
}
