package spec

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiffServiceSuspension(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool
	}{
		{"suspend", false, true},
		{"resume", true, false},
		{"remain_active", false, false},
		{"remain_suspended", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := Parse([]byte(minimum))
			if err != nil {
				t.Fatal(err)
			}
			service := before.Services["web"]
			service.Suspended = tc.before
			before.Services["web"] = service
			after, err := Normalize(before)
			if err != nil {
				t.Fatal(err)
			}
			service.Suspended = tc.after
			after.Services["web"] = service

			want := []Change{}
			if tc.before != tc.after {
				want = append(want, Change{Service: "web", Field: "suspended", Before: tc.before, After: tc.after})
			}
			if got := Diff(&before, after); !reflect.DeepEqual(got, want) {
				t.Fatalf("suspension diff: got %#v, want %#v", got, want)
			}
			if before.Services["web"].Suspended != tc.before {
				t.Fatal("planning changed the previous service suspension state")
			}
		})
	}
}

func TestDiffServiceResumeJSON(t *testing.T) {
	for _, tc := range []struct{ name, field string }{
		{"explicit_false", `,"suspended":false`},
		{"omitted_false", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after Application
			if err := json.Unmarshal([]byte(`{"name":"demo","services":{"web":{"image":"nginx","suspended":true}}}`), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(`{"name":"demo","services":{"web":{"image":"nginx"`+tc.field+`}}}`), &after); err != nil {
				t.Fatal(err)
			}
			changes := Diff(&before, after)
			want := []Change{{Service: "web", Field: "suspended", Before: true, After: false}}
			if !reflect.DeepEqual(changes, want) {
				t.Fatalf("resume diff: got %#v, want %#v", changes, want)
			}
			encoded, err := json.Marshal(changes)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded) != 1 || decoded[0]["before"] != true || decoded[0]["after"] != false {
				t.Fatalf("plan response did not retain the true-to-false change: %s", encoded)
			}
		})
	}
}
