package migrate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/spec"
)

// PlaceholderImage stands in for a Git service until its first build. It is
// the busybox digest Hakopod already pins for host terminals.
//
// How a built service reaches the application: prepareBuildSpec in
// internal/api/build_deploy.go creates a one-service application when none
// exists, and otherwise requires the build's service to already exist and every
// other service to have a digest-pinned image in the current resolved release.
// It then replaces only image, architecture and registry credential; port,
// public, env, secrets, bindings and Suspended are kept from the spec. So the
// migrated application is deployed first with each Git service present,
// suspended (zero replicas) on this pinned placeholder. Each first build then
// swaps in the built image, and the worker resumes the service.
const PlaceholderImage = "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"

// CronImage runs Vercel cron requests with busybox wget; same pinned image.
const CronImage = PlaceholderImage

// VolumeSizeGiB is the size of a recreated (empty) Railway volume.
const VolumeSizeGiB = 5

type ConvertOptions struct {
	Project     string
	Environment string
	Application string
	// GitConnectionID is the Hakopod Git connection used for every build.
	GitConnectionID string
	// Values supplies unreadable variables: Hakopod service name (as in
	// Proposal.Missing) → variable name → value.
	Values map[string]map[string]string
	// SecretKey derives secret names (see SecretName); the API passes the
	// installation auth encryption key.
	SecretKey []byte
}

type BuildPlan struct {
	Service      string `json:"service"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Repository   string `json:"repository"` // owner/name
	Branch       string `json:"branch"`
	ContextPath  string `json:"context_path"`
	// Mode is framework or buildpacks, as buildInput.Mode.
	Mode      string          `json:"mode"`
	Preset    string          `json:"preset"`
	Framework *framework.Plan `json:"framework,omitempty"`
	// BuildArgs are public build-time variables (NEXT_PUBLIC_ and similar).
	BuildArgs map[string]string `json:"build_args,omitempty"`
	Port      int               `json:"port"`
	Public    bool              `json:"public"`
}

type DatabasePlan struct {
	Name string `json:"name"`
	// Source is the inventory database name, to find its URL for the copy.
	Source   string        `json:"source"`
	Engine   Engine        `json:"engine"`
	Version  string        `json:"version"`
	CopyData bool          `json:"copy_data"`
	Reason   string        `json:"reason"`
	Spec     database.Spec `json:"spec"`
}

// BindingPlan is a managed database binding waiting for its database ID.
type BindingPlan struct {
	Service  string `json:"service"`
	Variable string `json:"variable"`
	// Database is a DatabasePlan.Name.
	Database string       `json:"database"`
	Binding  spec.Binding `json:"binding"`
}

type MissingVariable struct {
	Service string `json:"service"`
	Name    string `json:"name"`
}

type Proposal struct {
	// Spec has no bindings yet; call Bind with the created database IDs.
	Spec      spec.Application `json:"spec"`
	Builds    []BuildPlan      `json:"builds"`
	Databases []DatabasePlan   `json:"databases"`
	Bindings  []BindingPlan    `json:"bindings"`
	// Secrets maps secret name → value. Never serialized.
	Secrets     map[string]string `json:"-"`
	SecretNames []string          `json:"secret_names"`
	// Domains maps custom hostname → service, added as pending after deploy.
	Domains  map[string]string `json:"domains"`
	Missing  []MissingVariable `json:"missing"`
	Warnings []string          `json:"warnings"`
}

// SecretName is the deterministic application secret name for one migrated
// variable, so a retried migration overwrites rather than duplicates.
func SecretName(key []byte, project, environment, application, service, variable string) string {
	mac := hmac.New(sha256.New, key)
	b, _ := json.Marshal([]string{"hakopod-migrate-v1", project, environment, application, service, variable})
	mac.Write(b)
	return "migrate-" + hex.EncodeToString(mac.Sum(nil))[:32]
}

// Bind returns Spec with every planned binding pointed at its created
// database. ids maps DatabasePlan.Name → managed database ID.
func (p Proposal) Bind(ids map[string]string) (spec.Application, error) {
	app, err := spec.Normalize(p.Spec) // deep copy
	if err != nil {
		return app, err
	}
	for _, b := range p.Bindings {
		id, ok := ids[b.Database]
		if !ok {
			return app, fmt.Errorf("no database ID for %s", b.Database)
		}
		svc := app.Services[b.Service]
		if svc.Bindings == nil {
			svc.Bindings = map[string]spec.Binding{}
		}
		binding := b.Binding
		binding.ManagedDatabase = id
		svc.Bindings[b.Variable] = binding
		app.Services[b.Service] = svc
	}
	return spec.Normalize(app)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var cronPath = regexp.MustCompile(`^/[A-Za-z0-9._~/%=&?+-]*$`)
var publicBuildPrefixes = []string{"NEXT_PUBLIC_", "VITE_", "PUBLIC_", "REACT_APP_", "NUXT_PUBLIC_", "GATSBY_"}

type converter struct {
	opts     ConvertOptions
	inv      Inventory
	p        Proposal
	names    map[string]bool
	warned   map[string]bool
	dbs      map[string]DatabasePlan // by source name
	services []string                // Hakopod name per inventory service
}

func (c *converter) warn(format string, args ...any) {
	w := fmt.Sprintf(format, args...)
	if !c.warned[w] {
		c.warned[w] = true
		c.p.Warnings = append(c.p.Warnings, w)
	}
}

// Convert turns an inventory into a reviewable proposal. It does no I/O.
func Convert(inv Inventory, opts ConvertOptions) (Proposal, error) {
	if len(opts.SecretKey) == 0 {
		return Proposal{}, errors.New("a secret naming key is required")
	}
	c := &converter{opts: opts, inv: inv, names: map[string]bool{}, warned: map[string]bool{}, dbs: map[string]DatabasePlan{}}
	c.p = Proposal{Spec: spec.Application{SchemaVersion: 1, Name: opts.Application, Services: map[string]spec.Service{}}, Secrets: map[string]string{}, Domains: map[string]string{}, Builds: []BuildPlan{}, Databases: []DatabasePlan{}, Bindings: []BindingPlan{}, Missing: []MissingVariable{}, Warnings: []string{}}
	for _, w := range inv.Warnings {
		c.warn("%s", w)
	}
	for _, db := range inv.Databases {
		if err := c.database(db); err != nil {
			return Proposal{}, err
		}
	}
	for _, s := range inv.Services {
		c.services = append(c.services, c.unique(slug(s.Name)))
	}
	for i, s := range inv.Services {
		c.service(c.services[i], s)
	}
	if len(c.p.Spec.Services) > 20 {
		return Proposal{}, fmt.Errorf("the project needs %d Hakopod services; an application holds at most 20", len(c.p.Spec.Services))
	}
	app, err := spec.Normalize(c.p.Spec)
	if err != nil {
		return Proposal{}, fmt.Errorf("converted application is invalid: %w", err)
	}
	c.p.Spec = app
	// Check the bindings too, with stand-in IDs.
	ids := map[string]string{}
	for _, db := range c.p.Databases {
		ids[db.Name] = strings.Repeat("0", 32)
	}
	if _, err := c.p.Bind(ids); err != nil {
		return Proposal{}, fmt.Errorf("converted database bindings are invalid: %w", err)
	}
	c.p.SecretNames = make([]string, 0, len(c.p.Secrets))
	for name := range c.p.Secrets {
		c.p.SecretNames = append(c.p.SecretNames, name)
	}
	sort.Strings(c.p.SecretNames)
	return c.p, nil
}

// slug makes a valid Hakopod name (spec namePattern): lowercase letters,
// digits and hyphens, starting with a letter, at most 40 characters.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "service"
	}
	if s[0] < 'a' || s[0] > 'z' {
		s = "s-" + s
	}
	return trimName(s, 40)
}

func trimName(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.TrimRight(s, "-")
}

func (c *converter) unique(base string) string {
	name := base
	for i := 2; c.names[name]; i++ {
		suffix := "-" + strconv.Itoa(i)
		name = trimName(base, 40-len(suffix)) + suffix
	}
	c.names[name] = true
	return name
}

// checks reports why one service would fail Hakopod validation on its own.
func checks(name string, s spec.Service) error {
	_, err := spec.Normalize(spec.Application{Name: "check", Services: map[string]spec.Service{name: s}})
	return err
}

func (c *converter) database(db Database) error {
	supported := map[Engine][]string{PostgreSQL: {"17", "18"}, Redis: {"8"}, MySQL: {"8.4"}, MongoDB: {"8.0"}}[db.Engine]
	if supported == nil {
		c.warn("Database %s uses %q, which Hakopod cannot manage; it is not migrated.", db.Name, db.Engine)
		return nil
	}
	version := pickVersion(db.Version, supported)
	switch {
	case db.Version == "":
		c.warn("Database %s: the source version is unknown; Hakopod will create version %s.", db.Name, version)
	case major(db.Version) > major(version):
		c.warn("Database %s runs version %s, newer than Hakopod supports; Hakopod will create version %s and restoring into it may fail.", db.Name, db.Version, version)
	case major(db.Version) != major(version):
		c.warn("Database %s runs version %s; Hakopod will create version %s.", db.Name, db.Version, version)
	}
	storage := int64(5)
	if need := (2*db.SizeBytes + (1 << 30) - 1) >> 30; need > storage {
		storage = need
	}
	if storage > 1024 {
		storage = 1024
		c.warn("Database %s is larger than Hakopod's 1024 GiB storage limit allows with headroom; review its size.", db.Name)
	}
	cpu, memory := "250m", "512Mi"
	if db.Engine == MySQL || db.Engine == MongoDB {
		cpu, memory = "500m", "1Gi"
	}
	name := trimName(slug(c.opts.Application+"-"+db.Name), 40)
	for i := 2; c.dbName(name); i++ {
		suffix := "-" + strconv.Itoa(i)
		name = trimName(slug(c.opts.Application+"-"+db.Name), 40-len(suffix)) + suffix
	}
	plan := DatabasePlan{Name: name, Source: db.Name, Engine: db.Engine, Version: version,
		Spec: database.Spec{SchemaVersion: 1, Name: name, Engine: string(db.Engine), Version: version, Mode: "standalone", Shards: 1, CPU: cpu, Memory: memory, StorageGiB: storage, TLS: &database.TLSConfig{Mode: "required"}}}
	switch {
	case (db.Engine == PostgreSQL || db.Engine == Redis) && db.Reachable:
		plan.CopyData = true
		plan.Reason = "Data is copied from the source."
	case db.Engine == PostgreSQL || db.Engine == Redis:
		plan.Reason = "The source has no public endpoint, so it is created empty."
		c.warn("Database %s has no public endpoint on the source; Hakopod creates it empty. Enable a TCP proxy on the source to copy its data.", db.Name)
	default:
		plan.Reason = "Data for this engine is not copied yet, so it is created empty."
		c.warn("Database %s (%s) is created empty; copy its data yourself.", db.Name, db.Engine)
	}
	if err := plan.Spec.Validate(); err != nil {
		return fmt.Errorf("database %s: %w", db.Name, err)
	}
	c.dbs[db.Name] = plan
	c.p.Databases = append(c.p.Databases, plan)
	return nil
}

func (c *converter) dbName(name string) bool {
	for _, db := range c.p.Databases {
		if db.Name == name {
			return true
		}
	}
	return false
}

func major(v string) int {
	n, _ := strconv.Atoi(strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0])
	return n
}

// pickVersion keeps the source major when supported, else the nearest newer
// one, else the newest. An unknown version gets the newest, which can restore
// dumps from any older source.
func pickVersion(source string, supported []string) string {
	if source != "" {
		for _, v := range supported {
			if major(v) >= major(source) {
				return v
			}
		}
	}
	return supported[len(supported)-1]
}

func (c *converter) service(name string, s Service) {
	svc := spec.Service{Image: s.Image, Port: int32(s.Port), Public: s.Public || len(s.Domains) > 0, Healthcheck: s.Healthcheck}
	if s.Port < 0 || s.Port > 65535 {
		c.warn("Service %s: port %d is invalid and was dropped.", s.Name, s.Port)
		svc.Port = 0
	}
	if s.Replicas > 20 {
		c.warn("Service %s runs %d replicas; Hakopod allows at most 20.", s.Name, s.Replicas)
		svc.Replicas = 20
	} else if s.Replicas > 1 {
		svc.Replicas = int32(s.Replicas)
	}
	if svc.Healthcheck != "" && checks("x", spec.Service{Image: PlaceholderImage, Port: 80, Healthcheck: svc.Healthcheck}) != nil {
		c.warn("Service %s: healthcheck %q is not an HTTP path and was dropped.", s.Name, svc.Healthcheck)
		svc.Healthcheck = ""
	}

	// Railway cron services become scheduled jobs.
	var job *spec.Job
	var pathCrons []Cron
	for _, cr := range s.Crons {
		if cr.Path != "" {
			pathCrons = append(pathCrons, cr)
		} else if job == nil {
			job = &spec.Job{Schedule: &spec.JobSchedule{Cron: cr.Schedule, Timezone: "UTC"}}
			if cr.Command != "" && s.Repo == nil {
				svc.Command = []string{"sh", "-c", cr.Command}
			}
		} else {
			c.warn("Service %s has more than one cron schedule; only %q is kept.", s.Name, job.Schedule.Cron)
		}
	}

	var build *BuildPlan
	if s.Repo != nil {
		build = c.build(name, s, &svc)
		svc.Image = PlaceholderImage
		svc.Suspended = true
	} else if s.Image == "" {
		c.warn("Service %s has neither a repository nor an image; it is not migrated.", s.Name)
		return
	}

	if job != nil {
		if svc.Port != 0 || svc.Public || len(s.Domains) > 0 {
			c.warn("Service %s runs on a cron schedule, so it becomes a Hakopod job without a port or domains.", s.Name)
		}
		svc.Port, svc.Public, svc.Healthcheck, svc.Replicas = 0, false, "", 1
		svc.Job = job
		if err := checks(name, spec.Service{Image: PlaceholderImage, Job: &spec.Job{Schedule: &spec.JobSchedule{Cron: job.Schedule.Cron}}}); err != nil {
			c.warn("Service %s: cron schedule %q is not a valid five-field schedule; it runs as a regular worker instead.", s.Name, job.Schedule.Cron)
			svc.Job = nil
		}
		if build != nil {
			build.Public = false
		}
	} else if svc.Public && svc.Port == 0 {
		svc.Port = 8080
		c.warn("Service %s is public but its port is unknown; Hakopod assumes 8080.", s.Name)
	}

	if len(s.Volumes) > 0 {
		vol := &spec.Volume{MountPath: s.Volumes[0], SizeGiB: VolumeSizeGiB}
		probe := spec.Service{Image: PlaceholderImage, Volume: vol}
		if checks("x", probe) == nil {
			svc.Volume = vol
			if svc.Replicas > 1 {
				c.warn("Service %s keeps a volume, so it runs one replica instead of %d.", s.Name, svc.Replicas)
				svc.Replicas = 1
			}
			c.warn("Service %s: an empty %d GiB volume is mounted at %s; data from the source volume is not copied.", s.Name, VolumeSizeGiB, vol.MountPath)
		} else {
			c.warn("Service %s: volume %s cannot be recreated at that path; data is not copied.", s.Name, s.Volumes[0])
		}
		for _, extra := range s.Volumes[1:] {
			c.warn("Service %s: only one volume is recreated; %s is not, and its data is not copied.", s.Name, extra)
		}
	}

	env, secrets, bindings := c.variables(name, s)
	if svc.Port != 0 && svc.Job == nil {
		if _, ok := env["PORT"]; !ok && secrets["PORT"] == (spec.SecretRef{}) {
			env["PORT"] = strconv.Itoa(int(svc.Port))
		}
	}
	if len(env) > 0 {
		svc.Env = env
	}
	if len(secrets) > 0 {
		svc.Secrets = secrets
	}
	c.p.Spec.Services[name] = svc
	c.p.Bindings = append(c.p.Bindings, bindings...)
	if build != nil {
		if build.Framework != nil {
			build.BuildArgs = buildArgs(env)
		}
		c.p.Builds = append(c.p.Builds, *build)
	}
	if svc.Public {
		for _, host := range s.Domains {
			host = strings.ToLower(strings.TrimSuffix(host, "."))
			if !spec.ValidHostname(host) {
				c.warn("Domain %s is not a valid hostname for Hakopod and was skipped.", host)
			} else if other, ok := c.p.Domains[host]; ok && other != name {
				c.warn("Domain %s is attached to two services; it stays on %s.", host, other)
			} else {
				c.p.Domains[host] = name
			}
		}
	}
	for _, cr := range pathCrons {
		c.pathCron(name, s, svc, cr, env, secrets)
	}
}

// build maps source build settings into a Hakopod build configuration.
func (c *converter) build(name string, s Service, svc *spec.Service) *BuildPlan {
	r := s.Repo
	b := &BuildPlan{Service: name, Provider: r.Provider, ConnectionID: c.opts.GitConnectionID, Repository: r.Owner + "/" + r.Name, Branch: r.Branch, ContextPath: ".", Preset: "auto"}
	if b.Provider == "" {
		b.Provider = "github"
	}
	if b.Branch == "" {
		b.Branch = "main"
	}
	if dir := path.Clean("/" + r.RootDir); dir != "/" {
		b.ContextPath = strings.TrimPrefix(dir, "/")
	}
	plan := c.frameworkPlan(s)
	if plan == nil {
		b.Mode = "buildpacks"
		if svc.Port == 0 {
			svc.Port = 8080
		}
		c.warn("Service %s is built with buildpacks auto-detection; review the build before deploying.", s.Name)
	} else {
		if plan.Runtime == "node" && svc.Port != 0 {
			plan.Port = int(svc.Port)
		} else if plan.Runtime == "static" && svc.Port != 0 && svc.Port != 8080 {
			c.warn("Service %s is served as static files on port 8080 instead of %d.", s.Name, svc.Port)
		}
		svc.Port = int32(plan.Port)
		if err := framework.Validate(*plan); err != nil {
			c.warn("Service %s: the build settings do not form a valid framework build (%v); it uses buildpacks instead.", s.Name, err)
			plan = nil
			b.Mode = "buildpacks"
		} else {
			b.Mode = "framework"
			b.Framework = plan
		}
	}
	b.Port, b.Public = int(svc.Port), svc.Public
	return b
}

func packageManager(commands ...string) string {
	for _, cmd := range commands {
		switch first := strings.Fields(cmd + " x")[0]; first {
		case "npm", "pnpm", "yarn", "bun":
			return first
		}
	}
	return "npm"
}

func (c *converter) frameworkPlan(s Service) *framework.Plan {
	if s.Framework == "" && s.InstallCommand == "" && s.BuildCommand == "" && s.StartCommand == "" && s.OutputDir == "" {
		return nil
	}
	// Framework builds are Node or static only. Commands for another runtime
	// (python app.py, ./server) are left to buildpacks.
	if s.Framework == "" && s.OutputDir == "" && !regexp.MustCompile(`(^|[\s;&|/])(npm|npx|pnpm|yarn|bun|bunx|node)(\s|$)`).MatchString(s.InstallCommand+" ; "+s.BuildCommand+" ; "+s.StartCommand) {
		c.warn("Service %s does not look like a Node app; its source build and start commands are not carried over.", s.Name)
		return nil
	}
	pm := packageManager(s.InstallCommand, s.BuildCommand, s.StartCommand)
	install := map[string]string{"npm": "npm install", "pnpm": "pnpm install --frozen-lockfile", "yarn": "yarn install --frozen-lockfile", "bun": "bun install --frozen-lockfile"}[pm]
	p := framework.Plan{Framework: "node", Runtime: "node", PackageManager: pm, InstallCommand: install, BuildCommand: pm + " run build", StartCommand: pm + " run start", Port: 3000}
	static := func(fw, out string) {
		p.Framework, p.Runtime, p.OutputDirectory, p.StartCommand, p.Port = fw, "static", out, "", 8080
	}
	switch s.Framework {
	case "nextjs":
		p.Framework = "nextjs"
		p.StartCommand = "./node_modules/.bin/next start --hostname 0.0.0.0"
	case "vite", "vue", "svelte", "preact", "solidstart":
		static("vite", "dist")
	case "astro":
		static("astro", "dist")
	case "create-react-app":
		static("static", "build")
	case "gatsby":
		static("static", "public")
	case "sveltekit", "sveltekit-1":
		p.Framework = "sveltekit"
		p.StartCommand = "node build/index.js"
		c.warn("Service %s uses SvelteKit; switch it to adapter-node before the first build.", s.Name)
	case "":
		if s.Repo != nil && s.StartCommand == "" && s.OutputDir != "" {
			static("static", "")
		} else if s.StartCommand == "" {
			c.warn("Service %s: no start command is known; Hakopod runs `%s run start`.", s.Name, pm)
		}
	default:
		c.warn("Service %s uses the %s framework, which Hakopod has no preset for; it is built as a Node app that runs `%s run start`.", s.Name, s.Framework, pm)
	}
	if s.InstallCommand != "" {
		p.InstallCommand = s.InstallCommand
	}
	if s.BuildCommand != "" {
		p.BuildCommand = s.BuildCommand
	}
	if p.Runtime == "node" && s.StartCommand != "" {
		p.StartCommand = s.StartCommand
	}
	if p.Runtime == "static" && s.OutputDir != "" {
		p.OutputDirectory = strings.Trim(path.Clean(s.OutputDir), "/")
	}
	return &p
}

func buildArgs(env map[string]string) map[string]string {
	args := map[string]string{}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, prefix := range publicBuildPrefixes {
			if strings.HasPrefix(k, prefix) && len(args) < 32 && spec.ValidateBuildArguments(map[string]string{k: env[k]}) == nil {
				args[k] = env[k]
				break
			}
		}
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// variables splits a service's variables into plain env, secret references
// (values into Proposal.Secrets) and database bindings.
func (c *converter) variables(name string, s Service) (map[string]string, map[string]spec.SecretRef, []BindingPlan) {
	env := map[string]string{}
	secrets := map[string]spec.SecretRef{}
	var bindings []BindingPlan
	for _, v := range s.Variables {
		if !envName.MatchString(v.Name) {
			c.warn("Service %s: variable %q is not a valid environment name and was skipped.", s.Name, v.Name)
			continue
		}
		if ref := v.DatabaseRef; ref != nil {
			if b, ok := c.databaseVariable(name, s, v, env); ok {
				bindings = append(bindings, b)
			}
			continue
		}
		value, secret := v.Value, v.Sensitive || v.Unreadable
		if v.Unreadable {
			supplied, ok := c.opts.Values[name][v.Name]
			if !ok {
				c.p.Missing = append(c.p.Missing, MissingVariable{Service: name, Name: v.Name})
				continue
			}
			value = supplied
		}
		if strings.IndexByte(value, 0) >= 0 {
			c.warn("Service %s: variable %s contains a NUL byte and was skipped.", s.Name, v.Name)
			continue
		}
		if !secret && !spec.SensitiveEnv(v.Name, value) && len(value) <= 4096 {
			env[v.Name] = value
			continue
		}
		if value == "" {
			c.warn("Service %s: secret variable %s is empty and was skipped.", s.Name, v.Name)
			continue
		}
		ref := SecretName(c.opts.SecretKey, c.opts.Project, c.opts.Environment, c.opts.Application, name, v.Name)
		c.p.Secrets[ref] = value
		secrets[v.Name] = spec.SecretRef{Ref: ref}
	}
	return env, secrets, bindings
}

// databaseVariable maps one database reference. A managed binding exposes a
// single connection URL in the named variable; of the other parts only the
// fixed login and database names have an equivalent, as plain values.
func (c *converter) databaseVariable(name string, s Service, v Variable, env map[string]string) (BindingPlan, bool) {
	db, ok := c.dbs[v.DatabaseRef.Database]
	if !ok {
		c.warn("Service %s: %s refers to database %s, which is not migrated; set it yourself.", s.Name, v.Name, v.DatabaseRef.Database)
		return BindingPlan{}, false
	}
	protocol := map[Engine]string{PostgreSQL: "postgres", Redis: "redis", MySQL: "mysql", MongoDB: "mongodb"}[db.Engine]
	switch v.DatabaseRef.Part {
	case "url":
		b := spec.Binding{Protocol: protocol, Endpoint: "read_write"}
		if db.Engine == MongoDB {
			b.Endpoint, b.ClusterAware = "cluster", true
			c.warn("Service %s: %s connects to the MongoDB replica set; the driver must support replica sets.", s.Name, v.Name)
		}
		c.warn("Clients of database %s must trust its private CA, mounted at /var/run/secrets/hakopod-database/<database ID>.crt.", db.Name)
		return BindingPlan{Service: name, Variable: v.Name, Database: db.Name, Binding: b}, true
	case "user":
		env[v.Name] = map[Engine]string{PostgreSQL: "app", Redis: "default", MySQL: "app", MongoDB: "app"}[db.Engine]
	case "database":
		env[v.Name] = map[Engine]string{PostgreSQL: "app", Redis: "0", MySQL: "app", MongoDB: "app"}[db.Engine]
	default:
		c.warn("Service %s: %s holds the %s of database %s, which Hakopod only provides inside a connection URL; read it from a URL variable instead.", s.Name, v.Name, v.DatabaseRef.Part, db.Name)
	}
	return BindingPlan{}, false
}

// pathCron adds a job that requests a Vercel cron path from the service over
// the private network, with Vercel's CRON_SECRET bearer header when set.
func (c *converter) pathCron(name string, s Service, svc spec.Service, cr Cron, env map[string]string, secrets map[string]spec.SecretRef) {
	if svc.Port == 0 || !cronPath.MatchString(cr.Path) {
		c.warn("Service %s: cron path %q cannot be called and was skipped.", s.Name, cr.Path)
		return
	}
	job := spec.Service{Image: CronImage, Job: &spec.Job{Schedule: &spec.JobSchedule{Cron: cr.Schedule, Timezone: "UTC"}}}
	if checks("x", job) != nil {
		c.warn("Service %s: cron schedule %q for %s is not a valid five-field schedule and was skipped.", s.Name, cr.Schedule, cr.Path)
		return
	}
	header := ""
	if ref, ok := secrets["CRON_SECRET"]; ok {
		job.Secrets = map[string]spec.SecretRef{"CRON_SECRET": ref}
		header = `--header "Authorization: Bearer $CRON_SECRET" `
	} else if value, ok := env["CRON_SECRET"]; ok {
		job.Env = map[string]string{"CRON_SECRET": value}
		header = `--header "Authorization: Bearer $CRON_SECRET" `
	}
	url := fmt.Sprintf("http://%s:%d%s", name, svc.Port, cr.Path)
	job.Command = []string{"sh", "-c", "wget -q -O /dev/null " + header + "'" + url + "'"}
	c.p.Spec.Services[c.unique(trimName(name, 35)+"-cron")] = job
	c.warn("Vercel cron %s on %s runs as a Hakopod job that requests %s.", cr.Path, s.Name, url)
}
