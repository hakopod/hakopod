package cluster

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// A deploy that stops in preflight changed nothing, and says so; the message
// itself is unchanged.
func TestDeployPreflightFailureIsNotApplied(t *testing.T) {
	target := testTarget(t)
	target.ApplicationID = ""
	c := &Client{kube: fake.NewClientset(), options: Options{}}
	_, err := c.Deploy(context.Background(), target, func(Event) {})
	if !errors.Is(err, ErrNotApplied) || err.Error() == ErrNotApplied.Error() {
		t.Fatalf("preflight failure not marked as not applied: %v", err)
	}
	if errors.Is(errors.New("other"), ErrNotApplied) || NotApplied(nil) != nil {
		t.Fatal("NotApplied matched an unrelated error")
	}
}
