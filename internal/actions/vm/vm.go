// Package vm runs one GitHub Actions job per cloud VM. The VM receives only a
// single-use JIT runner configuration; provider credentials stay with Hakopod.
package vm

import (
	"context"
	"errors"
	"time"
)

// Tags mark every instance Hakopod launches. Termination and the reaper act
// only on instances that carry this installation's tag.
const (
	TagInstallation = "hakopod-installation"
	TagCapacity     = "hakopod-capacity"
	TagPool         = "hakopod-pool"
	TagSlot         = "hakopod-slot"
)

// Instance states use EC2's names: pending, running, shutting-down,
// terminated, stopping and stopped.
type Instance struct {
	ID          string
	Type        string
	Lifecycle   string // spot or on-demand
	State       string
	StateReason string // the cloud's reason code for the last state change
	LaunchedAt  time.Time
	Tags        map[string]string
}

// Interrupted reports a spot instance the cloud reclaimed.
func (i Instance) Interrupted() bool {
	return i.StateReason == "Server.SpotInstanceTermination"
}

// Gone reports an instance that is shutting down or already gone.
func (i Instance) Gone() bool {
	return i.State == "shutting-down" || i.State == "terminated" || i.State == "stopping" || i.State == "stopped"
}

type LaunchInput struct {
	Name          string // unique per slot; names cloud-side helpers
	Tags          map[string]string
	InstanceTypes []string
	Spot          bool
	UserData      string
}

var ErrNotFound = errors.New("the cloud has no record of this instance")

// ErrNoCapacity means the cloud answered but started nothing, for example
// because no listed type had spot capacity. Retrying the same request is
// pointless; callers start over with a fresh slot.
var ErrNoCapacity = errors.New("the cloud launched no instance")

// Provider is one cloud account and region, as an Actions capacity names it.
type Provider interface {
	Launch(context.Context, LaunchInput) (Instance, error)
	// State returns ErrNotFound once the cloud has forgotten the instance.
	State(context.Context, string) (Instance, error)
	// Terminate refuses an instance without this installation's tag.
	Terminate(context.Context, string) error
	List(context.Context, map[string]string) ([]Instance, error)
	// Check proves the credentials and network settings without launching.
	// It returns warnings worth showing an administrator.
	Check(context.Context) ([]string, error)
}
