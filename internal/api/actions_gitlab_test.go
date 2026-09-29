package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

const gitlabLifecycleToken = "glrt-development-fixture-private-registration"

// Provider and runtime behavior are deliberate development fixtures. Persistence
// and compare-and-swap recovery use an isolated real PostgreSQL database.
type gitlabLifecycleFake struct {
	*actionsFake
	t                                        *testing.T
	server                                   *Server
	pool                                     store.ActionsPool
	runtime                                  cluster.GitLabActionsRuntime
	runners                                  map[string]actions.ProviderRunner
	targets                                  map[string]actions.ProviderTarget
	paused                                   map[string]bool
	policies                                 map[string]bool
	starts                                   []string
	credentials                              []string
	providerOps                              int
	nextID                                   int
	podPhase                                 string
	registerLost, configLost                 bool
	startLost, startDidNotCommit             bool
	deleteLost, podDeleting                  bool
	policyDeleteFails, findFails             bool
	afterRegister                            func()
	jobs                                     map[string]actions.ProviderJob
	readFailure, reuseJobs                   bool
	traceCalls, cancelCalls, managerLogReads int
	afterTrace, beforeCancel                 func()
}

func (f *gitlabLifecycleFake) Capabilities() actions.ProviderCapabilities {
	return actions.ProviderCapabilities{Provider: actions.ProviderGitLab, Available: false}
}
func (f *gitlabLifecycleFake) Register(_ context.Context, target actions.ProviderTarget, name string, _ []string) (actions.ProviderRegistration, error) {
	f.providerOps++
	f.nextID++
	id := fmt.Sprint(f.nextID)
	runner := actions.ProviderRunner{ID: id, Name: name, Status: "online", ObservedAt: time.Now().UTC()}
	f.runners[id], f.targets[id] = runner, target
	if f.afterRegister != nil {
		f.afterRegister()
	}
	if f.registerLost {
		f.registerLost = false
		return actions.ProviderRegistration{}, &actions.GitLabError{Kind: "transport", Ambiguous: true}
	}
	data, _ := json.Marshal(actions.GitLabManagerConfig{SchemaVersion: 1, URL: target.GitLab.URL, RunnerID: id, Name: name, Token: gitlabLifecycleToken, TimeoutSeconds: 900})
	return actions.ProviderRegistration{Runner: runner, ManagerConfig: data, CleanupCredential: []byte(gitlabLifecycleToken)}, nil
}
func (f *gitlabLifecycleFake) Get(ctx context.Context, target actions.ProviderTarget, id string) (actions.ProviderRunner, error) {
	return f.GetOwned(ctx, target, id, "")
}
func (f *gitlabLifecycleFake) GetOwned(_ context.Context, target actions.ProviderTarget, id, name string) (actions.ProviderRunner, error) {
	f.providerOps++
	runner, ok := f.runners[id]
	if !ok {
		return runner, actions.ErrRunnerAbsent
	}
	if !reflect.DeepEqual(target, f.targets[id]) || (name != "" && runner.Name != name) {
		return actions.ProviderRunner{}, &actions.GitLabError{Kind: "identity"}
	}
	runner.ObservedAt = time.Now().UTC()
	return runner, nil
}
func (f *gitlabLifecycleFake) FindOwned(_ context.Context, target actions.ProviderTarget, name string) ([]actions.ProviderRunner, error) {
	f.providerOps++
	if f.findFails {
		return nil, &actions.GitLabError{Kind: "bounds"}
	}
	runners := []actions.ProviderRunner{}
	for id, runner := range f.runners {
		if runner.Name == name && reflect.DeepEqual(target, f.targets[id]) {
			runners = append(runners, runner)
		}
	}
	sort.Slice(runners, func(i, j int) bool { return runners[i].ID < runners[j].ID })
	return runners, nil
}
func (f *gitlabLifecycleFake) Drain(_ context.Context, target actions.ProviderTarget, id string) error {
	if !reflect.DeepEqual(target, f.targets[id]) {
		return &actions.GitLabError{Kind: "scope"}
	}
	f.paused[id] = true
	return nil
}
func (f *gitlabLifecycleFake) DrainOwned(ctx context.Context, target actions.ProviderTarget, id, name string) error {
	if _, err := f.GetOwned(ctx, target, id, name); err != nil {
		return err
	}
	return f.Drain(ctx, target, id)
}
func (f *gitlabLifecycleFake) Delete(ctx context.Context, target actions.ProviderTarget, id string) error {
	return f.DeleteOwned(ctx, target, id, "")
}
func (f *gitlabLifecycleFake) DeleteOwned(ctx context.Context, target actions.ProviderTarget, id, name string) error {
	f.providerOps++
	runner, ok := f.runners[id]
	if !ok {
		return actions.ErrRunnerAbsent
	}
	if !reflect.DeepEqual(target, f.targets[id]) || runner.Name != name {
		return &actions.GitLabError{Kind: "identity"}
	}
	if err := f.Drain(ctx, target, id); err != nil {
		return err
	}
	if runner.Busy {
		return &actions.GitLabError{Kind: "busy"}
	}
	delete(f.runners, id)
	if f.deleteLost {
		f.deleteLost = false
		return &actions.GitLabError{Kind: "transport", Ambiguous: true}
	}
	return nil
}
func (f *gitlabLifecycleFake) ActionsCredential(_ context.Context, target cluster.Target, reference string) (string, error) {
	config := target.Spec.Services[f.pool.Service].Actions
	if config.Credential != reference && config.EffectiveJobsCredential() != reference {
		f.t.Fatal("credential lookup used a different revision from its slot")
	}
	f.credentials = append(f.credentials, reference)
	return "glpat-development-fixture-management", nil
}
func (f *gitlabLifecycleFake) SaveGitLabActionsConfig(ctx context.Context, target cluster.Target, service, id string, config spec.Service, registration actions.GitLabManagerConfig, runtime cluster.GitLabActionsRuntime) error {
	f.t.Helper()
	slot := f.slot(id)
	if !reflect.DeepEqual(target.Spec.Services[service], slot.Config) || !reflect.DeepEqual(config, slot.Config) || slot.ProviderRunnerID != registration.RunnerID || len(slot.EncryptedRegistration) == 0 || bytes.Contains(slot.EncryptedRegistration, []byte(gitlabLifecycleToken)) {
		f.t.Fatal("runtime started before private registration was durably bound to its original slot")
	}
	if f.configLost {
		f.configLost = false
		return errors.New("development fixture lost secret response")
	}
	f.configs[id], f.policies[id] = true, true
	return nil
}
func (f *gitlabLifecycleFake) StartGitLabActionsPod(_ context.Context, _ cluster.Target, _ string, id string, _ spec.Service, _ actions.GitLabManagerConfig, _ cluster.GitLabActionsRuntime) error {
	f.t.Helper()
	if !f.slot(id).ManagerLaunchAttempted || !f.configs[id] || !f.policies[id] {
		f.t.Fatal("manager started without durable launch fence and private artifacts")
	}
	f.starts = append(f.starts, id)
	if !f.startDidNotCommit {
		f.pods[id] = true
		slot := f.slot(id)
		f.jobs[slot.ProviderRunnerID] = actions.ProviderJob{Identity: actions.ProviderJobIdentity{RunnerID: slot.ProviderRunnerID, RunnerName: "hakopod-" + id, Repository: "12", RunID: "21", JobID: "job-" + slot.ProviderRunnerID}, Name: "development-job", Status: "completed", Conclusion: "success", Steps: []actions.Step{}}
	}
	if f.startLost {
		f.startLost = false
		return errors.New("development fixture lost pod response")
	}
	return nil
}
func (f *gitlabLifecycleFake) DeleteActionsPod(_ context.Context, _ cluster.Target, id string) (bool, error) {
	for _, runner := range f.runners {
		if runner.Name == "hakopod-"+id {
			f.t.Fatal("pod deleted before provider absence was confirmed")
		}
	}
	if f.podDeleting {
		return false, nil
	}
	delete(f.pods, id)
	return true, nil
}
func (f *gitlabLifecycleFake) ActionsPodPhase(ctx context.Context, target cluster.Target, id string) (string, error) {
	if f.podPhase != "" && f.pods[id] {
		return f.podPhase, nil
	}
	return f.actionsFake.ActionsPodPhase(ctx, target, id)
}
func (f *gitlabLifecycleFake) DeleteActionsConfig(_ context.Context, _ cluster.Target, id string) error {
	if f.pods[id] {
		f.t.Fatal("registration removed while the pod still exists")
	}
	delete(f.configs, id)
	return nil
}
func (f *gitlabLifecycleFake) DeleteGitLabActionsConfig(ctx context.Context, target cluster.Target, id string) error {
	return f.DeleteActionsConfig(ctx, target, id)
}
func (f *gitlabLifecycleFake) DeleteGitLabActionsPolicy(_ context.Context, _ cluster.Target, id string) error {
	if f.pods[id] {
		f.t.Fatal("policy removed while the pod still exists")
	}
	if f.policyDeleteFails {
		return errors.New("development fixture policy deletion unavailable")
	}
	delete(f.policies, id)
	return nil
}
func (f *gitlabLifecycleFake) slot(id string) store.ActionsSlot {
	f.t.Helper()
	slots, err := f.server.Store.ActionsSlots(context.Background(), f.pool.ApplicationID, f.pool.Service)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, slot := range slots {
		if slot.ID == id {
			return slot
		}
	}
	f.t.Fatal("fixture slot disappeared")
	return store.ActionsSlot{}
}

func gitlabLifecycleHarness(t *testing.T, db *store.Store) (*Server, *gitlabLifecycleFake, store.ActionsPool) {
	t.Helper()
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	// Synthetic hashes and passed status exercise validation only. They are not
	// qualification evidence or published image references.
	runtime := cluster.GitLabActionsRuntime{Images: cluster.GitLabActionsImages{
		Manager: "ghcr.io/hakopod/gitlab-runner@sha256:" + strings.Repeat("a", 64), Helper: "ghcr.io/hakopod/gitlab-runner-helper@sha256:" + strings.Repeat("b", 64), DefaultJobImage: "docker.io/library/debian@sha256:" + strings.Repeat("c", 64),
		Architecture: "amd64", Status: "passed", RunnerVersion: "19.4.1", SourceCommit: "3c39fcebf73d01d464db3dee8a5267155273a6c5",
		TransportSourceSHA256: strings.Repeat("d", 64), ManagerBinarySHA256: strings.Repeat("e", 64), HelperBinarySHA256: strings.Repeat("f", 64), VerificationReportSHA256: strings.Repeat("1", 64),
	}, TransportPolicy: cluster.GitLabActionsTransportPolicy{SchemaVersion: 1, CoordinatorURL: "https://gitlab.com", ArtifactOrigins: []string{}}}
	config := spec.Service{Image: runtime.Images.Manager, Architecture: "amd64", Replicas: 1, Size: "compute", Actions: &spec.Actions{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}, Credential: "original-management", Labels: []string{"development"}, TimeoutMinutes: 15, WorkspaceSizeGiB: 8}}
	app := store.Application{ID: store.NewID(), Project: "native-fixture", Environment: "development", Name: "native-fixture"}
	app.Name = "native-" + app.ID[:12]
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO projects(name) VALUES($1) ON CONFLICT DO NOTHING`, app.Project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO environments(project,name) VALUES($1,$2) ON CONFLICT DO NOTHING`, app.Project, app.Environment); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO applications(id,project,environment,name,spec) VALUES($1,$2,$3,$4,'{}')`, app.ID, app.Project, app.Environment, app.Name); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncActions(context.Background(), app, spec.Application{Name: app.Name, Services: map[string]spec.Service{"runner": config}}, 1); err != nil {
		t.Fatal(err)
	}
	pool, err := db.ActionsPool(context.Background(), app.ID, "runner")
	if err != nil || pool == nil {
		t.Fatal("missing development fixture pool", err)
	}
	f := &gitlabLifecycleFake{actionsFake: &actionsFake{pods: map[string]bool{}, configs: map[string]bool{}}, t: t, pool: *pool, runtime: runtime, runners: map[string]actions.ProviderRunner{}, targets: map[string]actions.ProviderTarget{}, paused: map[string]bool{}, policies: map[string]bool{}}
	f.jobs = map[string]actions.ProviderJob{}
	s := &Server{Store: db, actionsTestRuntime: f, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}}
	s.actionsGitLabClient = func(_ context.Context, _ cluster.Target, _ spec.Service, _ string) (gitlabRunnerProvider, error) {
		return f, nil
	}
	s.actionsGitLabRuntime = func(_ context.Context, _ cluster.Target, _ spec.Service) (cluster.GitLabActionsRuntime, error) {
		return f.runtime, nil
	}
	s.actionsGitLabJobsClient = func(context.Context, cluster.Target, spec.Service, string) (gitlabWorkflowProvider, error) {
		if f.readFailure {
			return nil, &actions.GitLabError{Status: 403, Kind: "status"}
		}
		return f, nil
	}
	f.server = s
	return s, f, *pool
}

func gitlabReconcile(t *testing.T, s *Server, pool store.ActionsPool) {
	t.Helper()
	if err := s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
}

func gitlabCleaned(t *testing.T, s *Server, f *gitlabLifecycleFake, pool store.ActionsPool) {
	t.Helper()
	pool.Config.Suspended = true
	for pass := 0; pass < 8; pass++ {
		before := f.providerOps
		gitlabReconcile(t, s, pool)
		if f.providerOps-before > 3 {
			t.Fatal("cleanup exceeded one inventory read and two exact job reads in a bounded pass")
		}
		slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) == 0 {
			if len(f.runners)+len(f.pods)+len(f.configs)+len(f.policies) != 0 {
				t.Fatal("database cleanup completed before external resources were removed")
			}
			return
		}
	}
	t.Fatal("bounded cleanup did not converge")
}

type gitlabStoreFault struct {
	*store.Store
	saveBefore, saveAfter, launchAfter bool
}

func (f *gitlabStoreFault) SaveActionsProviderRegistration(ctx context.Context, slot store.ActionsSlot, id string, sealed []byte) error {
	if f.saveBefore {
		f.saveBefore = false
		return errors.New("development fixture database write failed")
	}
	err := f.Store.SaveActionsProviderRegistration(ctx, slot, id, sealed)
	if err == nil && f.saveAfter {
		f.saveAfter = false
		return errors.New("development fixture lost committed response")
	}
	return err
}
func (f *gitlabStoreFault) MarkActionsProviderLaunch(ctx context.Context, slot store.ActionsSlot) (store.ActionsSlot, error) {
	current, err := f.Store.MarkActionsProviderLaunch(ctx, slot)
	if err == nil && f.launchAfter {
		f.launchAfter = false
		return store.ActionsSlot{}, errors.New("development fixture lost launch fence response")
	}
	return current, err
}

func TestGitLabLifecyclePersistsEncryptedRegistrationBeforeLaunchAndNeverReplays(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	s.actionsGitLabTestStore = &gitlabStoreFault{Store: s.Store, saveAfter: true}
	gitlabReconcile(t, s, pool)
	if f.nextID != 1 || len(f.starts) != 1 {
		t.Fatal("lost committed registration response did not recover exactly once")
	}
	slot := f.slot(f.starts[0])
	if !slot.ManagerLaunchAttempted || slot.ProviderRunnerID != "1" || slot.Phase != "starting" {
		t.Fatal("launch fence was not durable")
	}
	public, _ := json.Marshal(slot)
	if bytes.Contains(public, []byte(gitlabLifecycleToken)) || bytes.Contains(public, []byte("registration")) || bytes.Contains(public, []byte("manager_launch")) {
		t.Fatal("private lifecycle material appeared in public slot JSON")
	}
	gitlabReconcile(t, s, pool)
	if f.slot(slot.ID).Phase != "online" {
		t.Fatal("live owned manager did not become online")
	}
	delete(f.pods, slot.ID)
	gitlabCleaned(t, s, f, pool)
	if len(f.starts) != 1 || f.nextID != 1 {
		t.Fatal("disappeared one-job manager was replayed")
	}
}

func TestGitLabLifecycleAmbiguousOperationsRetainRecoveryIntent(t *testing.T) {
	db := actionsDatabase(t)
	for _, failure := range []string{"register", "save-before", "launch-fence", "pod-not-created", "pod-created"} {
		t.Run(failure, func(t *testing.T) {
			s, f, pool := gitlabLifecycleHarness(t, db)
			fault := &gitlabStoreFault{Store: db}
			s.actionsGitLabTestStore = fault
			switch failure {
			case "register":
				f.registerLost = true
			case "save-before":
				fault.saveBefore = true
			case "launch-fence":
				fault.launchAfter = true
			case "pod-not-created":
				f.startLost, f.startDidNotCommit = true, true
			case "pod-created":
				f.startLost = true
			}
			if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil {
				t.Fatal("ambiguous operation did not retain its error")
			}
			if failure == "pod-created" {
				gitlabReconcile(t, s, pool)
				if f.slot(f.starts[0]).Phase != "online" {
					t.Fatal("committed pod was not recovered")
				}
			}
			gitlabCleaned(t, s, f, pool)
			if f.nextID != 1 || len(f.starts) > 1 || (failure == "launch-fence" && len(f.starts) != 0) {
				t.Fatal("ambiguous operation retried registration or manager launch")
			}
		})
	}
}

func TestGitLabLifecycleSavedButUnlaunchedRegistrationResumes(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	f.configLost = true
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil {
		t.Fatal("secret failure not surfaced")
	}
	slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
	if err != nil || len(slots) != 1 || slots[0].ManagerLaunchAttempted || slots[0].Phase != "starting" {
		t.Fatal("recoverable saved registration lost", err)
	}
	gitlabReconcile(t, s, pool)
	if f.nextID != 1 || len(f.starts) != 1 {
		t.Fatal("saved private registration was not resumed exactly once")
	}
}

func TestGitLabLifecycleDrainKeepsBusyJobsAndWaitsForPodAndPolicyCleanup(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	runner := f.runners["1"]
	runner.Busy = true
	f.runners["1"] = runner
	pool.Config.Suspended = true
	gitlabReconcile(t, s, pool)
	if !f.paused["1"] || len(f.runners) != 1 || len(f.pods) != 1 {
		t.Fatal("drain did not stop acquisition while preserving the active job")
	}
	runner.Busy = false
	f.runners["1"] = runner
	gitlabReconcile(t, s, pool)
	f.podDeleting = true
	for pass := 0; pass < 3; pass++ {
		gitlabReconcile(t, s, pool)
	}
	slot := f.slot(f.starts[0])
	if !slot.ProviderCleanupConfirmed || !f.configs[slot.ID] || !f.policies[slot.ID] {
		t.Fatal("private resources were removed before observed pod absence")
	}
	// Once absence is durably confirmed, external credentials, registration
	// decryption and image qualification are not prerequisites for local cleanup.
	s.actionsGitLabClient, s.actionsGitLabRuntime, s.Auth.EncryptionKey = nil, nil, ""
	f.podDeleting, f.policyDeleteFails = false, true
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil {
		t.Fatal("failed policy cleanup erased durable recovery")
	}
	f.slot(slot.ID)
	f.policyDeleteFails = false
	gitlabCleaned(t, s, f, pool)
}

func TestGitLabLifecycleCleanupUsesOriginalScopeCredentialAndRevision(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	changed := *pool.Config.Actions
	changed.GitLab = &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 99}
	changed.Credential = "replacement-management"
	pool.Config.Actions, pool.Config.Suspended, pool.Revision = &changed, true, 2
	s.actionsGitLabRuntime = nil
	s.Auth.EncryptionKey = ""
	gitlabCleaned(t, s, f, pool)
	for _, credential := range f.credentials {
		if credential != "original-management" {
			t.Fatal("old runner was observed or deleted with the new pool credential")
		}
	}
}

func TestGitLabLifecycleBoundedCleanupSurvivesLostDeleteAndIncompleteInventory(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	pool.Config.Suspended = true
	gitlabReconcile(t, s, pool) // Pause before discovery.
	gitlabReconcile(t, s, pool) // Persist the verified job before deletion.
	f.deleteLost = true
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil {
		t.Fatal("lost provider delete response not surfaced")
	}
	gitlabReconcile(t, s, pool)
	f.findFails = true
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil || len(f.pods) == 0 {
		t.Fatal("incomplete provider inventory was treated as absence")
	}
	f.findFails = false
	gitlabCleaned(t, s, f, pool)
}

func TestGitLabLifecycleLostIntentCleansEveryOwnedMatchWithoutBurstStarvation(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	f.registerLost = true
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil {
		t.Fatal("lost registration not surfaced")
	}
	runner := f.runners["1"]
	runner.ID = "2"
	f.runners["2"], f.targets["2"] = runner, f.targets["1"]
	gitlabCleaned(t, s, f, pool)
	if f.nextID != 1 || len(f.starts) != 0 {
		t.Fatal("lost registration was retried or launched")
	}
}

func TestGitLabLifecycleLeaseLossStopsWritesAfterRegistration(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	expired := false
	target := actionsTarget(pool)
	target.BeforeStep = func(context.Context) error {
		if expired {
			return errors.New("development fixture lease expired")
		}
		return nil
	}
	f.afterRegister = func() { expired = true }
	if s.reconcileActionsPool(context.Background(), target, pool) == nil || len(f.starts) != 0 || len(f.configs) != 0 {
		t.Fatal("expired controller persisted private artifacts or launched a manager")
	}
	slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
	if err != nil || len(slots) != 1 || slots[0].Phase != "intent" || len(slots[0].EncryptedRegistration) != 0 {
		t.Fatal("lost lease did not retain original recovery intent", err)
	}
	gitlabCleaned(t, s, f, pool)
}

func TestGitLabLifecycleProviderGateAndMixedSlotsNeverUseGitHubCredentials(t *testing.T) {
	db := actionsDatabase(t)
	s, f, pool := gitlabLifecycleHarness(t, db)
	s.actionsGitLabClient = nil
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil || len(f.credentials) != 0 || f.nextID != 0 {
		t.Fatal("unconfigured provider reached credential access or registration")
	}
	if _, err := s.runnerClient(context.Background(), actionsTarget(pool), pool.Config); err == nil || len(f.credentials) != 0 {
		t.Fatal("GitLab configuration was coerced into the GitHub client")
	}
	s, f, pool = gitlabLifecycleHarness(t, db)
	gitlabReconcile(t, s, pool)
	before := len(f.credentials)
	pool.Config.Actions = &spec.Actions{Repository: "other/repo", Credential: "github-token"}
	if s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool) == nil || len(f.credentials) != before || len(f.pods) != 1 {
		t.Fatal("mixed provider pool accessed the wrong credentials or removed its original runner")
	}
}

func TestGitLabLifecycleQualifiedPairChangeDrainsAndScalingPreservesSlots(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	first := f.starts[0]
	pool.Config.Replicas, pool.Revision = 2, 2
	gitlabReconcile(t, s, pool)
	if len(f.starts) != 2 || !f.pods[first] {
		t.Fatal("scale-only revision replaced the original registration")
	}
	pool.Config.Replicas = 1
	for pass := 0; pass < 3; pass++ {
		gitlabReconcile(t, s, pool)
	}
	if len(f.runners) != 1 || len(f.starts) != 2 {
		t.Fatal("scale-down failed to retire exactly one slot")
	}
	// A helper or policy qualification change must retire the original pair;
	// it cannot silently rewrite a manager's immutable saved registration.
	f.runtime.Images.Helper = "ghcr.io/hakopod/gitlab-runner-helper@sha256:" + strings.Repeat("2", 64)
	for pass := 0; pass < 3; pass++ {
		gitlabReconcile(t, s, pool)
	}
	if len(f.runners) != 0 || len(f.starts) != 2 {
		t.Fatal("changed qualified image pair reused its old manager registration")
	}
}

func TestGitLabErrorsRemainProviderSpecificAndNeverEchoCredentials(t *testing.T) {
	for _, err := range []error{&actions.GitLabError{Kind: "rate", RetryAt: time.Now()}, &actions.GitLabError{Kind: "scope"}, unsupportedActionsProvider(actions.ProviderGitLab), errors.New(gitlabLifecycleToken)} {
		message := safeActionsError(err)
		if strings.Contains(message, "GitHub") || strings.Contains(message, gitlabLifecycleToken) {
			t.Fatal("diagnostic mixed providers or exposed private data")
		}
	}
	err := &actionsReconcileRetry{err: &actions.GitLabError{Kind: "rate", RetryAt: time.Now()}, retry: &actions.RetryError{At: time.Now(), Progress: true}}
	var retry *actions.RetryError
	if !errors.As(err, &retry) || !retry.Progress || strings.Contains(safeActionsError(err), "GitHub") || !strings.Contains(safeActionsError(err), "GitLab") {
		t.Fatal("scheduler metadata discarded provider diagnostics or request progress")
	}
}

type gitlabCleanupBudgetFixture struct {
	*gitlabLifecycleFake
	remaining int
}

func (f *gitlabCleanupBudgetFixture) DeleteOwned(ctx context.Context, target actions.ProviderTarget, id, name string) error {
	if f.remaining < 9 {
		return &actions.GitLabError{Kind: "rate", RetryAt: time.Now().Add(time.Second)}
	}
	f.remaining -= 9
	return f.gitlabLifecycleFake.DeleteOwned(ctx, target, id, name)
}

func TestGitLabLifecycleBusyCleanupRotatesAcrossSharedCredentialBudget(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	pool.Config.Replicas = 2
	gitlabReconcile(t, s, pool)
	gitlabReconcile(t, s, pool)
	for id, runner := range f.runners {
		runner.Busy = true
		f.runners[id] = runner
	}
	pool.Config.Suspended = true
	gitlabReconcile(t, s, pool) // Persist acquisition shutdown for both slots.
	gitlabReconcile(t, s, pool) // Finish history discovery for both slots.
	budget := &gitlabCleanupBudgetFixture{gitlabLifecycleFake: f}
	s.actionsGitLabClient = func(context.Context, cluster.Target, spec.Service, string) (gitlabRunnerProvider, error) {
		return budget, nil
	}
	pool.Config.Suspended = true
	for pass := 0; pass < 3; pass++ {
		budget.remaining = 10
		err := s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool)
		var retry *actions.RetryError
		if !errors.As(err, &retry) {
			t.Fatal("fixture did not apply the native shared request budget", err)
		}
	}
	if !f.paused["1"] || !f.paused["2"] || len(f.pods) != 2 {
		t.Fatal("oldest busy runner starved another slot or interrupted its active job")
	}
}

func TestGitLabLifecycleReadinessRequiresOnlineProviderAndRunningPod(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	id := f.starts[0]
	runner := f.runners["1"]
	runner.Status, runner.Busy = "offline", true
	f.runners["1"] = runner
	gitlabReconcile(t, s, pool)
	if f.slot(id).Phase != "starting" {
		t.Fatal("unknown execution status on offline provider was reported ready")
	}
	runner.Status, runner.Busy = "online", false
	f.runners["1"], f.podPhase = runner, "Pending"
	gitlabReconcile(t, s, pool)
	if f.slot(id).Phase != "starting" {
		t.Fatal("pending Pod was reported ready from provider status alone")
	}
	f.podPhase = "Running"
	gitlabReconcile(t, s, pool)
	if f.slot(id).Phase != "online" {
		t.Fatal("matching live observations did not mark the runner online")
	}
}
