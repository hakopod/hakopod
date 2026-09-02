package api

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/types"
)

type BackupConfig struct {
	ManagedPostgres                   bool
	DatabaseURL, PGDumpPath, StateDir string
	MaxBytes                          int64
}

func (s *Server) ConfigureBackups(config BackupConfig) {
	if config.PGDumpPath == "" {
		config.PGDumpPath = "pg_dump"
	}
	if config.StateDir == "" {
		config.StateDir = filepath.Join(os.TempDir(), "hakopod-backups")
	}
	s.Backups = &backup.Service{Repo: s.Store, Runtime: &backupRuntime{server: s, config: config}, CredentialKey: s.authEncryptionKey(), StateDir: config.StateDir, MaxBytes: config.MaxBytes}
}
func (s *Server) RunBackups(ctx context.Context) {
	if s.Backups != nil {
		s.Backups.Run(ctx)
	}
}

type backupRuntime struct {
	server *Server
	config BackupConfig
}

func declaredBackupSource(a store.Application, name string, service spec.Service) (backup.Source, bool) {
	image := strings.Split(service.Image, "@")[0]
	image = strings.Split(image, ":")[0]
	image = strings.TrimPrefix(image, "docker.io/library/")
	image = strings.TrimPrefix(image, "docker.io/")
	source := backup.Source{Kind: "database", ApplicationID: a.ID, Service: name}
	switch image {
	case "postgres":
		source.Engine = "postgresql"
		source.Database = service.Env["POSTGRES_DB"]
		if source.Database == "" {
			source.Database = service.Env["POSTGRES_USER"]
		}
		if source.Database == "" {
			source.Database = "postgres"
		}
	case "mysql", "mysql/mysql-server":
		source.Engine = "mysql"
		source.Database = service.Env["MYSQL_DATABASE"]
	default:
		return source, false
	}
	return source, source.Validate() == nil
}
func (r *backupRuntime) Targets(ctx context.Context) ([]backup.Target, error) {
	management := backup.Target{Source: backup.Source{Kind: "management", Engine: "postgresql"}, Available: r.config.DatabaseURL != ""}
	if r.config.ManagedPostgres {
		resolved, err := r.Resolve(ctx, management.Source)
		if err != nil {
			management.Available = false
			management.Message = "Installer-managed PostgreSQL ownership/readiness verification failed."
		} else {
			management = resolved
			management.Message = "Logical dump using the pinned installer database client; preserve encryption key and configuration separately."
		}
	} else if _, err := exec.LookPath(r.config.PGDumpPath); err != nil {
		management.Available = false
		management.Message = "Configure a compatible official pg_dump executable with HAKOPOD_PG_DUMP_PATH."
	} else {
		management.Message = "Logical management database only. Preserve the authentication encryption key and configuration separately."
	}
	result := []backup.Target{management}
	applications, err := r.server.Store.BackupApplications(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range applications {
		for name, service := range a.Spec.Services {
			source, ok := declaredBackupSource(a, name, service)
			if !ok {
				continue
			}
			target := backup.Target{Source: source, ApplicationName: a.Name, Revision: a.Revision, Available: a.Status == "healthy"}
			if !target.Available {
				target.Message = "Database application must have a successful deployed revision."
			} else {
				target.Message = "Pod ownership, readiness and database tools are checked before execution."
			}
			result = append(result, target)
			if len(result) >= 128 {
				return result, nil
			}
		}
	}
	return result, nil
}
func (r *backupRuntime) Resolve(ctx context.Context, source backup.Source) (backup.Target, error) {
	target := backup.Target{Source: source}
	if err := source.Validate(); err != nil {
		return target, err
	}
	if source.Kind == "management" {
		if r.config.ManagedPostgres {
			if r.server.Cluster == nil {
				return target, fmt.Errorf("managed PostgreSQL requires Kubernetes")
			}
			pod, uid, fence, err := r.server.Cluster.ManagedBackupPod(ctx)
			if err != nil {
				return target, fmt.Errorf("installer management database ownership or readiness check failed")
			}
			var expected string
			if err = r.server.Store.Pool.QueryRow(ctx, "SELECT current_database() || ':' || system_identifier::text FROM pg_control_system()").Scan(&expected); err != nil {
				return target, fmt.Errorf("cannot verify the active management PostgreSQL identity")
			}
			observed, err := r.server.Cluster.ManagedBackupIdentity(ctx, pod, uid, fence)
			if err != nil || observed != expected {
				return target, fmt.Errorf("managed PostgreSQL is not the active management database")
			}
			target.Pod = pod
			target.PodUID = string(uid)
			target.RuntimeFingerprint = fence
			target.Available = true
			return target, nil
		}
		if r.config.DatabaseURL == "" {
			return target, fmt.Errorf("management database connection is unavailable")
		}
		if _, err := exec.LookPath(r.config.PGDumpPath); err != nil {
			return target, fmt.Errorf("management backup requires a compatible pg_dump executable configured with HAKOPOD_PG_DUMP_PATH")
		}
		target.Available = true
		return target, nil
	}
	a, err := r.server.Store.Application(ctx, source.ApplicationID)
	if err != nil {
		return target, err
	}
	if a.Status != "healthy" {
		return target, fmt.Errorf("database application has no successful current revision")
	}
	service, ok := a.Spec.Services[source.Service]
	if !ok {
		return target, backup.ErrNotFound
	}
	expected, ok := declaredBackupSource(a, source.Service, service)
	if !ok || expected.Engine != source.Engine || expected.Database != source.Database {
		return target, fmt.Errorf("%w: backup source must match the database declared by the official PostgreSQL/MySQL service", backup.ErrInput)
	}
	if r.server.Cluster == nil {
		return target, fmt.Errorf("Kubernetes is unavailable")
	}
	options, uid, err := r.server.Cluster.BackupPod(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, source.Service)
	if err != nil {
		return target, fmt.Errorf("database pod is unavailable or not uniquely owned")
	}
	target.ApplicationName = a.Name
	target.Revision = a.Revision
	target.Pod = options.Pod
	target.PodUID = string(uid)
	target.Available = true
	return target, nil
}

const postgresDumpScript = `set -eu
export PGPASSWORD="${POSTGRES_PASSWORD:?database password is unavailable}"
exec pg_dump --host=127.0.0.1 --port=5432 --format=custom --compress=0 --no-owner --no-acl --username="${POSTGRES_USER:-postgres}" --dbname="$1"`
const mysqlDumpScript = `set -eu
export MYSQL_PWD="${MYSQL_PASSWORD:-${MYSQL_ROOT_PASSWORD:?database password is unavailable}}"
exec mysqldump --no-defaults --host=127.0.0.1 --port=3306 --user="${MYSQL_USER:-root}" --single-transaction --quick --routines --events --triggers --no-tablespaces --set-gtid-purged=OFF --column-statistics=0 --skip-add-drop-table -- "$1"`
const postgresCreateDatabaseScript = `set -eu
export PGPASSWORD="${POSTGRES_PASSWORD:?database password is unavailable}"
export PGUSER="${POSTGRES_USER:-postgres}"
exec createdb --host=127.0.0.1 --port=5432 --template=template0 -- "$1"`
const postgresRestoreScript = `set -eu
export PGPASSWORD="${POSTGRES_PASSWORD:?database password is unavailable}"
export PGUSER="${POSTGRES_USER:-postgres}"
exec pg_restore --host=127.0.0.1 --port=5432 --exit-on-error --single-transaction --no-owner --no-acl --dbname="$1"`
const mysqlCreateDatabaseScript = `set -eu
export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:?fresh-database restore requires the configured root password}"
# The database name is generated by the API and independently validated below.
case "$1" in hp_restore_*) ;; *) exit 64;; esac
case "$1" in *[!a-z0-9_]*) exit 64;; esac
exec mysql --no-defaults --host=127.0.0.1 --port=3306 --user=root --execute="CREATE DATABASE $1"`
const mysqlRestoreScript = `set -eu
export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:?fresh-database restore requires the configured root password}"
exec mysql --no-defaults --host=127.0.0.1 --port=3306 --user=root --binary-mode --database="$1"`

func (r *backupRuntime) Dump(ctx context.Context, target backup.Target, out io.Writer) error {
	if target.Kind == "management" {
		if r.config.ManagedPostgres {
			if err := r.server.Cluster.ManagedBackupDump(ctx, target.Pod, types.UID(target.PodUID), target.RuntimeFingerprint, out); err != nil {
				return fmt.Errorf("managed PostgreSQL dump failed or its ownership changed")
			}
			return nil
		}
		command := exec.CommandContext(ctx, r.config.PGDumpPath, "--format=custom", "--compress=0", "--no-owner", "--no-acl")
		command.WaitDelay = 5 * time.Second
		environment, err := managementDumpEnvironment(r.config.DatabaseURL)
		if err != nil {
			return err
		}
		command.Env = environment
		command.Stdout = out
		command.Stderr = io.Discard
		if err := command.Run(); err != nil {
			return fmt.Errorf("management pg_dump failed; verify client/server versions, connection and privileges")
		}
		return nil
	}
	script := postgresDumpScript
	if target.Engine == "mysql" {
		script = mysqlDumpScript
	}
	return r.execute(ctx, target, script, nil, out)
}

// pg_dump does not expand a URI supplied through PGDATABASE. Resolve the same
// pgx connection settings as the server, then use libpq environment variables;
// passwords, client-key passwords and connection options never enter argv.
func managementDumpEnvironment(dsn string) ([]string, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("management dump connection settings are invalid")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, fmt.Errorf("external management backup requires a PostgreSQL connection URL")
	}
	environment := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PG") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, "PGHOST="+config.Host, "PGPORT="+strconv.Itoa(int(config.Port)), "PGUSER="+config.User, "PGPASSWORD="+config.Password, "PGDATABASE="+config.Database, "PGCONNECT_TIMEOUT=10")
	for query, name := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslpassword": "PGSSLPASSWORD", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR", "target_session_attrs": "PGTARGETSESSIONATTRS", "options": "PGOPTIONS", "channel_binding": "PGCHANNELBINDING"} {
		value := u.Query().Get(query)
		if value == "" {
			value = os.Getenv(name)
		}
		if value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment, nil
}
func (r *backupRuntime) Restore(ctx context.Context, target backup.Target, input io.Reader) error {
	if target.Kind != "database" || len(target.Database) != 31 || !strings.HasPrefix(target.Database, "hp_restore_") {
		return fmt.Errorf("restore target must be a generated fresh database")
	}
	for _, c := range target.Database {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("invalid restore database name")
		}
	}
	script := postgresRestoreScript
	createScript := postgresCreateDatabaseScript
	if target.Engine == "mysql" {
		script = mysqlRestoreScript
		createScript = mysqlCreateDatabaseScript
	}
	// Creation must complete without a streaming stdin. A fast refusal (such as
	// an existing name) otherwise leaves the SPDY stdin copier waiting on a
	// remote process that never reads the dump. Each exec rechecks the pod UID.
	if err := r.execute(ctx, target, createScript, nil, io.Discard); err != nil {
		return err
	}
	return r.execute(ctx, target, script, input, io.Discard)
}
func (r *backupRuntime) execute(ctx context.Context, target backup.Target, script string, input io.Reader, out io.Writer) error {
	a, err := r.server.Store.Application(ctx, target.ApplicationID)
	if err != nil {
		return err
	}
	if a.Revision != target.Revision || a.Status != "healthy" {
		return fmt.Errorf("database application revision changed; create a new review or backup")
	}
	service, ok := a.Spec.Services[target.Service]
	if !ok {
		return backup.ErrNotFound
	}
	declared, ok := declaredBackupSource(a, target.Service, service)
	if !ok || declared.Engine != target.Engine {
		return fmt.Errorf("database service changed")
	}
	options := cluster.TerminalOptions{Pod: target.Pod, Container: "app", Command: []string{"/bin/sh", "-c", script, "hakopod-backup", target.Database}}
	t := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}
	if err = r.server.Cluster.BackupExec(ctx, t, target.Service, options, types.UID(target.PodUID), input, out, io.Discard); err != nil {
		return fmt.Errorf("database tool failed or the selected pod changed; verify backup tools and privileges. A fresh restore database may be partial and is never dropped automatically")
	}
	return nil
}
