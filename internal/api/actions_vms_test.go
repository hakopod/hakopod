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
	claims     []string
	images     map[string]*vm.Image
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
	if in.Image != "" {
		tags["image"] = in.Image // lets tests see which image an instance came from
	}
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
func (f *vmFake) Image(_ context.Context, setting string) (string, error) {
	if setting == vm.ImageRunsOnFull {
		return "ami-runson", nil
	}
	return setting, nil
}
func (f *vmFake) Claim(_ context.Context, id, userData string, tags map[string]string) error {
	i := f.instances[id]
	if i == nil || i.State != "stopped" {
		return errors.New("not a stopped standby")
	}
	f.claims = append(f.claims, id+" "+userData)
	maps.Copy(i.Tags, tags)
	i.State = "pending"
	return nil
}
func (f *vmFake) ConsoleOutput(context.Context, string) (string, error) { return "", nil }
func (f *vmFake) CreateImage(_ context.Context, instance, name string, tags map[string]string) (string, error) {
	id := fmt.Sprintf("ami-%d", len(f.images)+1)
	f.images[id] = &vm.Image{ID: id, State: "pending", Tags: maps.Clone(tags)}
	return id, nil
}
func (f *vmFake) Images(_ context.Context, tags map[string]string) ([]vm.Image, error) {
	out := []vm.Image{}
	for _, i := range f.images {
		if i.Tags[vm.TagCapacity] == tags[vm.TagCapacity] {
			out = append(out, *i)
		}
	}
	return out, nil
}
func (f *vmFake) DeleteImage(_ context.Context, id string) error {
	delete(f.images, id)
	return nil
}

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
	cloud := &vmFake{instances: map[string]*vm.Instance{}, images: map[string]*vm.Image{}}
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

func TestActionsCapacitySettingsAPI(t *testing.T) {
	f := newAppLogFixture(t)
	cloud := &vmFake{instances: map[string]*vm.Instance{}, images: map[string]*vm.Image{}}
	f.server.Config.Handler = (&Server{Store: f.db, actionsVMClient: func(context.Context, store.ActionsCapacity, string) (vm.Provider, error) { return cloud, nil }}).Handler()
	if status, _ := f.call(t, "GET", "/settings/actions-capacity", f.reader1, nil); status != 403 {
		t.Fatalf("non-admin GET: %d", status)
	}
	status, out := f.call(t, "GET", "/settings/actions-capacity", f.admin, nil)
	if status != 200 || !strings.HasPrefix(out["external_id"].(string), "hakopod-") || len(out["installation_id"].(string)) != 32 {
		t.Fatalf("settings: %d %v", status, out)
	}
	capacity := map[string]any{"name": "aws-spot", "cloud": "aws", "region": "eu-central-1", "role_arn": "arn:aws:iam::123456789012:role/runner",
		"subnet_ids": []string{"subnet-0abc1234"}, "security_group_ids": []string{"sg-0abc1234"}, "instance_types": []string{"c7i.xlarge"},
		"architecture": "amd64", "spot": true, "max_instances": 5, "environments": []string{"*"}}
	put := map[string]any{"capacities": []any{capacity}, "expected_revision": out["revision"]}
	if status, _ = f.call(t, "PUT", "/settings/actions-capacity", f.reader1, put); status != 403 {
		t.Fatalf("non-admin PUT: %d", status)
	}
	if status, out = f.call(t, "PUT", "/settings/actions-capacity", f.admin, put); status != 200 || out["revision"] != float64(2) {
		t.Fatalf("save: %d %v", status, out)
	}
	if status, _ = f.call(t, "PUT", "/settings/actions-capacity", f.admin, put); status != 409 {
		t.Fatalf("stale revision: %d", status)
	}
	if status, _ = f.call(t, "POST", "/settings/actions-capacity/aws-spot/test", f.reader1, map[string]bool{}); status != 403 {
		t.Fatalf("non-admin test: %d", status)
	}
	if status, out = f.call(t, "POST", "/settings/actions-capacity/aws-spot/test", f.admin, map[string]bool{}); status != 200 || len(cloud.launches) != 0 {
		t.Fatalf("check must not launch: %d %v", status, out)
	}
	if status, out = f.call(t, "POST", "/settings/actions-capacity/aws-spot/test", f.admin, map[string]bool{"launch": true}); status != 200 || out["instance_type"] != "t3.micro" || len(cloud.terminated) != 1 || cloud.launches[0].Spot {
		t.Fatalf("launch test: %d %v", status, out)
	}
	if status, _ = f.call(t, "POST", "/settings/actions-capacity/missing/test", f.admin, map[string]bool{}); status != 404 {
		t.Fatalf("unknown capacity: %d", status)
	}
	if status, _ = f.call(t, "POST", "/settings/actions-capacity/aws-spot/build-image", f.admin, map[string]bool{}); status != 400 {
		t.Fatalf("build for runs-on-full: %d", status)
	}
	capacity["image"] = "hakopod-minimal"
	put["expected_revision"] = float64(2)
	if status, out = f.call(t, "PUT", "/settings/actions-capacity", f.admin, put); status != 200 {
		t.Fatalf("save hakopod-minimal: %d %v", status, out)
	}
	if status, _ = f.call(t, "POST", "/settings/actions-capacity/aws-spot/build-image", f.reader1, map[string]bool{}); status != 403 {
		t.Fatalf("non-admin build: %d", status)
	}
	if status, out = f.call(t, "POST", "/settings/actions-capacity/aws-spot/build-image", f.admin, map[string]bool{}); status != 202 || out["state"] != "building" {
		t.Fatalf("build: %d %v", status, out)
	}
	if status, _ = f.call(t, "POST", "/settings/actions-capacity/aws-spot/build-image", f.admin, map[string]bool{}); status != 409 {
		t.Fatalf("second build: %d", status)
	}
	if status, out = f.call(t, "GET", "/settings/actions-capacity", f.admin, nil); status != 200 || out["images"].(map[string]any)["aws-spot"].(map[string]any)["state"] != "building" {
		t.Fatalf("build state not shown: %d %v", status, out)
	}
}

func setCapacity(t *testing.T, s *Server, change func(*store.ActionsCapacity)) store.ActionsCapacity {
	t.Helper()
	ctx := context.Background()
	v, _, err := s.Store.ActionsCapacitySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	change(&v.Capacities[0])
	if _, err = s.Store.Pool.Exec(ctx, "UPDATE installation_settings SET value=$1 WHERE name='actions_capacity'", store.JSON(v)); err != nil {
		t.Fatal(err)
	}
	c, _, err := s.Store.ActionsCapacity(ctx, v.Capacities[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestActionsVMWarmPool(t *testing.T) {
	s, _, cloud, p := vmHarness(t, 1)
	ctx := context.Background()
	c := setCapacity(t, s, func(c *store.ActionsCapacity) { c.MaxInstances, c.WarmPool = 3, 2 })
	if c.Image != vm.ImageRunsOnFull {
		t.Fatal("default image", c.Image)
	}
	now := time.Now()
	if err := s.keepActionsStandbys(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	if len(cloud.launches) != 2 || cloud.launches[0].Spot || !cloud.launches[0].StopOnShutdown || cloud.launches[0].Image != "ami-runson" ||
		cloud.launches[0].Tags[vm.TagPoolState] != "standby" || cloud.launches[0].Tags[vm.TagImage] != "ami-runson" || strings.Contains(cloud.launches[0].UserData, "--jitconfig") {
		t.Fatalf("standbys: %+v", cloud.launches)
	}
	// Prepared standbys stop themselves; the sweep keeps them.
	for _, i := range cloud.instances {
		i.State = "stopped"
	}
	if err := s.keepActionsStandbys(ctx, c, now); err != nil || len(cloud.launches) != 2 || len(cloud.terminated) != 0 {
		t.Fatal("stable pool changed", err, cloud.terminated)
	}
	// An extra standby still booting is trimmed, not a ready one.
	extra := &vm.Instance{ID: "i-extra", State: "pending", LaunchedAt: now, Tags: maps.Clone(cloud.instances["i-1"].Tags)}
	cloud.instances["i-extra"] = extra
	for range 5 { // the fake lists in map order
		extra.State = "pending"
		cloud.terminated = nil
		if err := s.keepActionsStandbys(ctx, c, now); err != nil || len(cloud.terminated) != 1 || cloud.terminated[0] != "i-extra" {
			t.Fatal("trimmed a ready standby", err, cloud.terminated)
		}
	}
	delete(cloud.instances, "i-extra")
	cloud.terminated = nil
	// The reaper leaves standbys to the warm pool.
	if err := s.reapActionsCapacity(ctx, c, now); err != nil || len(cloud.terminated) != 0 {
		t.Fatal("reaper terminated a standby", err, cloud.terminated)
	}

	// A job claims a standby instead of launching.
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
		t.Fatal(err)
	}
	slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if err != nil || len(slots) != 1 || slots[0].InstanceSource != "standby" || len(cloud.claims) != 1 || len(cloud.launches) != 2 {
		t.Fatalf("standby not claimed: %+v %v %d", slots, err, len(cloud.launches))
	}
	claimed := cloud.instances[slots[0].InstanceID]
	if !strings.Contains(cloud.claims[0], "--jitconfig") || claimed.Tags[vm.TagSlot] != slots[0].ID || claimed.Tags[vm.TagPoolState] != "claimed" {
		t.Fatalf("claim: %+v", claimed)
	}
	// Until EC2 shows the start, the claimed standby reads as stopped.
	claimed.State = "stopped"
	if phase, err := s.slotRuntime(slots[0].Config).ActionsPodPhase(ctx, actionsTarget(p), slots[0].ID); err != nil || phase != "Pending" {
		t.Fatal("claimed standby", phase, err)
	}
	claimed.State = "running"

	// The pool refills within max_instances, and a new image replaces standbys.
	if err = s.keepActionsStandbys(ctx, c, now); err != nil || len(cloud.launches) != 3 {
		t.Fatal("pool not refilled", err, len(cloud.launches))
	}
	c = setCapacity(t, s, func(c *store.ActionsCapacity) { c.Image = "ami-0123456789abcdef0" })
	if err = s.keepActionsStandbys(ctx, c, now); err != nil || len(cloud.terminated) != 2 || len(cloud.launches) != 5 || cloud.launches[4].Image != "ami-0123456789abcdef0" {
		t.Fatal("standbys of the old image kept", err, cloud.terminated, len(cloud.launches))
	}
	// Turning the pool off terminates every standby; the claimed one stays.
	c = setCapacity(t, s, func(c *store.ActionsCapacity) { c.WarmPool = 0 })
	if err = s.keepActionsStandbys(ctx, c, now); err != nil || len(cloud.terminated) != 4 || claimed.State != "running" {
		t.Fatal("standbys kept after warm_pool = 0", err, cloud.terminated)
	}
}

func TestActionsImageBuild(t *testing.T) {
	s, _, cloud, p := vmHarness(t, 1)
	ctx := context.Background()
	c := setCapacity(t, s, func(c *store.ActionsCapacity) { c.Image = vm.ImageHakopodMinimal })
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err == nil || !strings.Contains(err.Error(), "build-image aws-spot") || len(cloud.launches) != 0 {
		t.Fatal("launched without an image", err)
	}
	if err := s.Store.RequestActionsImageBuild(ctx, store.Principal{}, c.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.RequestActionsImageBuild(ctx, store.Principal{}, c.Name); !errors.Is(err, store.ErrConflict) {
		t.Fatal("second build accepted", err)
	}
	step := func() store.ActionsCapacityImage {
		t.Helper()
		builds, err := s.Store.ActionsCapacityImages(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.buildActionsImage(ctx, c, builds[c.Name], time.Now()); err != nil {
			t.Fatal(err)
		}
		builds, _ = s.Store.ActionsCapacityImages(ctx)
		return builds[c.Name]
	}
	b := step()
	builder := cloud.launches[0]
	if b.BuilderID != "i-1" || !builder.StopOnShutdown || builder.DiskGiB != 8 || builder.Image != "" || builder.Tags[vm.TagPoolState] != "builder" || !strings.Contains(builder.UserData, "hakopod-runner.service") {
		t.Fatalf("builder: %+v %+v", b, builder)
	}
	if b = step(); b.BuildingImageID != "" {
		t.Fatal("imaged a running builder")
	}
	cloud.instances["i-1"].State = "stopped"
	if b = step(); b.BuildingImageID != "ami-1" || b.State != "building" {
		t.Fatalf("image not created: %+v", b)
	}
	cloud.images["ami-old"] = &vm.Image{ID: "ami-old", State: "available", Tags: map[string]string{vm.TagCapacity: c.Name}}
	cloud.images["ami-1"].State = "available"
	if b = step(); b.State != "available" || b.ImageID != "ami-1" || cloud.instances["i-1"].State != "terminated" {
		t.Fatalf("build not finished: %+v", b)
	}
	if step(); cloud.images["ami-old"] != nil || cloud.images["ami-1"] == nil {
		t.Fatal("older images not removed", cloud.images)
	}
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil || len(cloud.launches) != 2 || cloud.launches[1].Image != "ami-1" ||
		!strings.HasPrefix(cloud.launches[1].UserData, "#!/bin/bash") {
		t.Fatal("not launched from the built image", err)
	}
}
