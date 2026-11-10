package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/actions/vm"
	vmaws "github.com/hakopod/hakopod/internal/actions/vm/aws"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// These tests drive the Actions worker (the sweep, the warm pool, the reaper
// and spot reruns) against a real AWS account, with the real store on an
// isolated PostgreSQL. Only GitHub is faked: JIT configurations, runner
// status and job reruns. They need HAKOPOD_AWS_ACCEPTANCE=1,
// HAKOPOD_TEST_DATABASE_URL, the HAKOPOD_ACTIONS_VM_* role settings and
// AWS_REGION, as TestAcceptanceSpotInstance in internal/actions/vm/aws does.
// Each test uses its own acceptance-pool-... installation and terminates
// everything carrying that tag when it ends. The spot interruption test also
// needs HAKOPOD_ACCEPTANCE_INTERRUPT: a command that, given an instance ID,
// starts an AWS FIS aws:ec2:send-spot-instance-interruptions experiment on it.
// FIS interrupts two minutes after its notice, and a runner given a fake JIT
// configuration powers off sooner, so HAKOPOD_ACCEPTANCE_SPOT_IMAGE can name
// an arm64 AMI that ignores the boot script (Bottlerocket, say) to keep the
// spot instance up until the interruption.
//
//	HAKOPOD_AWS_ACCEPTANCE=1 HAKOPOD_TEST_DATABASE_URL=postgres://... \
//	  go test ./internal/api -run TestAcceptanceActionsVM -timeout 90m -v

// stockUbuntu is a capacity image setting that is not runs-on-full: a runner
// on it installs Docker and the runner at boot, which takes minutes.
const stockUbuntu = "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"

type awsRig struct {
	t            *testing.T
	s            *Server
	gh           jitFake
	cloud        *vmaws.Client
	view         vm.Provider // what the worker sees; cloud unless an interruption is simulated
	pool         store.ActionsPool
	installation string
	capacity     string
	start        time.Time
}

func newAWSRig(t *testing.T, c store.ActionsCapacity, retries int64) *awsRig {
	t.Helper()
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to run the Actions worker against a real AWS account")
	}
	db := actionsDatabase(t)
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	ctx := context.Background()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	installation := "acceptance-pool-" + hex.EncodeToString(suffix)
	if _, err := db.Pool.Exec(ctx, "UPDATE installation_license SET installation_id=$1", installation); err != nil {
		t.Fatal(err)
	}
	c.Cloud, c.Region, c.RoleARN, c.Architecture = "aws", os.Getenv("AWS_REGION"), os.Getenv("HAKOPOD_ACTIONS_VM_ROLE_ARN"), "arm64"
	c.SubnetIDs = []string{os.Getenv("HAKOPOD_ACTIONS_VM_SUBNET")}
	c.SecurityGroupIDs = []string{os.Getenv("HAKOPOD_ACTIONS_VM_SECURITY_GROUP")}
	c.Environments = []string{"tenant-a/production"}
	settings := store.ActionsCapacitySettings{ExternalID: os.Getenv("HAKOPOD_ACTIONS_VM_EXTERNAL_ID"), Capacities: []store.ActionsCapacity{c}}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO installation_settings(name,value) VALUES('actions_capacity',$1)", store.JSON(settings)); err != nil {
		t.Fatal(err)
	}
	a, err := spec.Normalize(spec.Application{Name: "runners", Services: map[string]spec.Service{"runner": {Replicas: 1,
		Actions: &spec.Actions{Repository: "team/repo", Credential: "token", Capacity: c.Name, SpotRetries: &retries}}}})
	if err != nil {
		t.Fatal(err)
	}
	app := store.Application{ID: store.NewID(), Project: "tenant-a", Environment: "production", Name: "runners"}
	if err = db.SyncActions(ctx, app, a, 1); err != nil {
		t.Fatal(err)
	}
	pools, err := db.ActionsPools(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	fake := &actionsFake{runners: map[int64]actions.Runner{}, pods: map[string]bool{}, configs: map[string]bool{}}
	gh := jitFake{fake, &[]string{}, new(bool)}
	// actionsVMClient stays nil: the worker builds its own vm/aws client.
	s := &Server{Store: db, actionsTestRuntime: fake, actionsClient: func(string) (runnerProvider, error) { return gh, nil }}
	s.Auth.EncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	cloud, err := vmaws.New(ctx, vmaws.Config{Installation: installation, Region: c.Region, RoleARN: c.RoleARN, ExternalID: settings.ExternalID,
		SubnetIDs: c.SubnetIDs, SecurityGroupIDs: c.SecurityGroupIDs, Architecture: "arm64", DiskGiB: 20})
	if err != nil {
		t.Fatal(err)
	}
	r := &awsRig{t: t, s: s, gh: gh, cloud: cloud, view: cloud, pool: pools[0], installation: installation, capacity: c.Name, start: time.Now()}
	t.Cleanup(r.cleanup)
	t.Logf("installation %s, capacity %s", installation, c.Name)
	return r
}

// cleanup terminates every instance and deletes every image carrying this
// installation's tag, then waits until no instance is left running.
func (r *awsRig) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for {
		list, err := r.cloud.List(ctx, nil)
		if err != nil {
			r.t.Errorf("cleanup: %v; terminate instances tagged %s by hand", err, r.installation)
			return
		}
		left := 0
		for _, i := range list {
			if i.State == "terminated" {
				continue
			}
			left++
			if i.State != "shutting-down" {
				if err = r.cloud.Terminate(ctx, i.ID); err != nil {
					r.t.Errorf("cleanup: %s: %v", i.ID, err)
				}
			}
		}
		if images, err := r.cloud.Images(ctx, nil); err == nil {
			for _, i := range images {
				if err = r.cloud.DeleteImage(ctx, i.ID); err != nil {
					r.t.Errorf("cleanup: image %s: %v", i.ID, err)
				}
			}
		}
		if left == 0 {
			r.t.Logf("cleanup: nothing tagged %s is left running", r.installation)
			return
		}
		if ctx.Err() != nil {
			r.t.Errorf("cleanup: %d instances tagged %s still not terminated", left, r.installation)
			return
		}
		time.Sleep(10 * time.Second)
	}
}

func (r *awsRig) since() string { return time.Since(r.start).Round(time.Second).String() }

// live lists the installation's instances with the tags that are not
// terminated or shutting down.
func (r *awsRig) live(tags map[string]string) []vm.Instance {
	r.t.Helper()
	list, err := r.cloud.List(context.Background(), tags)
	if err != nil {
		r.t.Fatal(err)
	}
	return slices.DeleteFunc(list, func(i vm.Instance) bool { return i.State == "terminated" || i.State == "shutting-down" })
}

func (r *awsRig) standbys() []vm.Instance {
	return r.live(map[string]string{vm.TagCapacity: r.capacity, vm.TagPoolState: "standby"})
}

func (r *awsRig) state(id string) vm.Instance {
	r.t.Helper()
	i, err := r.view.State(context.Background(), id)
	if err != nil && !errors.Is(err, vm.ErrNotFound) {
		r.t.Fatal(err)
	}
	return i
}

// sweep runs one pass of the Actions worker exactly as runActionsVMReaper does.
func (r *awsRig) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	r.s.reapActionsVMs(ctx)
}

func (r *awsRig) reconcile() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return r.s.reconcileActionsPool(ctx, actionsTarget(r.pool), r.pool)
}

func (r *awsRig) slots() []store.ActionsSlot {
	r.t.Helper()
	v, err := r.s.Store.ActionsSlots(context.Background(), r.pool.ApplicationID, r.pool.Service)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

// until polls done every five seconds and runs step every period, the way
// the worker's timers would, until done holds or timeout passes.
func (r *awsRig) until(what string, timeout, period time.Duration, step func(), done func() bool) time.Duration {
	r.t.Helper()
	began := time.Now()
	next := began
	for {
		if step != nil && !time.Now().Before(next) {
			step()
			next = time.Now().Add(period)
		}
		if done() {
			took := time.Since(began).Round(time.Second)
			r.t.Logf("[%s] %s after %s", r.since(), what, took)
			return took
		}
		if time.Since(began) > timeout {
			r.t.Fatalf("[%s] %s: not within %s", r.since(), what, timeout)
		}
		time.Sleep(5 * time.Second)
	}
}

func (r *awsRig) setCapacity(change func(*store.ActionsCapacity)) store.ActionsCapacity {
	return setCapacity(r.t, r.s, change)
}

// launchStray starts an instance the worker did not launch, with the
// installation's tag and the given ones, that stays running for an hour.
func (r *awsRig) launchStray(name string, tags map[string]string, image string) vm.Instance {
	r.t.Helper()
	disk := int64(0)
	if image != "" {
		disk = 30 // RunsOn's images need 30 GiB
	}
	i, err := r.cloud.Launch(context.Background(), vm.LaunchInput{Name: name, Tags: tags, InstanceTypes: []string{"t4g.nano", "t4g.micro"},
		UserData: "#!/bin/bash\nsleep 3600\n", Image: image, DiskGiB: disk, StopOnShutdown: tags[vm.TagPoolState] == "standby"})
	if err != nil {
		r.t.Fatal(err)
	}
	r.until("stray "+i.ID+" listed", 2*time.Minute, 0, nil, func() bool {
		return slices.ContainsFunc(r.live(tags), func(l vm.Instance) bool { return l.ID == i.ID })
	})
	return i
}

func stoppedCount(list []vm.Instance) int {
	n := 0
	for _, i := range list {
		if i.State == "stopped" {
			n++
		}
	}
	return n
}

// TestAcceptanceActionsVMWarmPool: refill to warm_pool, a claim then a
// refill, an extra and a stuck standby trimmed, standbys replaced when the
// image changes, and warm_pool 0 terminating them all.
func TestAcceptanceActionsVMWarmPool(t *testing.T) {
	t.Parallel()
	r := newAWSRig(t, store.ActionsCapacity{Name: "pool", InstanceTypes: []string{"t4g.micro"}, DiskGiB: 30, MaxInstances: 3,
		Image: vm.ImageRunsOnFull, WarmPool: 2}, 2)
	ctx := context.Background()
	image, err := r.cloud.Image(ctx, vm.ImageRunsOnFull)
	if err != nil {
		t.Fatal(err)
	}
	most := 0
	ready := func(image string) func() bool {
		return func() bool {
			list := r.standbys()
			most = max(most, len(list))
			for _, i := range list {
				if i.Tags[vm.TagImage] != image || i.Lifecycle != "on-demand" {
					t.Fatalf("standby %s: image %q, %s", i.ID, i.Tags[vm.TagImage], i.Lifecycle)
				}
			}
			return len(list) == 2 && stoppedCount(list) == 2
		}
	}

	// Refill from empty: two standbys boot, warm up and stop themselves.
	refill := r.until("warm pool filled to 2 stopped standbys", 15*time.Minute, 30*time.Second, r.sweep, ready(image))
	if most > 2 {
		t.Fatalf("the sweep launched %d standbys for a warm pool of 2", most)
	}

	// A job claims a standby instead of launching one.
	before := map[string]bool{}
	for _, i := range r.standbys() {
		before[i.ID] = true
	}
	claimAt := time.Now()
	if err = r.reconcile(); err != nil {
		t.Fatal(err)
	}
	claimCall := time.Since(claimAt).Round(time.Millisecond)
	slots := r.slots()
	if len(slots) != 1 || slots[0].InstanceSource != "standby" || !before[slots[0].InstanceID] {
		t.Fatalf("no standby claimed: %+v", slots)
	}
	claimed := slots[0].InstanceID
	t.Logf("[%s] slot %s claimed standby %s in %s", r.since(), slots[0].ID, claimed, claimCall)
	if phase, err := r.s.slotRuntime(slots[0].Config).ActionsPodPhase(ctx, actionsTarget(r.pool), slots[0].ID); err != nil || phase != "Pending" {
		t.Fatal("claimed standby phase", phase, err)
	}
	toRunning := r.until("claimed standby running", 3*time.Minute, 0, nil, func() bool {
		i := r.state(claimed)
		if i.Tags[vm.TagPoolState] != "claimed" || i.Tags[vm.TagSlot] != slots[0].ID {
			t.Fatalf("claimed standby tags: %v", i.Tags)
		}
		return i.State == "running"
	})

	// The sweep refills the pool while the job runs; the claimed instance is
	// the slot's and is left alone.
	claimRefill := r.until("warm pool refilled after the claim", 15*time.Minute, 30*time.Second, r.sweep, ready(image))
	// The fake JIT configuration makes the runner exit; a claimed standby
	// terminates on shutdown instead of stopping.
	r.until("claimed instance terminated itself after its job", 10*time.Minute, 0, nil, func() bool {
		i := r.state(claimed)
		if i.State == "stopped" {
			t.Fatalf("claimed standby %s stopped instead of terminating", claimed)
		}
		return i.State == "terminated" || i.State == "shutting-down"
	})

	// An extra standby, as from a launch whose response was lost, is trimmed.
	extra := r.launchStray("standby-extra", map[string]string{vm.TagCapacity: "pool", vm.TagPoolState: "standby", vm.TagImage: image}, image)
	r.sweep()
	if list := r.standbys(); len(list) != 2 || stoppedCount(list) != 2 {
		t.Fatalf("extra standby %s not trimmed, or a ready one was: %+v", extra.ID, list)
	}

	// A standby that never stops is replaced once it has run 15 minutes. The
	// clock is moved forward rather than waited for.
	stuck := r.launchStray("standby-stuck", map[string]string{vm.TagCapacity: "pool", vm.TagPoolState: "standby", vm.TagImage: image}, image)
	c := r.setCapacity(func(c *store.ActionsCapacity) { c.WarmPool, c.MaxInstances = 3, 4 })
	if err = r.s.keepActionsStandbys(ctx, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if list := r.standbys(); len(list) != 3 || !slices.ContainsFunc(list, func(i vm.Instance) bool { return i.ID == stuck.ID }) {
		t.Fatalf("a booting standby was not kept: %+v", list)
	}
	if err = r.s.keepActionsStandbys(ctx, c, time.Now().Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list := r.standbys()
	if slices.ContainsFunc(list, func(i vm.Instance) bool { return i.ID == stuck.ID }) {
		t.Fatalf("stuck standby %s kept", stuck.ID)
	}
	t.Logf("[%s] stuck standby %s replaced; %d standbys", r.since(), stuck.ID, len(list))
	r.setCapacity(func(c *store.ActionsCapacity) { c.WarmPool, c.MaxInstances = 2, 3 })
	r.until("pool back to 2 stopped standbys", 15*time.Minute, 30*time.Second, r.sweep, ready(image))

	// A new image replaces every standby.
	old := r.standbys()
	r.setCapacity(func(c *store.ActionsCapacity) { c.Image, c.DiskGiB = stockUbuntu, 20 })
	imageSwap := r.until("standbys rebuilt on the new image", 20*time.Minute, 30*time.Second, r.sweep, ready(stockUbuntu))
	for _, i := range old {
		if s := r.state(i.ID).State; s != "terminated" && s != "shutting-down" {
			t.Fatalf("standby %s of the old image is %s", i.ID, s)
		}
	}

	// warm_pool 0 terminates every standby.
	r.setCapacity(func(c *store.ActionsCapacity) { c.WarmPool = 0 })
	drain := r.until("warm_pool 0 left no standby", 3*time.Minute, 30*time.Second, r.sweep, func() bool { return len(r.standbys()) == 0 })
	t.Logf("TIMINGS refill=%s claim_call=%s claim_to_running=%s claim_to_refill=%s image_swap=%s drain=%s", refill, claimCall, toRunning, claimRefill, imageSwap, drain)
}

// TestAcceptanceActionsVMReaper: a tagged instance with no slot, a duplicate
// launch for a slot, and a slot instance past its time limit are terminated;
// the slot's own instance is not.
func TestAcceptanceActionsVMReaper(t *testing.T) {
	t.Parallel()
	r := newAWSRig(t, store.ActionsCapacity{Name: "reap", InstanceTypes: []string{"t4g.nano"}, DiskGiB: 20, MaxInstances: 3, Image: stockUbuntu}, 2)
	ctx := context.Background()

	stray := r.launchStray("stray", map[string]string{vm.TagCapacity: "reap", vm.TagSlot: store.NewID()}, "")
	noSlot := r.until("stray instance with no slot terminated", 3*time.Minute, 30*time.Second, r.sweep, func() bool {
		s := r.state(stray.ID).State
		return s == "terminated" || s == "shutting-down"
	})

	if err := r.reconcile(); err != nil {
		t.Fatal(err)
	}
	slots := r.slots()
	if len(slots) != 1 || slots[0].InstanceID == "" || slots[0].InstanceSource != "cold" || slots[0].InstanceLifecycle != "on-demand" {
		t.Fatalf("slot not launched: %+v", slots)
	}
	own := slots[0].InstanceID
	duplicate := r.launchStray("duplicate", map[string]string{vm.TagCapacity: "reap", vm.TagSlot: slots[0].ID}, "")
	dup := r.until("duplicate launch for a slot terminated", 3*time.Minute, 30*time.Second, r.sweep, func() bool {
		s := r.state(duplicate.ID).State
		return s == "terminated" || s == "shutting-down"
	})
	if s := r.state(own).State; s == "terminated" || s == "shutting-down" {
		t.Fatalf("the reaper terminated the slot's own instance %s, or it ended first (%s)", own, s)
	}

	// Past timeout_minutes plus 15 the slot's instance goes too. The clock is
	// moved forward rather than waited for.
	c, _, err := r.s.Store.ActionsCapacity(ctx, "reap")
	if err != nil {
		t.Fatal(err)
	}
	limit := time.Duration(slots[0].Config.Actions.TimeoutMinutes)*time.Minute + 16*time.Minute
	if err = r.s.reapActionsCapacity(ctx, c, time.Now().Add(limit)); err != nil {
		t.Fatal(err)
	}
	timedOut := r.until("slot instance past its time limit terminated", time.Minute, 0, nil, func() bool {
		s := r.state(own).State
		return s == "terminated" || s == "shutting-down"
	})
	t.Logf("TIMINGS no_slot=%s duplicate=%s timed_out=%s", noSlot, dup, timedOut)
}

// reclaimed is the worker's real provider, except that instances it was told
// about read as reclaimed by spot once they are gone. It stands in for an
// FIS interruption when none can be started.
type reclaimed struct {
	vm.Provider
	ids map[string]bool
}

func (p *reclaimed) mark(i vm.Instance) vm.Instance {
	if p.ids[i.ID] && (i.State == "terminated" || i.State == "shutting-down") {
		i.StateReason = "Server.SpotInstanceTermination"
	}
	return i
}

func (p *reclaimed) State(ctx context.Context, id string) (vm.Instance, error) {
	i, err := p.Provider.State(ctx, id)
	return p.mark(i), err
}

func (p *reclaimed) List(ctx context.Context, tags map[string]string) ([]vm.Instance, error) {
	list, err := p.Provider.List(ctx, tags)
	for n := range list {
		list[n] = p.mark(list[n])
	}
	return list, err
}

// TestAcceptanceActionsVMSpotInterruption: a real spot interruption marks
// the slot interrupted, the failed run is rerun once and its next launch is
// on-demand; a second interruption beyond spot_retries is not rerun. Without
// HAKOPOD_ACCEPTANCE_INTERRUPT the interruption is SIMULATED: the spot
// instance is terminated through the API and the worker is told the reason.
func TestAcceptanceActionsVMSpotInterruption(t *testing.T) {
	t.Parallel()
	interrupt := os.Getenv("HAKOPOD_ACCEPTANCE_INTERRUPT")
	image := os.Getenv("HAKOPOD_ACCEPTANCE_SPOT_IMAGE")
	if image == "" {
		image = stockUbuntu
	}
	r := newAWSRig(t, store.ActionsCapacity{Name: "spot", InstanceTypes: []string{"t4g.nano", "t4g.micro", "t4g.small"}, Spot: true, DiskGiB: 20,
		MaxInstances: 2, Image: image}, 1)
	ctx := context.Background()
	start := func(id string) string {
		out, err := exec.Command(interrupt, id).CombinedOutput()
		if err != nil {
			t.Fatalf("interrupt %s: %v: %s", id, err, out)
		}
		return "FIS experiment " + strings.TrimSpace(string(out))
	}
	if interrupt == "" {
		fake := &reclaimed{Provider: r.cloud, ids: map[string]bool{}}
		r.view = fake
		r.s.actionsVMClient = func(context.Context, store.ActionsCapacity, string) (vm.Provider, error) { return fake, nil }
		start = func(id string) string {
			fake.ids[id] = true
			if err := r.cloud.Terminate(ctx, id); err != nil {
				t.Fatal(err)
			}
			return "SIMULATED interruption (terminated through the API)"
		}
	}

	// evict launches a spot slot, interrupts its instance and reconciles until
	// the interruption is recorded, then delivers the failed job for attempt.
	evict := func(attempt int64) (store.ActionsSlot, map[string]time.Duration) {
		t.Helper()
		timings := map[string]time.Duration{}
		var slot store.ActionsSlot
		r.until("spot slot launched", 3*time.Minute, 15*time.Second, func() {
			if err := r.reconcile(); err != nil {
				t.Log("reconcile:", safeActionsError(err))
			}
		}, func() bool {
			for _, v := range r.slots() {
				if v.InstanceID != "" && v.InstanceLifecycle == "spot" && r.state(v.InstanceID).State == "running" {
					slot = v
					return true
				}
			}
			return false
		})
		how := start(slot.InstanceID)
		notice := time.Now()
		t.Logf("[%s] %s: %s (slot %s, %s)", r.since(), how, slot.InstanceID, slot.ID, slot.InstanceType)
		r.until("instance reclaimed by spot", 8*time.Minute, 0, nil, func() bool {
			i := r.state(slot.InstanceID)
			if (i.State == "terminated" || i.State == "shutting-down") && !i.Interrupted() {
				t.Fatalf("%s ended before the interruption: %s (%s)", i.ID, i.State, i.StateReason)
			}
			return i.Interrupted()
		})
		timings["notice_to_reclaimed"] = time.Since(notice).Round(time.Second)
		r.until("slot recorded as interrupted", 3*time.Minute, 10*time.Second, func() {
			if err := r.reconcile(); err != nil {
				t.Log("reconcile:", safeActionsError(err))
			}
		}, func() bool {
			evictions, err := r.s.Store.ActionsEvictions(ctx, r.pool.ApplicationID, r.pool.Service)
			if err != nil {
				t.Fatal(err)
			}
			return slices.ContainsFunc(evictions, func(e store.ActionsEviction) bool { return e.SlotID == slot.ID })
		})
		timings["notice_to_interrupted"] = time.Since(notice).Round(time.Second)
		body := fmt.Sprintf(`{"action":"completed","workflow_job":{"id":%d,"labels":["hakopod"],"run_id":77,"run_attempt":%d,"conclusion":"failure","runner_name":"hakopod-%s"},"repository":{"full_name":"team/repo"}}`, attempt, attempt, slot.ID)
		if err := r.s.recordActionsJob(ctx, defaultGitConnection("github"), []byte(body)); err != nil {
			t.Fatal(err)
		}
		r.sweep()
		timings["notice_to_rerun_decided"] = time.Since(notice).Round(time.Second)
		return slot, timings
	}

	first, timings := evict(1)
	if len(*r.gh.reruns) != 1 || (*r.gh.reruns)[0] != "team/repo#77" {
		t.Fatal("interrupted run not rerun", *r.gh.reruns)
	}
	// The slot that replaced the interrupted one may already be on spot; once
	// it ends, the next launch spends the rerun's on-demand grant.
	notice := time.Now().Add(-timings["notice_to_rerun_decided"])
	var onDemand store.ActionsSlot
	r.until("rerun launched on-demand", 6*time.Minute, 15*time.Second, func() {
		for _, v := range r.slots() {
			if v.ID != first.ID && v.InstanceID != "" && v.InstanceLifecycle == "spot" {
				if err := r.cloud.Terminate(ctx, v.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := r.reconcile(); err != nil {
			t.Log("reconcile:", safeActionsError(err))
		}
	}, func() bool {
		for _, v := range r.slots() {
			if v.InstanceID != "" && v.InstanceLifecycle == "on-demand" {
				onDemand = v
				return true
			}
		}
		return false
	})
	timings["notice_to_on_demand_launch"] = time.Since(notice).Round(time.Second)
	if i := r.state(onDemand.InstanceID); i.Lifecycle != "on-demand" {
		t.Fatalf("rerun instance %s is %s in EC2", i.ID, i.Lifecycle)
	}
	t.Logf("TIMINGS first interruption: %v", timings)

	// The on-demand runner ends; the next one is spot again, and its
	// interruption is beyond spot_retries = 1.
	if err := r.cloud.Terminate(ctx, onDemand.InstanceID); err != nil {
		t.Fatal(err)
	}
	second, timings := evict(2)
	t.Logf("TIMINGS second interruption: %v", timings)
	if len(*r.gh.reruns) != 1 {
		t.Fatal("rerun beyond spot_retries", *r.gh.reruns)
	}
	evictions, err := r.s.Store.ActionsEvictions(ctx, r.pool.ApplicationID, r.pool.Service)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(evictions, func(e store.ActionsEviction) bool { return e.SlotID == second.ID })
	if i < 0 || evictions[i].RerunAt == nil || !strings.Contains(evictions[i].RerunError, "already used its 1 spot retries") {
		t.Fatalf("second interruption not refused as over spot_retries: %+v", evictions)
	}
}
