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
	// TagPoolState marks warm pool instances: standby while stopped and
	// waiting for a job, claimed once a slot took one, builder for an
	// instance that becomes a hakopod-minimal image. Slot instances have none.
	TagPoolState = "hakopod-pool-state"
	// TagImage records the image a standby was prepared from.
	TagImage = "hakopod-image"
)

// Capacity image settings besides an AMI ID or resolve:ssm: parameter.
const (
	// ImageRunsOnFull is RunsOn's public build of GitHub's Ubuntu 24.04
	// runner image, with Docker and the runner preinstalled.
	ImageRunsOnFull = "runs-on-full"
	// ImageHakopodMinimal is an image Hakopod builds in the customer's
	// account: stock Ubuntu with Docker, the pinned runner and a boot unit.
	ImageHakopodMinimal = "hakopod-minimal"
	// ImageUbuntu is the Azure default: Canonical's Ubuntu 24.04 LTS from
	// the marketplace, with Docker and the runner installed at boot.
	ImageUbuntu = "ubuntu-24.04"
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
	Image         string // resolved by Provider.Image; empty is stock Ubuntu 24.04
	// StopOnShutdown keeps the instance when it powers off, for standbys and
	// image builders. Runners terminate on shutdown.
	StopOnShutdown bool
	DiskGiB        int64 // zero uses the capacity's
	// VolumeInitRate fetches the image's snapshot onto the root volume at
	// this many MiB/s (100-300), a charge per GiB of snapshot; zero leaves
	// EBS's lazy, best-effort loading.
	VolumeInitRate int64
}

// Image is a machine image Hakopod created; State is pending, available or
// failed.
type Image struct {
	ID        string
	State     string
	CreatedAt time.Time
	Tags      map[string]string
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

	// Image resolves a capacity's image setting to what Launch takes.
	Image(ctx context.Context, setting string) (string, error)
	// Claim readies a stopped standby for one job and starts it: the tags
	// and user data are replaced, and the instance terminates on shutdown.
	Claim(ctx context.Context, id, userData string, tags map[string]string) error
	// ConsoleOutput returns the instance's recent serial console output.
	ConsoleOutput(ctx context.Context, id string) (string, error)
	// CreateImage images a stopped instance; the image and its snapshots
	// carry the tags and this installation's.
	CreateImage(ctx context.Context, instance, name string, tags map[string]string) (string, error)
	// Images lists images carrying every given tag and this installation's.
	Images(ctx context.Context, tags map[string]string) ([]Image, error)
	// DeleteImage deregisters an image of this installation and deletes its
	// snapshots.
	DeleteImage(ctx context.Context, id string) error
}
