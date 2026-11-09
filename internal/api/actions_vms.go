package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
			return r.s.Store.RecordActionsInstance(ctx, v.ID, i.ID, i.Type, i.Lifecycle)
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
	script, err := vm.RunnerUserData(c.Architecture, string(plain), v.Config.Actions.TimeoutMinutes+15)
	if err != nil {
		return err
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
	launched, err := p.Launch(ctx, vm.LaunchInput{Name: v.ID, InstanceTypes: types, Spot: spot, UserData: script,
		Tags: map[string]string{vm.TagCapacity: c.Name, vm.TagPool: v.ApplicationID + "/" + v.Service, vm.TagSlot: v.ID}})
	if errors.Is(err, vm.ErrNoCapacity) {
		// The registration is retired with this slot; the next one retries.
		if e := r.s.Store.UpdateActionsSlot(ctx, v.ID, v.RunnerID, "cleanup"); e != nil {
			return e
		}
	}
	if err != nil {
		return capacityError("Capacity %s: %s", c.Name, err)
	}
	return r.s.Store.RecordActionsInstance(ctx, v.ID, launched.ID, launched.Type, launched.Lifecycle)
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
			if err = r.s.Store.RecordActionsInstance(ctx, v.ID, i.ID, i.Type, i.Lifecycle); err != nil {
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
		switch {
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
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		step, cancel := context.WithTimeout(ctx, 50*time.Second)
		s.reapActionsVMs(step)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reapActionsVMs(ctx context.Context) {
	settings, _, err := s.Store.ActionsCapacitySettings(ctx)
	if err != nil {
		return
	}
	for _, c := range settings.Capacities {
		c, _ = settings.Find(c.Name)
		if err := s.reapActionsCapacity(ctx, c, time.Now()); err != nil {
			slog.Warn("Actions VM reaper could not sweep a capacity", "capacity", c.Name, "error", err.Error())
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
