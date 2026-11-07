package store

import "testing"

func TestFinishKeepsObservationWithoutStatus(t *testing.T) {
	type observation struct {
		Status string `json:"status"`
	}
	for name, test := range map[string]struct {
		result any
		want   bool
	}{
		"empty observation": {observation{}, false},
		"empty pointer":     {&observation{}, false},
		"failed":            {observation{"failed"}, true},
		"cancelled map":     {map[string]any{"status": "cancelled"}, true},
		"nil":               {nil, false},
		"not an object":     {"text", false},
	} {
		if got := storableObservation(test.result); got != test.want {
			t.Errorf("%s: storable=%v, want %v", name, got, test.want)
		}
	}
}
