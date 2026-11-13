package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/migrate"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// A claimed migration runs its steps in order. Each step records what it
// created (database, copy and build IDs) in the steps JSON before moving on,
// and checks that record first, so a migration resumed after a lost lease
// continues where it stopped instead of creating things twice.

// Tests shorten these.
var (
	migrationPoll      = 5 * time.Second
	migrationHeartbeat = 20 * time.Second
)

var migrationStepNames = []string{"secrets", "databases", "data", "deploy", "builds", "domains", "finish"}

// errMigrationStopped means the lease was lost or the user cancelled.
var errMigrationStopped = errors.New("migration stopped")

type migrationItem struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Ref is what this item created: a database, copy or build ID.
	Ref string `json:"ref,omitempty"`
}

type migrationStep struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Message    string          `json:"message,omitempty"`
	Items      []migrationItem `json:"items,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

func (st *migrationStep) item(name string) *migrationItem {
	for i := range st.Items {
		if st.Items[i].Name == name {
			return &st.Items[i]
		}
	}
	st.Items = append(st.Items, migrationItem{Name: name, Status: "pending"})
	return &st.Items[len(st.Items)-1]
}

func done(status string) bool { return status == "succeeded" || status == "skipped" }

// runningMigrationCopies lists the database copies a migration has started
// and not seen finish.
func runningMigrationCopies(raw json.RawMessage) []string {
	var steps []migrationStep
	_ = json.Unmarshal(raw, &steps)
	var out []string
	for _, st := range steps {
		if st.Name != "data" {
			continue
		}
		for _, it := range st.Items {
			if it.Ref != "" && !done(it.Status) {
				out = append(out, it.Ref)
			}
		}
	}
	return out
}

// RunPlatformMigrations runs queued migrations one at a time and erases
// tokens past their expiry.
func (s *Server) RunPlatformMigrations(ctx context.Context) {
	if s.Store == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var expired time.Time
	for ctx.Err() == nil {
		if time.Since(expired) > time.Minute {
			bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = s.Store.ExpirePlatformMigrations(bounded)
			cancel()
			expired = time.Now()
		}
		for ctx.Err() == nil && s.runNextPlatformMigration(ctx) {
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// runNextPlatformMigration claims and runs one migration, reporting whether
// there was one.
func (s *Server) runNextPlatformMigration(ctx context.Context) bool {
	lease := store.NewID()
	claim, cancel := context.WithTimeout(ctx, 5*time.Second)
	m, err := s.Store.ClaimPlatformMigration(claim, lease)
	cancel()
	if err != nil {
		return false
	}
	s.runPlatformMigration(ctx, m, lease)
	return true
}

type migrationRun struct {
	s        *Server
	m        store.PlatformMigration
	lease    string
	p        store.Principal
	rt       migrationRuntime
	proposal migrate.Proposal
	inv      migrate.Inventory
	secrets  map[string]string
	steps    []migrationStep
	appID    string
	dbIDs    map[string]string // DatabasePlan.Name → managed database ID
}

func (s *Server) runPlatformMigration(parent context.Context, m store.PlatformMigration, lease string) {
	ctx, stop := context.WithCancel(parent)
	defer stop()
	go func() {
		ticker := time.NewTicker(migrationHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if ok, err := s.Store.HeartbeatPlatformMigration(ctx, m.ID, lease); err == nil && !ok {
				stop()
				return
			}
		}
	}()
	r := &migrationRun{s: s, m: m, lease: lease, appID: m.ApplicationID, dbIDs: map[string]string{}}
	err := r.execute(ctx)
	if parent.Err() != nil {
		return // shutting down; another worker resumes once the lease lapses
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()
	if err == nil {
		_ = s.Store.FinishPlatformMigration(bg, m.ID, lease, "succeeded", "")
		return
	}
	if ctx.Err() != nil || errors.Is(err, errMigrationStopped) {
		current, e := s.Store.PlatformMigration(bg, m.ID)
		if e == nil && current.Lease == lease && current.Status == "running" && current.CancelRequested {
			r.stopCopies(bg)
			_ = s.Store.FinishPlatformMigration(bg, m.ID, lease, "cancelled", "Cancelled. Anything created before the cancellation stays.")
		}
		return // the lease was lost; its new holder continues
	}
	r.stopCopies(bg)
	_ = s.Store.FinishPlatformMigration(bg, m.ID, lease, "failed", err.Error())
}

func (r *migrationRun) stopCopies(ctx context.Context) {
	if r.rt == nil {
		return
	}
	for _, id := range runningMigrationCopies(store.JSON(r.steps)) {
		_ = r.rt.CancelDatabaseCopy(ctx, cluster.CopyID(id))
	}
}

func (r *migrationRun) save(ctx context.Context) error {
	err := r.s.Store.RecordPlatformMigrationSteps(ctx, r.m.ID, r.lease, store.JSON(r.steps), r.appID)
	if errors.Is(err, store.ErrConflict) {
		return errMigrationStopped
	}
	return err
}

// prepare loads what every step needs: the grant, the stored proposal, and
// the source re-read with the token, converted again for the secret values.
func (r *migrationRun) prepare(ctx context.Context) error {
	s := r.s
	if json.Unmarshal(r.m.Steps, &r.steps) != nil || len(r.steps) == 0 {
		r.steps = nil
		for _, name := range migrationStepNames {
			r.steps = append(r.steps, migrationStep{Name: name, Status: "pending"})
		}
	}
	for _, st := range r.steps {
		if st.Name == "databases" {
			for _, it := range st.Items {
				if it.Ref != "" {
					r.dbIDs[it.Name] = it.Ref
				}
			}
		}
	}
	key := s.authEncryptionKey()
	if len(key) != 32 {
		return errors.New("The authentication encryption key is not configured, so the source token cannot be opened.")
	}
	if r.rt = s.migrationRuntime(); r.rt == nil {
		return errors.New("Kubernetes is unavailable.")
	}
	var err error
	if r.p, err = s.Store.KeyPrincipal(ctx, r.m.ID); err != nil || !r.p.Allows("deployments:write", r.m.Project, r.m.Environment, r.m.ApplicationName) {
		return errors.New("The person who planned this migration can no longer deploy here, or their account was disabled.")
	}
	if err = json.Unmarshal(r.m.Proposal, &r.proposal); err != nil {
		return errors.New("The stored proposal is unreadable. Plan the migration again.")
	}
	tokenSealed, variablesSealed, err := s.Store.PlatformMigrationSecrets(ctx, r.m.ID)
	if err != nil {
		return err
	}
	if len(tokenSealed) == 0 {
		return errors.New("The source token was erased. Plan the migration again.")
	}
	token, err := migrate.Open(key, r.m.ID, "token", tokenSealed)
	if err != nil {
		return err
	}
	values, err := migrationValues(key, r.m.ID, variablesSealed)
	if err != nil {
		return err
	}
	read, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r.inv, err = s.migrationSource(r.m.Provider, string(token), r.m.SourceTeamID).Read(read, r.m.SourceProjectID, r.m.SourceEnvironmentID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s could not be read with the token. Check that it is still valid, then plan the migration again.", providerTitle(r.m.Provider))
	}
	again, err := migrate.Convert(r.inv, migrate.ConvertOptions{Project: r.m.Project, Environment: r.m.Environment, Application: r.m.ApplicationName, GitConnectionID: migrationGitConnection(r.proposal), Values: values, SecretKey: key})
	if err != nil {
		return err
	}
	if len(again.Missing) > 0 {
		return fmt.Errorf("The %s project has new variables without values since the plan. Plan the migration again.", providerTitle(r.m.Provider))
	}
	r.secrets = again.Secrets
	return nil
}

func (r *migrationRun) execute(ctx context.Context) error {
	if err := r.prepare(ctx); err != nil {
		return err
	}
	run := map[string]func(context.Context, *migrationStep) error{
		"secrets": r.writeSecrets, "databases": r.createDatabases, "data": r.copyData, "deploy": r.deploy,
		"builds": r.createBuilds, "domains": r.addDomains,
		"finish": func(context.Context, *migrationStep) error { return nil },
	}
	for i := range r.steps {
		st := &r.steps[i]
		if done(st.Status) {
			continue
		}
		fn := run[st.Name]
		if fn == nil {
			continue
		}
		// Notice a cancellation at every step, not only every heartbeat.
		if ok, err := r.s.Store.HeartbeatPlatformMigration(ctx, r.m.ID, r.lease); err != nil {
			return err
		} else if !ok {
			return errMigrationStopped
		}
		now := time.Now().UTC()
		if st.StartedAt == nil {
			st.StartedAt = &now
		}
		st.Status, st.Message = "running", ""
		if err := r.save(ctx); err != nil {
			return err
		}
		err := fn(ctx, st)
		finished := time.Now().UTC()
		st.FinishedAt = &finished
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, errMigrationStopped) {
				return errMigrationStopped
			}
			st.Status, st.Message = "failed", err.Error()
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = r.save(bg)
			cancel()
			return err
		}
		if st.Status == "running" {
			st.Status = "succeeded"
		}
		if st.Name == "finish" {
			st.Message = "The source token was erased."
		}
		if err = r.save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *migrationRun) writeSecrets(ctx context.Context, st *migrationStep) error {
	m := r.m
	for _, name := range r.proposal.SecretNames {
		value, ok := r.secrets[name]
		if !ok {
			return fmt.Errorf("A secret variable was removed from the %s project since the plan. Plan the migration again.", providerTitle(m.Provider))
		}
		if err := r.rt.PutWorkloadSecret(ctx, m.Project, m.Environment, m.ApplicationName, name, value); err != nil {
			return fmt.Errorf("Saving an application secret failed: %w", err)
		}
		if _, err := r.s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.write',$3)", r.p.ID, r.p.KeyID, m.Project+"/"+m.Environment+"/"+m.ApplicationName+"/"+name); err != nil {
			return err
		}
	}
	st.Message = fmt.Sprintf("%d secrets saved.", len(r.proposal.SecretNames))
	if len(r.proposal.SecretNames) == 0 {
		st.Status, st.Message = "skipped", "The project has no secret variables."
	}
	return nil
}

func migrationSleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(migrationPoll):
		return nil
	}
}

func (r *migrationRun) createDatabases(ctx context.Context, st *migrationStep) error {
	if len(r.proposal.Databases) == 0 {
		st.Status, st.Message = "skipped", "The project has no databases."
		return nil
	}
	s, m := r.s, r.m
	for _, plan := range r.proposal.Databases {
		it := st.item(plan.Name)
		if it.Status == "succeeded" {
			continue
		}
		// Like POST /databases: the ID follows from the identity and the
		// idempotency key, so a retried creation finds the first one.
		idem := "platform-migration-" + m.ID + "-" + plan.Name
		sum := sha256.Sum256([]byte("managed-database:" + r.p.ID + ":" + idem))
		id := hex.EncodeToString(sum[:16])
		d, err := s.Store.DatabaseInternal(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			spec := plan.Spec.WithSecureDefaults()
			for i := 2; ; i++ {
				var taken bool
				if err = s.Store.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_databases WHERE project=$1 AND environment=$2 AND name=$3 AND deleted_at IS NULL)", m.Project, m.Environment, spec.Name).Scan(&taken); err != nil {
					return err
				}
				if !taken {
					break
				}
				suffix := "-" + strconv.Itoa(i)
				spec.Name = strings.TrimRight(plan.Name[:min(len(plan.Name), 40-len(suffix))], "-") + suffix
			}
			d = database.Resource{ID: id, Project: m.Project, Environment: m.Environment, Spec: spec}
			if err = r.rt.DatabaseControllerAvailable(ctx, spec); err != nil {
				return fmt.Errorf("Database %s cannot be created: %w", spec.Name, err)
			}
			if err = r.rt.ValidateDatabasePlacement(ctx, d); err != nil {
				return fmt.Errorf("Database %s cannot be placed: %w", spec.Name, err)
			}
			random := make([]byte, 32)
			if _, err = rand.Read(random); err != nil {
				return err
			}
			if d.EncryptedCredentials, err = database.SealCredentials(s.authEncryptionKey(), id, []byte(hex.EncodeToString(random))); err != nil {
				return err
			}
			if _, err = s.Store.AcceptDatabase(ctx, r.p, d, 0, idem, "create"); err != nil {
				return fmt.Errorf("Database %s could not be created: %w", spec.Name, err)
			}
		} else if err != nil {
			return err
		}
		it.Ref, it.Status, it.Message = id, "running", "Creating database "+d.Spec.Name+"."
		r.dbIDs[plan.Name] = id
		if err = r.save(ctx); err != nil {
			return err
		}
		if _, _, err = r.databaseReady(ctx, id); err != nil {
			return err
		}
		it.Status, it.Message = "succeeded", "Created database "+d.Spec.Name+"."
		if err = r.save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// databaseReady waits for a fresh ready observation of the database.
func (r *migrationRun) databaseReady(ctx context.Context, id string) (database.Resource, database.Observation, error) {
	deadline := time.Now().Add(time.Hour)
	for {
		var status, message string
		err := r.s.Store.Pool.QueryRow(ctx, "SELECT status,message FROM managed_database_operations WHERE database_id=$1 AND kind='create'", id).Scan(&status, &message)
		if err != nil {
			return database.Resource{}, database.Observation{}, err
		}
		if status == "failed" || status == "cancelled" {
			return database.Resource{}, database.Observation{}, fmt.Errorf("Creating a database failed: %s", message)
		}
		d, err := r.s.Store.DatabaseInternal(ctx, id)
		if err != nil {
			return d, database.Observation{}, err
		}
		// Ready once the database worker finished creating it, and ready now.
		if status == "succeeded" && d.Status == "ready" {
			if o, err := r.rt.ObserveDatabase(ctx, d); err == nil && o.Status == "ready" && o.Revision == d.Revision {
				return d, o, nil
			}
		}
		if time.Now().After(deadline) {
			return d, database.Observation{}, fmt.Errorf("Database %s was not ready within an hour.", d.Spec.Name)
		}
		if err = migrationSleep(ctx); err != nil {
			return d, database.Observation{}, err
		}
	}
}

func (r *migrationRun) copyData(ctx context.Context, st *migrationStep) error {
	copies := 0
	for _, plan := range r.proposal.Databases {
		it := st.item(plan.Name)
		if done(it.Status) {
			continue
		}
		if !plan.CopyData {
			it.Status, it.Message = "skipped", plan.Reason
			continue
		}
		copies++
		id := r.dbIDs[plan.Name]
		d, o, err := r.databaseReady(ctx, id)
		if err != nil {
			return err
		}
		if it.Ref == "" {
			if err = r.rt.DatabaseEmpty(ctx, d, o); err != nil {
				return fmt.Errorf("Database %s already holds data, so nothing was copied into it: %v", d.Spec.Name, err)
			}
			var source migrate.Database
			for _, db := range r.inv.Databases {
				if db.Name == plan.Source {
					source = db
				}
			}
			if source.URL == "" {
				return fmt.Errorf("The source database %s is no longer reachable from the internet, so its data cannot be copied.", plan.Source)
			}
			// The application does not exist yet; the Job records the migration.
			copyID, err := r.rt.StartDatabaseCopy(ctx, cluster.CopyRequest{ApplicationID: r.m.ID, Database: d, Observation: o, SourceURL: source.URL, SourceVersion: source.Version})
			if err != nil {
				return fmt.Errorf("Copying data into %s could not start: %w", d.Spec.Name, err)
			}
			it.Ref, it.Status, it.Message = string(copyID), "running", "Copying data from "+plan.Source+"."
			// Record a started copy even when stopping, so a resumed run follows it.
			if err = r.save(context.WithoutCancel(ctx)); err != nil {
				return err
			}
		}
		err = r.waitCopy(ctx, it, d.Spec.Name)
		_ = r.rt.CleanupDatabaseCopies(context.WithoutCancel(ctx), id)
		if err != nil {
			return err
		}
		if err = r.save(ctx); err != nil {
			return err
		}
	}
	if copies == 0 {
		st.Status, st.Message = "skipped", "No database data is copied."
	}
	return nil
}

func (r *migrationRun) waitCopy(ctx context.Context, it *migrationItem, name string) error {
	for {
		status, err := r.rt.DatabaseCopyStatus(ctx, cluster.CopyID(it.Ref))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("The data copy into %s can no longer be followed: %w", name, err)
		}
		switch status.Phase {
		case "succeeded":
			it.Status, it.Message = "succeeded", "Data copied."
			if status.Keys > 0 {
				it.Message = fmt.Sprintf("%d keys copied.", status.Keys)
			}
			return nil
		case "failed":
			it.Status, it.Message = "failed", status.Error
			return fmt.Errorf("Copying data into %s failed: %s", name, status.Error)
		}
		if err = migrationSleep(ctx); err != nil {
			return err
		}
	}
}

// deploy accepts the first revision as POST /deployments does. Git services
// are in it suspended on the placeholder image until their first build.
func (r *migrationRun) deploy(ctx context.Context, st *migrationStep) error {
	app, err := r.proposal.Bind(r.dbIDs)
	if err != nil {
		return fmt.Errorf("The application could not be prepared: %w", err)
	}
	d, err := r.s.Store.Accept(ctx, r.p, r.m.Project, r.m.Environment, app, 0, "platform-migration-"+r.m.ID)
	if err != nil {
		return fmt.Errorf("The application could not be deployed: %w", err)
	}
	r.appID = d.ApplicationID
	st.Message = "Deployment " + d.ID + " accepted."
	return nil
}

// createBuilds saves a build configuration per Git service, as POST /builds
// does, and opens a pull request that adds its workflow.
func (r *migrationRun) createBuilds(ctx context.Context, st *migrationStep) error {
	if len(r.proposal.Builds) == 0 {
		st.Status, st.Message = "skipped", "No service is built from Git."
		return nil
	}
	ctx = context.WithValue(ctx, principalKey{}, r.p) // Git connections are checked against the planner
	for _, b := range r.proposal.Builds {
		it := st.item(b.Service)
		if done(it.Status) {
			continue
		}
		c, err := r.buildConfig(ctx, b, it.Ref)
		if err != nil {
			return fmt.Errorf("The build for %s could not be saved: %w", b.Service, err)
		}
		it.Ref = c.ID
		if err = r.save(ctx); err != nil {
			return err
		}
		if c.Provider != "github" {
			it.Status, it.Message = "skipped", "Install the build from Builds, then build to start this service."
			continue
		}
		link, err := r.s.openBuildPullRequest(ctx, c)
		if err != nil {
			it.Status, it.Message = "skipped", "A pull request could not be opened ("+err.Error()+"). Install the build from Builds, then build to start this service."
			continue
		}
		it.Status = "succeeded"
		it.Message = "Merge the pull request to build and start this service: " + link
	}
	return nil
}

func (r *migrationRun) buildConfig(ctx context.Context, b migrate.BuildPlan, id string) (buildConfig, error) {
	s, m := r.s, r.m
	if id == "" {
		// Saved just before a crash, before its ID was recorded.
		_ = s.Store.Pool.QueryRow(ctx, "SELECT id FROM build_configs WHERE project=$1 AND environment=$2 AND name=$3 AND service=$4", m.Project, m.Environment, m.ApplicationName, b.Service).Scan(&id)
	}
	if id != "" {
		return s.readBuild(ctx, id)
	}
	// env, secrets, command and args stay unset so the spec's values are kept.
	c, err := normalizeBuild(buildInput{Provider: b.Provider, ConnectionID: b.ConnectionID, ApplicationID: r.appID, Project: m.Project, Environment: m.Environment, Name: m.ApplicationName, Service: b.Service,
		Repository: b.Repository, Branch: b.Branch, Mode: b.Mode, Preset: b.Preset, ContextPath: b.ContextPath, Framework: b.Framework, BuildArgs: b.BuildArgs, Port: b.Port, Public: b.Public, AutoBuild: true, AutoDeploy: true})
	if err != nil {
		return c, err
	}
	nodes, err := r.rt.Nodes(ctx)
	if err != nil {
		return c, fmt.Errorf("%w: node inspection is unavailable", store.ErrInput)
	}
	if c.Architecture, err = nodeArchitecture(nodes); err != nil {
		return c, err
	}
	if err = s.validateGitConnection(ctx, c.Provider, c.ConnectionID); err != nil {
		return c, err
	}
	if err = s.validateBuildApplication(ctx, c); err != nil {
		return c, err
	}
	return c, s.insertBuild(ctx, r.p, &c)
}

// openBuildPullRequest commits the reviewed workflow to a new branch and opens
// a pull request into the default branch. The configuration counts as
// installed: the workflow it runs is exactly the reviewed one, and it can only
// run once someone with access to the repository merges it.
func (s *Server) openBuildPullRequest(ctx context.Context, c buildConfig) (string, error) {
	repoPath := "/repos/" + c.Repository
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.githubGET(ctx, repoPath, &repo, c.ConnectionID); err != nil {
		return "", err
	}
	var head struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := s.githubGET(ctx, repoPath+"/git/ref/heads/"+repo.DefaultBranch, &head, c.ConnectionID); err != nil {
		return "", err
	}
	branch := "hakopod/build-" + c.ID
	call := func(method, endpoint string, body any, ok ...int) (*json.Decoder, int, error) {
		response, err := s.githubBuildRequest(ctx, method, endpoint, body, c.ConnectionID)
		if err != nil {
			return nil, 0, err
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		response.Body.Close()
		for _, code := range ok {
			if response.StatusCode == code {
				return json.NewDecoder(strings.NewReader(string(data))), code, nil
			}
		}
		return nil, response.StatusCode, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	// 422: the branch exists from an earlier attempt.
	if _, _, err := call("POST", repoPath+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": head.Object.SHA}, 201, 422); err != nil {
		return "", err
	}
	current, status, err := s.workflowContents(ctx, c, branch)
	if err != nil {
		return "", err
	}
	body := map[string]any{"message": "Add the Hakopod build for " + c.Name + "/" + c.Service, "branch": branch, "content": base64.StdEncoding.EncodeToString([]byte(buildWorkflow(c)))}
	if status == 200 {
		body["sha"] = current.SHA
	}
	out, _, err := call("PUT", repoPath+"/contents/"+c.workflowPath(), body, 200, 201)
	if err != nil {
		return "", err
	}
	var written struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if out.Decode(&written) != nil || !commitPattern.MatchString(written.Commit.SHA) {
		return "", errors.New("GitHub's commit response was invalid")
	}
	// The workflow ignores pushes that only change its own file, so a merge that
	// adds only the workflow would not build. A short note beside it makes the
	// merge itself start the first build; the workflow stays exactly as reviewed.
	notePath := ".hakopod/builds/" + c.ID + ".md"
	if _, status, err := call("GET", repoPath+"/contents/"+notePath+"?ref="+url.QueryEscape(branch), nil, 200, 404); err != nil {
		return "", err
	} else if status == 404 {
		note := "# Hakopod build for " + c.Service + "\n\nAdded when this service was migrated to Hakopod, so that merging the pull request starts its first build. You can delete this file.\n"
		if _, _, err = call("PUT", repoPath+"/contents/"+notePath, map[string]any{"message": "Note the Hakopod build for " + c.Service, "branch": branch, "content": base64.StdEncoding.EncodeToString([]byte(note))}, 201); err != nil {
			return "", err
		}
	}
	var pull struct {
		URL string `json:"html_url"`
	}
	text := "Hakopod migrated this service from another platform. This adds the workflow that builds `" + c.Service + "` and publishes its image for Hakopod, and a short note so that merging starts the first build.\n\nMerge it to build `" + c.Service + "` and start it on Hakopod."
	out, code, err := call("POST", repoPath+"/pulls", map[string]string{"title": "Build " + c.Service + " with Hakopod", "head": branch, "base": repo.DefaultBranch, "body": text}, 201, 422)
	if err != nil {
		return "", err
	}
	if code == 201 {
		err = out.Decode(&pull)
	} else { // already open from an earlier attempt
		owner, _, _ := strings.Cut(c.Repository, "/")
		var open []struct {
			URL string `json:"html_url"`
		}
		if out, _, err = call("GET", repoPath+"/pulls?state=open&head="+owner+":"+branch, nil, 200); err == nil {
			err = out.Decode(&open)
		}
		if err == nil && len(open) > 0 {
			pull.URL = open[0].URL
		}
	}
	if err != nil || !strings.HasPrefix(pull.URL, "https://") {
		return "", errors.New("GitHub did not return the pull request")
	}
	if _, err = s.Store.Pool.Exec(ctx, "UPDATE build_configs SET installed_revision=$2,installed_commit=$3,updated_at=now() WHERE id=$1 AND revision=$2", c.ID, c.Revision, written.Commit.SHA); err != nil {
		return "", err
	}
	return pull.URL, nil
}

// addDomains adds each custom domain as pending, as POST
// /applications/{id}/domains does. A domain another application holds is
// reported on its row and does not fail the migration.
func (r *migrationRun) addDomains(ctx context.Context, st *migrationStep) error {
	if len(r.proposal.Domains) == 0 {
		st.Status, st.Message = "skipped", "The project has no custom domains."
		return nil
	}
	a, err := r.s.Store.Application(ctx, r.appID)
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(r.proposal.Domains))
	for host := range r.proposal.Domains {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		it := st.item(host)
		if done(it.Status) {
			continue
		}
		d, err := r.s.Store.BeginDomainVerification(ctx, r.p, a, host, r.proposal.Domains[host])
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInput) {
			it.Status, it.Message = "failed", "Not added: "+err.Error()
			st.Message = "Some domains were not added."
			continue
		}
		if err != nil {
			return err
		}
		view := r.s.domainView(a, d, map[string]bool{})
		it.Status = "succeeded"
		it.Message = fmt.Sprintf("Pending. Add a TXT record %s with the value %s, verify it, then point %s at the service with a CNAME", view.VerificationName, view.VerificationValue, host)
		if view.Target != "" {
			it.Message += " to " + view.Target
		}
		it.Message += "."
	}
	return nil
}
