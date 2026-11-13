// Package migrate reads a project from another hosting platform (Railway or
// Vercel) into a neutral Inventory and turns it into a Hakopod application:
// services, variables, secrets, domains, build configurations and managed
// databases. Provider clients only read; they never change the source project.
// The source API token is used for one migration and is never stored here.
package migrate

// Provider names a platform a project can be migrated from.
type Provider string

const (
	Railway Provider = "railway"
	Vercel  Provider = "vercel"
)

// Project is one source project a token can read, for the picker.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Team or workspace the project belongs to, when the provider has one.
	Team string `json:"team,omitempty"`
	// Environments to choose from (Railway); Vercel always migrates production.
	Environments []Environment `json:"environments,omitempty"`
}

type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Inventory is everything read from one source project and environment, in
// provider-neutral form. Converters only depend on this type.
type Inventory struct {
	Provider    Provider   `json:"provider"`
	ProjectID   string     `json:"project_id"`
	ProjectName string     `json:"project_name"`
	Environment string     `json:"environment,omitempty"`
	Services    []Service  `json:"services"`
	Databases   []Database `json:"databases"`
	// Warnings are things the reader saw but cannot carry over, in plain words.
	Warnings []string `json:"warnings,omitempty"`
}

// Service is a long-running app or worker on the source platform.
type Service struct {
	Name string `json:"name"`
	// Exactly one of Repo and Image is set.
	Repo  *Repo  `json:"repo,omitempty"`
	Image string `json:"image,omitempty"`

	// Build and run settings as the source platform has them; empty means the
	// platform detected them. Hakopod's framework detection fills the gaps.
	Framework      string `json:"framework,omitempty"` // e.g. nextjs, as Vercel names it
	InstallCommand string `json:"install_command,omitempty"`
	BuildCommand   string `json:"build_command,omitempty"`
	StartCommand   string `json:"start_command,omitempty"`
	OutputDir      string `json:"output_dir,omitempty"`

	Port        int    `json:"port,omitempty"`
	Public      bool   `json:"public"`
	Healthcheck string `json:"healthcheck,omitempty"`
	Replicas    int    `json:"replicas,omitempty"`

	// Variables as the service sees them, by name. A variable that points at a
	// database in this inventory is a DatabaseRef instead of a plain value.
	Variables []Variable `json:"variables"`
	// Custom domains only; platform domains (*.up.railway.app, *.vercel.app)
	// are left out.
	Domains []string `json:"domains,omitempty"`
	// Scheduled runs of this service, from Railway cron schedules or vercel.json crons.
	Crons []Cron `json:"crons,omitempty"`
	// Persistent volumes on the source; data in them is not copied.
	Volumes []string `json:"volumes,omitempty"`
}

type Repo struct {
	Provider string `json:"provider"` // github
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	RootDir  string `json:"root_dir,omitempty"`
}

type Variable struct {
	Name string `json:"name"`
	// Value is empty when Unreadable is set or when DatabaseRef is set.
	Value string `json:"value,omitempty"`
	// Unreadable marks a value the provider will not return through its API
	// (Vercel sensitive variables); the user supplies it before the migration runs.
	Unreadable bool `json:"unreadable,omitempty"`
	// Sensitive is the provider's own hint that the value is a secret.
	Sensitive bool `json:"sensitive,omitempty"`
	// DatabaseRef names a Database in this inventory whose connection value
	// this variable carries, with which part of it (url, host, port, user,
	// password, database).
	DatabaseRef *DatabaseRef `json:"database_ref,omitempty"`
}

type DatabaseRef struct {
	Database string `json:"database"`
	Part     string `json:"part"` // url, host, port, user, password, database
}

type Cron struct {
	Schedule string `json:"schedule"`
	Path     string `json:"path,omitempty"` // Vercel: HTTP path to call
	Command  string `json:"command,omitempty"`
}

// Engine of a source database. Data is copied for postgresql and redis.
type Engine string

const (
	PostgreSQL Engine = "postgresql"
	Redis      Engine = "redis"
	MySQL      Engine = "mysql"
	MongoDB    Engine = "mongodb"
)

// Database is a database the project uses: a Railway database service, or a
// Vercel storage integration (Neon Postgres, Upstash Redis).
type Database struct {
	Name    string `json:"name"`
	Engine  Engine `json:"engine"`
	Version string `json:"version,omitempty"` // major version when known, e.g. 16
	// URL is a connection string the copy job can reach from the internet, with
	// credentials. It is a secret: never logged, never returned by the API.
	URL string `json:"-"`
	// Reachable is false when the source has no public endpoint (a Railway
	// database without a TCP proxy); its data cannot be copied.
	Reachable bool `json:"reachable"`
	// Approximate size, when the provider reports it.
	SizeBytes int64 `json:"size_bytes,omitempty"`
}
