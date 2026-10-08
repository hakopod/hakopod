package spec

import (
	"strings"
	"testing"
)

func TestRuntimeProfileIsReviewedAndValidated(t *testing.T) {
	base := "schema_version = 1\nname = 'analytics'\n[services.worker]\nimage = 'busybox:1.37'\n"
	before, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse([]byte(base + "runtime_profile = 'bounded-worker'\n"))
	if err != nil {
		t.Fatal(err)
	}
	changes := Diff(&before, after)
	if len(changes) != 1 || changes[0].Field != "runtime_profile" || changes[0].After != "bounded-worker" || changes[0].Sensitive {
		t.Fatal("the runtime selection is missing from the review")
	}
	if !HasDeliveryCapabilities(after) {
		t.Fatal("the runtime selection bypasses delivery validation")
	}
	for _, value := range []string{"*", "../other", "Other", "runtime.example", strings.Repeat("a", 64)} {
		if _, err := Parse([]byte(base + "runtime_profile = '" + value + "'\n")); err == nil {
			t.Fatalf("invalid profile alias was accepted: %q", value)
		}
	}
	if err := ValidatePreview(after); err == nil {
		t.Fatal("a preview inherited the runtime grant")
	}
	for _, svc := range []Service{{RuntimeProfile: "bounded-worker", Actions: &Actions{}}, {RuntimeProfile: "bounded-worker", Serverless: &Serverless{}}} {
		if validateRuntimeProfile(svc) == nil {
			t.Fatal("a managed runtime accepted an application runtime override")
		}
	}
	if _, err := Parse([]byte(base + "runtime_profile = 'bounded-worker'\n[services.worker.job]\ntimeout_seconds = 30\n")); err != nil {
		t.Fatal("a bounded one-time job rejected a runtime profile", err)
	}
}

func TestRuntimeProfileCannotFollowAServiceTransfer(t *testing.T) {
	source, destination := transferFixture("source"), transferFixture("destination")
	svc := source.Services["api"]
	svc.RuntimeProfile = "bounded-worker"
	source.Services["api"] = svc
	if _, _, err := MoveService(source, destination, "api", "worker"); err == nil {
		t.Fatal("a runtime grant followed a service to another application")
	}
}
