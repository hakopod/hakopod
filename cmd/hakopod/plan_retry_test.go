package main

import (
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestRetryNoteForUnchangedPlanAfterFailedRelease(t *testing.T) {
	plan := appPlan{ExpectedRevision: 7, LastReleaseFailed: true}
	if got := retryNote(plan); got != "the last release (revision 7) failed; deploying again will retry it" {
		t.Fatalf("note %q", got)
	}
	plan.Changes = []spec.Change{{Service: "web", Field: "image"}}
	if retryNote(plan) != "" {
		t.Fatal("a plan with changes needs no retry note")
	}
	if retryNote(appPlan{ExpectedRevision: 7}) != "" {
		t.Fatal("a plan after a healthy release needs no retry note")
	}
}
