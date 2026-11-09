package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/actions/vm"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type vmFake struct {
	instances  map[string]*vm.Instance
	launches   []vm.LaunchInput
	terminated []string
	noCapacity bool
}

func (f *vmFake) Launch(_ context.Context, in vm.LaunchInput) (vm.Instance, error) {
	f.launches = append(f.launches, in)
	if f.noCapacity {
		return vm.Instance{}, fmt.Errorf("%w (InsufficientInstanceCapacity)", vm.ErrNoCapacity)
	}
	lifecycle := "on-demand"
	if in.Spot {
		lifecycle = "spot"
	}
	tags := maps.Clone(in.Tags)
	tags[vm.TagInstallation] = "inst"
	i := &vm.Instance{ID: fmt.Sprintf("i-%d", len(f.launches)), Type: in.InstanceTypes[0], Lifecycle: lifecycle, State: "pending", LaunchedAt: time.Now(), Tags: tags}
	f.instances[i.ID] = i
	return *i, nil
}
func (f *vmFake) State(_ context.Context, id string) (vm.Instance, error) {
	if i, ok := f.instances[id]; ok {
		return *i, nil
	}
	return vm.Instance{}, vm.ErrNotFound
}
func (f *vmFake) Terminate(_ context.Context, id string) error {
	f.terminated = append(f.terminated, id)
	if i, ok := f.instances[id]; ok {
		i.State = "terminated"
	}
	return nil
}
func (f *vmFake) List(_ context.Context, tags map[string]string) ([]vm.Instance, error) {
	out := []vm.Instance{}
next:
	for _, i := range f.instances {
		for k, v := range tags {
			if i.Tags[k] != v {
				continue next
			}
		}
		out = append(out, *i)
	}
	return out, nil
}
func (f *vmFake) Check(context.Context) ([]string, error) { return nil, nil }

// jitFake hands out a JIT configuration shaped like GitHub's and records reruns.
type jitFake struct {
	*actionsFake
	reruns *[]string
	refuse *bool
}

func (f jitFake) RerunFailedJobs(_ context.Context, repository string, run int64) error {
	if *f.refuse {
		return &actions.StatusError{Status: 403}
	}
	*f.reruns = append(*f.reruns, fmt.Sprintf("%s#%d", repository, run))
	return nil
}

func (f jitFake) Register(ctx context.Context, target actions.Target, name string, labels []string) (actions.Registration, error) {
	r, err := f.actionsFake.Register(ctx, target, name, labels)
	r.EncodedConfig = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("single-job ", 20)))
	return r, err
}

func vmHarness(t *testing.T, replicas int32) (*Server, *actionsFake, *vmFake, store.ActionsPool) {
	db := actionsDatabase(t)
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	ctx := context.Background()
	settings := store.ActionsCapacitySettings{ExternalID: "hakopod-test", Capacities: []store.ActionsCapacity{{
		Name: "aws-spot", Cloud: "aws", Region: "eu-central-1", RoleARN: "arn:aws:iam::123456789012:role/runner", SubnetIDs: []string{"subnet-0abc1234"},
		SecurityGroupIDs: []string{"sg-0abc1234"}, InstanceTypes: []string{"c7i.xlarge", "m7i.xlarge"}, Architecture: "amd64", Spot: true, DiskGiB: 100,
		MaxInstances: 1, Environments: []string{"tenant-a/production"},
	}}}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO installation_settings(name,value) VALUES('actions_capacity',$1)", store.JSON(settings)); err != nil {
		t.Fatal(err)
	}
	a, err := spec.Normalize(spec.Application{Name: "runners", Services: map[string]spec.Service{"runner": {Replicas: replicas, Actions: &spec.Actions{Repository: "team/repo", Credential: "token", Capacity: "aws-spot"}}}})
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
	cloud := &vmFake{instances: map[string]*vm.Instance{}}
	client := jitFake{fake, &[]string{}, new(bool)}
	s := &Server{Store: db, actionsTestRuntime: fake, actionsClient: func(string) (runnerProvider, error) { return client, nil },
		actionsVMClient: func(context.Context, store.ActionsCapacity, string) (vm.Provider, error) { return cloud, nil }}
	s.Auth.EncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	return s, fake, cloud, pools[0]
}

func TestActionsVMSlotLifecycle(t *testing.T) {
	s, f, cloud, p := vmHarness(t, 2)
	ctx := context.Background()
	target := actionsTarget(p)
	slots := func() []store.ActionsSlot {
		t.Helper()
		v, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err := s.reconcileActionsPool(ctx, target, p); err != nil {
		t.Fatal(err)
	}
	v := slots()
	if len(v) != 1 || v[0].InstanceID != "i-1" || v[0].InstanceLifecycle != "spot" || v[0].EncryptedRegistration != nil || len(f.pods) != 0 {
		t.Fatalf("VM slot not launched: %+v", v)
	}
	launch := cloud.launches[0]
	if !launch.Spot || launch.Tags[vm.TagSlot] != v[0].ID || launch.Tags[vm.TagPool] != p.ApplicationID+"/runner" || !strings.Contains(launch.UserData, "--jitconfig") {
		t.Fatalf("launch request: %+v", launch)
	}
	if strings.Contains(launch.UserData, "control-plane-fixture-only") {
		t.Fatal("the installation credential reached the VM")
	}

	// max_instances is 1: the second replica waits before any registration.
	err := s.reconcileActionsPool(ctx, target, p)
	var wait *actionsCapacityError
	if !errors.As(err, &wait) || !wait.full || !strings.Contains(safeActionsError(err), "Waiting for capacity aws-spot") || f.next != 1 {
		t.Fatal("ceiling not enforced or not explained", err, f.next)
	}

	// A spot interruption of a busy runner is recorded, then cleaned up.
	cloud.instances["i-1"].State = "running"
	runner := f.runners[1]
	runner.Busy = true
	f.runners[1] = runner
	_ = s.reconcileActionsPool(ctx, target, p)
	cloud.instances["i-1"].State, cloud.instances["i-1"].StateReason = "terminated", "Server.SpotInstanceTermination"
	runner.Busy = false
	f.runners[1] = runner
	_ = s.reconcileActionsPool(ctx, target, p)
	evictions, err := s.Store.ActionsEvictions(ctx, p.ApplicationID, p.Service)
	if err != nil || len(evictions) != 1 || evictions[0].InstanceID != "i-1" || evictions[0].InstanceType != "c7i.xlarge" {
		t.Fatal("eviction not recorded", evictions, err)
	}
	if _, ok := f.runners[1]; ok {
		t.Fatal("GitHub runner of the vanished instance was kept")
	}
	if len(cloud.launches) < 2 || cloud.launches[1].Tags[vm.TagSlot] == v[0].ID {
		t.Fatal("a fresh slot did not replace the interrupted one")
	}

	// No capacity anywhere: the slot is retired and its registration removed.
	for _, v := range slots() {
		cloud.instances[v.InstanceID].State = "terminated"
	}
	cloud.noCapacity = true
	_ = s.reconcileActionsPool(ctx, target, p)
	if v := slots(); len(v) != 1 || v[0].Phase != "cleanup" || v[0].InstanceID != "" {
		t.Fatalf("failed launch left a live slot: %+v", v)
	}
	if err = s.reconcileActionsPool(ctx, target, p); !errors.As(err, &wait) {
		t.Fatal(err)
	}
	cloud.noCapacity = false

	// Removing the pool terminates its instances.
	if err = s.reconcileActionsPool(ctx, target, p); err != nil {
		t.Fatal(err)
	}
	p.Removed = true
	for range 3 {
		_ = s.reconcileActionsPool(ctx, target, p)
	}
	for _, i := range cloud.instances {
		if i.State != "terminated" {
			t.Fatalf("instance %s survived pool removal", i.ID)
		}
	}
	if len(slots()) != 0 {
		t.Fatal("slots survived pool removal")
	}
}

func TestActionsVMReaper(t *testing.T) {
	s, _, cloud, p := vmHarness(t, 1)
	ctx := context.Background()
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
		t.Fatal(err)
	}
	live := cloud.instances["i-1"]
	live.State = "running"
	tags := func(slot string) map[string]string {
		return map[string]string{vm.TagInstallation: "inst", vm.TagCapacity: "aws-spot", vm.TagSlot: slot}
	}
	cloud.instances["i-orphan"] = &vm.Instance{ID: "i-orphan", State: "running", LaunchedAt: time.Now(), Tags: tags(store.NewID())}
	cloud.instances["i-dup"] = &vm.Instance{ID: "i-dup", State: "running", LaunchedAt: time.Now(), Tags: tags(live.Tags[vm.TagSlot])}
	// A draining slot is marked cleanup while its job still runs.
	if err := s.Store.UpdateActionsSlot(ctx, live.Tags[vm.TagSlot], 1, "cleanup"); err != nil {
		t.Fatal(err)
	}
	c, _, err := s.Store.ActionsCapacity(ctx, "aws-spot")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.reapActionsCapacity(ctx, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if live.State != "running" || cloud.instances["i-orphan"].State != "terminated" || cloud.instances["i-dup"].State != "terminated" {
		t.Fatal("reaper terminated the wrong instances", cloud.terminated)
	}
	if err = s.reapActionsCapacity(ctx, c, time.Now().Add(76*time.Minute)); err != nil || live.State != "terminated" {
		t.Fatal("reaper kept an instance past its time limit", err)
	}
}

func TestActionsVMSpotRerun(t *testing.T) {
	s, _, cloud, p := vmHarness(t, 1)
	ctx := context.Background()
	target := actionsTarget(p)
	client, _ := s.actionsClient("")
	fake := client.(jitFake)
	evict := func(attempt int64) {
		t.Helper()
		if err := s.reconcileActionsPool(ctx, target, p); err != nil {
			t.Fatal(err)
		}
		slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
		if err != nil || len(slots) != 1 {
			t.Fatal(slots, err)
		}
		cloud.instances[slots[0].InstanceID].State = "terminated"
		cloud.instances[slots[0].InstanceID].StateReason = "Server.SpotInstanceTermination"
		_ = s.reconcileActionsPool(ctx, target, p)
		body := fmt.Sprintf(`{"action":"completed","workflow_job":{"id":%d,"labels":["hakopod"],"run_id":77,"run_attempt":%d,"conclusion":"failure","runner_name":"hakopod-%s"},"repository":{"full_name":"team/repo"}}`, attempt, attempt, slots[0].ID)
		if err = s.recordActionsJob(ctx, defaultGitConnection("github"), []byte(body)); err != nil {
			t.Fatal(err)
		}
		if err = s.rerunEvictedJobs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	evict(1)
	if len(*fake.reruns) != 1 || (*fake.reruns)[0] != "team/repo#77" {
		t.Fatal("interrupted job not rerun", *fake.reruns)
	}
	// The replacement already started on spot; the next launch after the
	// rerun is on-demand.
	launches := len(cloud.launches)
	cloud.instances[fmt.Sprintf("i-%d", launches)].State = "terminated"
	if err := s.reconcileActionsPool(ctx, target, p); err != nil {
		t.Fatal(err)
	}
	if len(cloud.launches) != launches+1 || cloud.launches[launches].Spot {
		t.Fatal("rerun did not launch on-demand", len(cloud.launches))
	}
	*fake.refuse = true
	evict(2)
	evictions, err := s.Store.ActionsEvictions(ctx, p.ApplicationID, p.Service)
	if err != nil || len(evictions) != 2 || !strings.Contains(evictions[0].RerunError, "Actions write permission") {
		t.Fatal("refused rerun not recorded on the pool", evictions, err)
	}
	// A refused rerun does not count against the default two retries.
	*fake.refuse = false
	evict(3)
	evict(4)
	if len(*fake.reruns) != 2 {
		t.Fatal("rerun beyond spot_retries", *fake.reruns)
	}
}
