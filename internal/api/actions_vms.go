package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/actions/vm"
	vmaws "github.com/hakopod/hakopod/internal/actions/vm/aws"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// actionsCapacityError carries a message safe to show on the pool: capacity
// names, limits and cloud error codes, never request bodies or credentials.
// full marks a capacity at max_instances, which is a wait, not a fault.
type actionsCapacityError struct {
	message string
	full    bool
}

func (e *actionsCapacityError) Error() string { return e.message }

func capacityError(format string, args ...any) error {
	return &actionsCapacityError{message: fmt.Sprintf(format, args...)}
}

func capacityFull(c store.ActionsCapacity) error {
	return &actionsCapacityError{message: fmt.Sprintf("Waiting for capacity %s: all %d instances are in use. Queued jobs start as others finish.", c.Name, c.MaxInstances), full: true}
}

// slotRuntime picks where a slot runs: the installation's pods, or a VM in the
// capacity its configuration names. Slot operations use the slot's own
// configuration so a pool moved between them still cleans up its old slots.
func (s *Server) slotRuntime(config spec.Service) actionsRuntime {
	if config.Actions != nil && config.Actions.Capacity != "" {
		return &vmActionsRuntime{actionsRuntime: s.actionRuntime(), s: s}
	}
	return s.actionRuntime()
}

// vmActionsRuntime is the pod runtime's seam for VM slots. Credentials and
// scope checks still come from the installation runtime.
type vmActionsRuntime struct {
	actionsRuntime
	s *Server
}

func (s *Server) actionsVMProvider(ctx context.Context, c store.ActionsCapacity) (vm.Provider, error) {
	installation, err := s.Store.InstallationID(ctx)
	if err != nil {
		return nil, err
	}
	key := string(store.JSON(c)) + "\x00" + installation
	if p, ok := s.actionsVMProviders.Load(key); ok {
		return p.(vm.Provider), nil
	}
	var p vm.Provider
	if s.actionsVMClient != nil {
		p, err = s.actionsVMClient(ctx, c, installation)
	} else {
		p, err = vmaws.New(ctx, vmaws.Config{Installation: installation, Region: c.Region, RoleARN: c.RoleARN, ExternalID: c.ExternalID,
			SubnetIDs: c.SubnetIDs, SecurityGroupIDs: c.SecurityGroupIDs, Architecture: c.Architecture, DiskGiB: c.DiskGiB})
	}
	if err != nil {
		return nil, capacityError("Capacity %s is unavailable: %s", c.Name, err)
	}
	// ponytail: one cached client per capacity revision; old ones stay until restart.
	s.actionsVMProviders.Store(key, p)
	return p, nil
}

// capacity resolves a slot's capacity. Launches also require that the pool's
// environment may use it; cleanup never does, so it cannot strand instances.
func (r *vmActionsRuntime) capacity(ctx context.Context, t cluster.Target, config spec.Service, launch bool) (store.ActionsCapacity, vm.Provider, error) {
	name := config.Actions.Capacity
	c, ok, err := r.s.Store.ActionsCapacity(ctx, name)
	if err != nil {
		return c, nil, err
	}
	if !ok {
		return c, nil, capacityError("Actions capacity %s no longer exists. Ask an administrator to restore it.", name)
	}
	if launch && !c.Allows(t.Project, t.Environment) {
		return c, nil, capacityError("Actions capacity %s is not available to %s/%s.", name, t.Project, t.Environment)
	}
	p, err := r.s.actionsVMProvider(ctx, c)
	return c, p, err
}

func (r *vmActionsRuntime) slot(ctx context.Context, id string) (*store.ActionsSlot, error) {
	v, err := r.s.Store.ActionsSlot(ctx, id)
	if err == nil && v != nil && (v.Config.Actions == nil || v.Config.Actions.Capacity == "") {
		return nil, errors.New("runner slot is not a VM slot")
	}
	return v, err
}

// instances finds every instance launched for a slot, including one whose
// launch response was lost before its ID was recorded.
func (r *vmActionsRuntime) instances(ctx context.Context, p vm.Provider, v store.ActionsSlot) ([]vm.Instance, error) {
	list, err := p.List(ctx, map[string]string{vm.TagSlot: v.ID})
	if err != nil {
		return nil, err
	}
	if v.InstanceID != "" && !slices.ContainsFunc(list, func(i vm.Instance) bool { return i.ID == v.InstanceID }) {
		// Tag listings are eventually consistent right after a launch.
		i, err := p.State(ctx, v.InstanceID)
		if err == nil {
			list = append(list, i)
		} else if !errors.Is(err, vm.ErrNotFound) {
			return nil, err
		}
	}
	return list, nil
}

// ActionsPoolAvailable checks what does not change per launch. The ceiling is
// checked by ActionsRoom before a runner is registered and enforced again
// when the instance is reserved.
func (r *vmActionsRuntime) ActionsPoolAvailable(ctx context.Context, t cluster.Target, config spec.Service) error {
	if len(r.s.authEncryptionKey()) != 32 {
		return capacityError("VM runner pools need the 32-byte authentication encryption key to hold runner configurations until launch.")
	}
	_, _, err := r.capacity(ctx, t, config, true)
	return err
}

func (r *vmActionsRuntime) ActionsRoom(ctx context.Context, t cluster.Target, config spec.Service) error {
	c, _, err := r.capacity(ctx, t, config, true)
	if err != nil {
		return err
	}
	used, err := r.s.Store.ActionsCapacityInUse(ctx, c.Name)
	if err != nil {
		return err
	}
	if used >= c.MaxInstances {
		return capacityFull(c)
	}
	return nil
}

func (r *vmActionsRuntime) SaveActionsConfig(ctx context.Context, t cluster.Target, _, id, config string) error {
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	sealed, err := r.s.encryptAuth([]byte(config))
	if err != nil {
		return err
	}
	return r.s.Store.SaveActionsVMConfig(ctx, id, sealed)
}

// The configuration is dropped at launch and with the slot row.
func (r *vmActionsRuntime) DeleteActionsConfig(context.Context, cluster.Target, string) error {
	return nil
}

// HasActionsConfig is true only before the first launch: a VM that went away
// is never relaunched with a configuration it may already have used.
func (r *vmActionsRuntime) HasActionsConfig(ctx context.Context, _ cluster.Target, id string) (bool, error) {
	v, err := r.slot(ctx, id)
	return err == nil && v != nil && v.InstanceID == "" && v.EncryptedRegistration != nil, err
}

func (r *vmActionsRuntime) StartActionsPod(ctx context.Context, t cluster.Target, service, id string, _ spec.Service) error {
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	v, err := r.slot(ctx, id)
	if err != nil || v == nil || v.InstanceID != "" {
		return err
	}
	// Entitlement is checked before every launch, as for pods.
	if err = r.s.Store.RequireActions(ctx, t.Project, t.Environment); err != nil {
		return err
	}
	c, p, err := r.capacity(ctx, t, v.Config, true)
	if err != nil {
		return err
	}
	existing, err := r.instances(ctx, p, *v)
	if err != nil {
		return capacityError("Capacity %s: %s", c.Name, err)
	}
	for _, i := range existing {
		if !i.Gone() {
			return r.s.Store.RecordActionsInstance(ctx, v.ID, i.ID, i.Type, i.Lifecycle, "")
		}
	}
	ok, err := r.s.Store.ReserveActionsInstance(ctx, v.ID, c.Name, c.MaxInstances)
	if err != nil {
		return err
	}
	if !ok {
		return capacityFull(c)
	}
	plain, err := r.s.decryptAuth(v.EncryptedRegistration)
	if err != nil {
		return errors.New("the runner configuration for this slot cannot be opened")
	}
	defer clear(plain)
	image, err := r.s.actionsImage(ctx, c, p)
	if err != nil {
		return err
	}
	script, err := vm.RunnerUserData(c.Image, c.Architecture, string(plain), v.Config.Actions.TimeoutMinutes+15)
	if err != nil {
		return err
	}
	tags := map[string]string{vm.TagCapacity: c.Name, vm.TagPool: v.ApplicationID + "/" + v.Service, vm.TagSlot: v.ID}
	if c.WarmPool > 0 {
		if err = actionsFence(ctx, t); err != nil {
			return err
		}
		if claimed, err := r.claimStandby(ctx, c, p, *v, image, script, tags); claimed || err != nil {
			return err
		}
	}
	hot, err := r.s.Store.EvictedActionsInstanceTypes(ctx, c.Name)
	if err != nil {
		return err
	}
	types := slices.DeleteFunc(slices.Clone(c.InstanceTypes), func(t string) bool { return slices.Contains(hot, t) })
	spot := c.Spot
	if len(types) == 0 {
		// Every type was interrupted repeatedly; stay off spot for now.
		types, spot = c.InstanceTypes, false
	}
	if spot {
		onDemand, err := r.s.Store.TakeActionsOnDemandLaunch(ctx, v.ApplicationID, v.Service)
		if err != nil {
			return err
		}
		spot = !onDemand
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	launched, err := p.Launch(ctx, vm.LaunchInput{Name: v.ID, InstanceTypes: types, Spot: spot, UserData: script, Image: image, Tags: tags})
	if errors.Is(err, vm.ErrNoCapacity) {
		// The registration is retired with this slot; the next one retries.
		if e := r.s.Store.UpdateActionsSlot(ctx, v.ID, v.RunnerID, "cleanup"); e != nil {
			return e
		}
	}
	if err != nil {
		return capacityError("Capacity %s: %s", c.Name, err)
	}
	return r.s.Store.RecordActionsInstance(ctx, v.ID, launched.ID, launched.Type, launched.Lifecycle, "cold")
}

// claimStandby starts a stopped standby of the capacity's current image for
// the slot. The database decides which slot gets a standby; a standby that
// cannot be started is terminated, and the slot then starts over like one
// whose instance went away. No standby left means a cold launch.
func (r *vmActionsRuntime) claimStandby(ctx context.Context, c store.ActionsCapacity, p vm.Provider, v store.ActionsSlot, image, script string, tags map[string]string) (bool, error) {
	standbys, err := p.List(ctx, map[string]string{vm.TagCapacity: c.Name, vm.TagPoolState: "standby", vm.TagImage: image})
	if err != nil {
		return false, capacityError("Capacity %s: %s", c.Name, err)
	}
	for _, i := range standbys {
		if i.State != "stopped" {
			continue
		}
		ok, err := r.s.Store.ClaimActionsStandby(ctx, v.ID, i.ID, i.Type)
		if err != nil || !ok {
			if err != nil {
				return false, err
			}
			continue
		}
		claimed := maps.Clone(tags)
		claimed[vm.TagPoolState] = "claimed"
		if err = p.Claim(ctx, i.ID, script, claimed); err != nil {
			_ = p.Terminate(context.WithoutCancel(ctx), i.ID)
			return true, capacityError("Capacity %s: %s", c.Name, err)
		}
		return true, nil
	}
	return false, nil
}

// actionsImage resolves the capacity's image for a launch.
func (s *Server) actionsImage(ctx context.Context, c store.ActionsCapacity, p vm.Provider) (string, error) {
	if c.Image == vm.ImageHakopodMinimal {
		images, err := s.Store.ActionsCapacityImages(ctx)
		if err != nil {
			return "", err
		}
		if images[c.Name].ImageID == "" {
			return "", capacityError("Capacity %s runs hakopod-minimal, which is not built yet. Run hakopod actions capacity build-image %s.", c.Name, c.Name)
		}
		return images[c.Name].ImageID, nil
	}
	image, err := p.Image(ctx, c.Image)
	if err != nil {
		return "", capacityError("Capacity %s: %s", c.Name, err)
	}
	return image, nil
}

// ActionsPodPhase maps the instance onto pod phases: Pending, Running,
// Succeeded once it shuts down, missing when the cloud has none. A spot
// interruption is recorded here, where its reason is visible.
func (r *vmActionsRuntime) ActionsPodPhase(ctx context.Context, t cluster.Target, id string) (string, error) {
	v, err := r.slot(ctx, id)
	if err != nil || v == nil {
		return "missing", err
	}
	c, p, err := r.capacity(ctx, t, v.Config, false)
	if err != nil {
		return "", err
	}
	list, err := r.instances(ctx, p, *v)
	if err != nil {
		return "", capacityError("Capacity %s: %s", c.Name, err)
	}
	phase := "missing"
	for _, i := range list {
		if v.InstanceID == "" && !i.Gone() {
			// The launch response was lost; adopt the instance.
			if err = r.s.Store.RecordActionsInstance(ctx, v.ID, i.ID, i.Type, i.Lifecycle, ""); err != nil {
				return "", err
			}
			v.InstanceID = i.ID
		}
		if i.ID != v.InstanceID {
			continue
		}
		if i.Interrupted() {
			if err = r.s.Store.RecordActionsEviction(ctx, *v, c.Name, i.ID, i.Type); err != nil {
				return "", err
			}
		}
		// A claimed standby reads as stopped until its start shows up.
		starting := v.InstanceSource == "standby" && (i.State == "stopped" || i.State == "stopping") &&
			v.InstanceReservedAt != nil && time.Since(*v.InstanceReservedAt) < 3*time.Minute
		switch {
		case starting:
			phase = "Pending"
		case i.Gone():
			phase = "Succeeded"
		case i.State == "running":
			phase = "Running"
		default:
			phase = "Pending"
		}
	}
	return phase, nil
}

// recordActionsInterruption looks a failed job's VM up before its run is
// attached: the failure can arrive before a sweep sees the interruption,
// and a run is only attached to an eviction already on record. Pod slots
// and lookup errors are left to the sweeps.
func (s *Server) recordActionsInterruption(ctx context.Context, slot string) {
	v, err := s.Store.ActionsSlot(ctx, slot)
	if err != nil || v == nil || v.Config.Actions == nil || v.Config.Actions.Capacity == "" {
		return
	}
	if _, err = (&vmActionsRuntime{s: s}).ActionsPodPhase(ctx, cluster.Target{}, slot); err != nil {
		slog.Warn("Actions VM could not be checked for an interruption", "slot", slot, "error", err.Error())
	}
}

// DeleteActionsPod terminates every instance of the slot and reports done once
// none is left running.
func (r *vmActionsRuntime) DeleteActionsPod(ctx context.Context, t cluster.Target, id string) (bool, error) {
	if err := actionsFence(ctx, t); err != nil {
		return false, err
	}
	v, err := r.slot(ctx, id)
	if err != nil || v == nil {
		return v == nil && err == nil, err
	}
	c, p, err := r.capacity(ctx, t, v.Config, false)
	if err != nil {
		return false, err
	}
	list, err := r.instances(ctx, p, *v)
	if err != nil {
		return false, capacityError("Capacity %s: %s", c.Name, err)
	}
	gone := true
	for _, i := range list {
		if i.State == "terminated" || i.State == "shutting-down" {
			continue
		}
		if err = p.Terminate(ctx, i.ID); err != nil {
			return false, capacityError("Capacity %s: %s", c.Name, err)
		}
		gone = false
	}
	return gone, nil
}

// runActionsVMReaper sweeps each capacity for instances the slot lifecycle no
// longer accounts for. GitHub runners of vanished instances are removed by the
// pool reconcile, which sees the instance missing and cleans the slot up.
func (s *Server) runActionsVMReaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		step, cancel := context.WithTimeout(ctx, 25*time.Second)
		s.reapActionsVMs(step)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// One worker at a time sweeps, keeps warm pools and builds images, so
// replicas do not launch standbys or builders twice.
func (s *Server) reapActionsVMs(ctx context.Context) {
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended('hakopod.actions.vms',0))").Scan(&locked); err != nil || !locked {
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, e := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended('hakopod.actions.vms',0))"); e != nil {
			conn.Conn().Close(cleanup)
		}
	}()
	settings, _, err := s.Store.ActionsCapacitySettings(ctx)
	if err != nil {
		return
	}
	builds, err := s.Store.ActionsCapacityImages(ctx)
	if err != nil {
		return
	}
	for _, c := range settings.Capacities {
		c, _ = settings.Find(c.Name)
		now := time.Now()
		if err := s.reapActionsCapacity(ctx, c, now); err != nil {
			slog.Warn("Actions VM reaper could not sweep a capacity", "capacity", c.Name, "error", err.Error())
		}
		if b, ok := builds[c.Name]; ok || c.Image == vm.ImageHakopodMinimal {
			if err := s.buildActionsImage(ctx, c, b, now); err != nil {
				slog.Warn("Actions image build could not advance", "capacity", c.Name, "error", err.Error())
			}
		}
		if err := s.keepActionsStandbys(ctx, c, now); err != nil {
			slog.Warn("Actions warm pool could not be kept", "capacity", c.Name, "error", err.Error())
		}
	}
	if err := s.rerunEvictedJobs(ctx); err != nil {
		slog.Warn("Actions spot reruns could not be checked", "error", err.Error())
	}
}

// rerunEvictedJobs asks GitHub to rerun the failed jobs of runs that lost a
// runner to a spot interruption, at most spot_retries attempts per run. Each
// rerun grants the pool on-demand launches for the jobs it lost. Outcomes,
// including a refusal, are kept on the eviction and shown in pool status.
func (s *Server) rerunEvictedJobs(ctx context.Context) error {
	pending, err := s.Store.PendingActionsReruns(ctx)
	if err != nil {
		return err
	}
	for _, e := range pending {
		p, err := s.Store.ActionsPool(ctx, e.ApplicationID, e.Service)
		if err != nil {
			return err
		}
		if p == nil || p.Config.Actions == nil {
			continue
		}
		jobs, err := s.Store.ClaimActionsRerun(ctx, e, p.Config.Actions.EffectiveSpotRetries())
		if err != nil || jobs == 0 {
			if err != nil {
				return err
			}
			continue
		}
		failure := ""
		config := p.Config
		jobsAccess := *config.Actions
		jobsAccess.Credential = jobsAccess.EffectiveJobsCredential()
		config.Actions = &jobsAccess
		client, err := s.runnerClient(ctx, actionsTarget(*p), config)
		if err == nil {
			rerun, ok := client.(interface {
				RerunFailedJobs(context.Context, string, int64) error
			})
			if !ok {
				return errors.New("the runner provider cannot rerun jobs")
			}
			err = rerun.RerunFailedJobs(ctx, e.Repository, e.RunID)
		}
		var status *actions.StatusError
		switch {
		case errors.As(err, &status) && (status.Status == 403 || status.Status == 404):
			failure = fmt.Sprintf("GitHub refused the rerun (HTTP %d). Give the pool's jobs credential Actions write permission on %s.", status.Status, e.Repository)
		case err != nil:
			failure = "The rerun could not be requested: " + safeActionsError(err)
		}
		if err = s.Store.FinishActionsRerun(ctx, e, jobs, failure); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) reapActionsCapacity(ctx context.Context, c store.ActionsCapacity, now time.Time) error {
	p, err := s.actionsVMProvider(ctx, c)
	if err != nil {
		return err
	}
	list, err := p.List(ctx, map[string]string{vm.TagCapacity: c.Name})
	if err != nil {
		return err
	}
	ids := []string{}
	for _, i := range list {
		ids = append(ids, i.Tags[vm.TagSlot])
	}
	slots, err := s.Store.ActionsVMSlots(ctx, ids)
	if err != nil {
		return err
	}
	for _, i := range list {
		if i.Gone() {
			continue
		}
		if reason := reapReason(i, slots, now); reason != "" {
			if err = p.Terminate(ctx, i.ID); err != nil {
				return err
			}
			slog.Info("Terminated an Actions VM", "capacity", c.Name, "instance", i.ID, "reason", reason)
		}
	}
	return nil
}

// reapReason says why an instance should not be running, or "" if it may.
// A slot in cleanup is left alone: a draining slot is marked cleanup while its
// job still runs, and the reconcile terminates it once the runner is idle.
func reapReason(i vm.Instance, slots map[string]store.ActionsVMSlot, now time.Time) string {
	if state := i.Tags[vm.TagPoolState]; state == "standby" || state == "builder" {
		return "" // the warm pool and image builds keep their own
	}
	v, ok := slots[i.Tags[vm.TagSlot]]
	limit := 360 * time.Minute
	if ok && v.Config.Actions != nil {
		limit = time.Duration(v.Config.Actions.TimeoutMinutes) * time.Minute
	}
	switch {
	case !ok:
		return "no slot"
	case v.PoolRemoved:
		return "pool removed"
	case v.InstanceID != "" && v.InstanceID != i.ID:
		return "duplicate launch"
	case !i.LaunchedAt.IsZero() && now.Sub(i.LaunchedAt) > limit+15*time.Minute:
		return "timed out"
	}
	return ""
}

// keepActionsStandbys holds the capacity's warm pool at warm_pool stopped
// standbys of its current image, within the room max_instances leaves after
// running slots. Standbys of another image, extras and any stuck booting or
// stopping are terminated. A standby is on-demand: EC2 cannot stop spot
// instances launched the way runners are.
// ponytail: slots reserve against max_instances without counting standbys;
// extras are trimmed here on the next sweep instead.
func (s *Server) keepActionsStandbys(ctx context.Context, c store.ActionsCapacity, now time.Time) error {
	p, err := s.actionsVMProvider(ctx, c)
	if err != nil {
		return err
	}
	list, err := p.List(ctx, map[string]string{vm.TagCapacity: c.Name, vm.TagPoolState: "standby"})
	if err != nil || len(list) == 0 && c.WarmPool == 0 {
		return err
	}
	image, want := "", int64(0)
	if c.WarmPool > 0 {
		if image, err = s.actionsImage(ctx, c, p); err != nil {
			return err
		}
		used, err := s.Store.ActionsCapacityInUse(ctx, c.Name)
		if err != nil {
			return err
		}
		want = max(0, min(c.WarmPool, c.MaxInstances-used))
	}
	// Ready standbys are kept before ones still booting, so trimming an
	// extra never throws away a warmed-up instance for a cold one.
	slices.SortStableFunc(list, func(a, b vm.Instance) int {
		return cmp.Compare(standbyRank(a), standbyRank(b))
	})
	kept := int64(0)
	for _, i := range list {
		if i.State == "terminated" || i.State == "shutting-down" {
			continue
		}
		reason := ""
		switch {
		case i.Tags[vm.TagImage] != image:
			reason = "image changed"
		case i.State != "stopped" && now.Sub(i.LaunchedAt) > 15*time.Minute:
			reason = "stuck " + i.State
		case kept >= want:
			reason = "not needed"
		}
		if reason == "" {
			kept++
			continue
		}
		if err = p.Terminate(ctx, i.ID); err != nil {
			return err
		}
		slog.Info("Terminated an Actions standby", "capacity", c.Name, "instance", i.ID, "reason", reason)
	}
	if kept >= want {
		return nil
	}
	script, err := vm.RunnerUserData(c.Image, c.Architecture, "", 0)
	if err != nil {
		return err
	}
	for ; kept < want; kept++ {
		_, err = p.Launch(ctx, vm.LaunchInput{Name: "standby-" + store.NewID()[:12], InstanceTypes: c.InstanceTypes, UserData: script, Image: image, StopOnShutdown: true,
			Tags: map[string]string{vm.TagCapacity: c.Name, vm.TagPoolState: "standby", vm.TagImage: image}})
		if err != nil {
			return err
		}
	}
	return nil
}

func standbyRank(i vm.Instance) int {
	if i.State == "stopped" {
		return 0
	}
	return 1
}

// buildActionsImage advances a capacity's hakopod-minimal build by one step
// per sweep: launch a builder from stock Ubuntu, image it once it has powered
// itself off, wait for the image, then retire the builder and older images.
// Builders and images the record does not name are removed on the way.
func (s *Server) buildActionsImage(ctx context.Context, c store.ActionsCapacity, b store.ActionsCapacityImage, now time.Time) error {
	p, err := s.actionsVMProvider(ctx, c)
	if err != nil {
		return err
	}
	builders, err := p.List(ctx, map[string]string{vm.TagCapacity: c.Name, vm.TagPoolState: "builder"})
	if err != nil {
		return err
	}
	for _, i := range builders {
		if i.ID != b.BuilderID && i.State != "terminated" && i.State != "shutting-down" {
			if err = p.Terminate(ctx, i.ID); err != nil {
				return err
			}
		}
	}
	images, err := p.Images(ctx, map[string]string{vm.TagCapacity: c.Name})
	if err != nil {
		return err
	}
	for _, i := range images {
		if i.ID != b.ImageID && i.ID != b.BuildingImageID {
			if err = p.DeleteImage(ctx, i.ID); err != nil {
				return err
			}
		}
	}
	if b.State != "building" {
		return nil
	}
	fail := func(message string) error {
		if b.BuilderID != "" {
			_ = p.Terminate(ctx, b.BuilderID)
		}
		if b.BuildingImageID != "" {
			_ = p.DeleteImage(ctx, b.BuildingImageID)
		}
		b.State, b.BuilderID, b.BuildingImageID, b.Message = "failed", "", "", message
		return s.Store.SaveActionsCapacityImage(ctx, b)
	}
	switch {
	case now.Sub(b.RequestedAt) > 45*time.Minute:
		message := "The image build did not finish within 45 minutes."
		if b.BuilderID != "" && b.BuildingImageID == "" {
			if out, err := p.ConsoleOutput(ctx, b.BuilderID); err == nil {
				message += " The builder's console ends with: " + tailText(out, 300)
			}
		}
		return fail(message)
	case b.BuilderID == "":
		script, err := vm.ImageUserData(c.Architecture)
		if err != nil {
			return fail(err.Error())
		}
		i, err := p.Launch(ctx, vm.LaunchInput{Name: "image-" + c.Name, InstanceTypes: c.InstanceTypes, UserData: script, StopOnShutdown: true, DiskGiB: 8,
			Tags: map[string]string{vm.TagCapacity: c.Name, vm.TagPoolState: "builder"}})
		if err != nil {
			return fail("The builder could not be launched: " + err.Error())
		}
		b.BuilderID, b.Message = i.ID, "Installing Docker and the runner on "+i.ID+"."
		return s.Store.SaveActionsCapacityImage(ctx, b)
	case b.BuildingImageID == "":
		i, err := p.State(ctx, b.BuilderID)
		if errors.Is(err, vm.ErrNotFound) || err == nil && (i.State == "terminated" || i.State == "shutting-down") {
			return fail("The builder instance went away before it finished.")
		}
		if err != nil || i.State != "stopped" {
			return err // still installing; a failed install keeps running until the time limit
		}
		id, err := p.CreateImage(ctx, b.BuilderID, "hakopod-minimal-"+c.Name+"-"+now.UTC().Format("20060102-150405"), map[string]string{vm.TagCapacity: c.Name})
		if err != nil {
			return fail("The image could not be created: " + err.Error())
		}
		b.BuildingImageID, b.Message = id, "Waiting for image "+id+"."
		return s.Store.SaveActionsCapacityImage(ctx, b)
	}
	for _, i := range images {
		if i.ID != b.BuildingImageID {
			continue
		}
		switch i.State {
		case "available":
			if err = p.Terminate(ctx, b.BuilderID); err != nil {
				return err
			}
			b.State, b.ImageID, b.BuilderID, b.BuildingImageID, b.Message = "available", i.ID, "", "", ""
			return s.Store.SaveActionsCapacityImage(ctx, b) // the next sweep removes the previous image
		case "failed":
			return fail("AWS could not create the image.")
		}
	}
	return nil
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
