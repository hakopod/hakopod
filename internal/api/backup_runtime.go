package api

import (
	"context"
	"fmt"
	"io"
	"net/netip"
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
	BlockedEndpointCIDRs              []netip.Prefix
}

func (s *Server) ConfigureBackups(config BackupConfig) {
	if config.PGDumpPath == "" {
		config.PGDumpPath = "pg_dump"
	}
	if config.StateDir == "" {
		config.StateDir = filepath.Join(os.TempDir(), "hakopod-backups")
	}
	// One runtime serves both roles: it streams logical dumps and it drives the
	// engines that back themselves up.
	runtime := &backupRuntime{server: s, config: config}
	s.Backups = &backup.Service{Repo: s.Store, Runtime: runtime, Engine: runtime, CredentialKey: s.authEncryptionKey(), StateDir: config.StateDir, MaxBytes: config.MaxBytes, BlockedEndpointCIDRs: config.BlockedEndpointCIDRs}
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
	case "clickhouse/clickhouse-server":
		source.Engine = "clickhouse"
		// The ClickHouse blueprint declares no database variable at all, only a
		// password secret, so the server's own default database is the one to
		// back up unless the service names another.
		source.Database = service.Env["CLICKHOUSE_DB"]
		if source.Database == "" {
			source.Database = service.Env["CLICKHOUSE_DATABASE"]
		}
		if source.Database == "" {
			source.Database = "default"
		}
	default:
		return source, false
	}
	return source, source.Validate() == nil
}
func (r *backupRuntime) Targets(ctx context.Context) ([]backup.Target, error) {
	result := []backup.Target{}
	if store.BackupManagementAllowed(ctx) {
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
		result = append(result, management)

	}
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

// ClickHouse writes its own backup to object storage, so hakopod only issues a
// statement and watches system.backups. Both bounds below are short on purpose:
// a statement issued with ASYNC returns as soon as the server has accepted it,
// and a poll is a single point lookup by id.
const clickhouseStartTimeout = 60 * time.Second
const clickhousePollTimeout = 20 * time.Second

// clickhouseScript reads one statement from standard input and the password
// from the pod environment, exactly as the PostgreSQL and MySQL scripts do.
// Nothing secret may appear in argv: every element of the exec command becomes
// a repeated command query parameter on the Kubernetes exec request URL, which
// kube-apiserver audit, API-server access logs and any proxy in front of them
// record verbatim. clickhouse-client speaks only the native protocol, so it
// connects to port 9000 rather than the service's HTTP port. The password must
// be a separate argument; an attached form is read as part of the option.
const clickhouseScript = `set -eu
exec clickhouse-client --host=127.0.0.1 --port=9000 --user "${CLICKHOUSE_USER:-default}" --password "${CLICKHOUSE_PASSWORD:?database password is unavailable}"`

// The three ClickHouse statuses this implementation understands. Anything else,
// including an empty or unparseable response, is an error rather than a state.
const (
	clickhouseBackupRunning  = "CREATING_BACKUP"
	clickhouseBackupDone     = "BACKUP_CREATED"
	clickhouseBackupFailed   = "BACKUP_FAILED"
	clickhouseRestoreRunning = "RESTORING"
	clickhouseRestoreDone    = "RESTORED"
	clickhouseRestoreFailed  = "RESTORE_FAILED"
)

// clickhouseOutputLimit bounds the captured poll output. One row of three small
// columns is a few dozen bytes; anything beyond this is a broken server.
const clickhouseOutputLimit = 4096

type boundedWriter struct {
	buffer []byte
	limit  int
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if len(w.buffer)+len(data) > w.limit {
		return 0, fmt.Errorf("database response exceeded its expected size")
	}
	w.buffer = append(w.buffer, data...)
	return len(data), nil
}

// clickhouseString quotes a value for a single-quoted ClickHouse string. It is
// applied to credentials and destination fields alike, so a value containing a
// quote cannot end the literal early.
func clickhouseString(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

// clickhouseIdentifier refuses anything outside the character set the declared
// source and the generated restore name are already validated against, so a
// database name can never carry statement syntax.
func clickhouseIdentifier(name string) (string, error) {
	if name == "" || len(name) > 63 {
		return "", fmt.Errorf("invalid ClickHouse database name")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", fmt.Errorf("invalid ClickHouse database name")
		}
	}
	return "`" + name + "`", nil
}

// clickhouseOperationID names the operation after the prefix it writes, so a
// poll is a point lookup and an operator can correlate a row in system.backups
// with the objects in the bucket. A random identifier would do neither.
func clickhouseOperationID(kind, prefix string) (string, error) {
	if prefix == "" || len(prefix) > 160 {
		return "", fmt.Errorf("engine backup requires a bounded object prefix")
	}
	name := []rune("hakopod-" + kind + "-")
	for _, c := range prefix {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			name = append(name, c)
			continue
		}
		name = append(name, '-')
	}
	return string(name), nil
}

// clickhouseReference accepts only what clickhouseOperationID produces, so a
// stored reference cannot carry statement syntax into the poll query.
func clickhouseReference(ref string) error {
	if !strings.HasPrefix(ref, "hakopod-") || len(ref) > 200 {
		return fmt.Errorf("engine operation reference is missing or malformed")
	}
	for _, letter := range ref {
		if !(letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' || letter >= '0' && letter <= '9' || letter == '_' || letter == '-') {
			return fmt.Errorf("engine operation reference is missing or malformed")
		}
	}
	return nil
}

// clickhouseDestinationURL is the tree the engine writes under. The endpoint,
// bucket and prefix are validated when the destination is saved.
func clickhouseDestinationURL(d backup.Destination, prefix string) string {
	return strings.TrimSuffix(d.Endpoint, "/") + "/" + d.Bucket + "/" + strings.Trim(prefix, "/")
}

func (r *backupRuntime) clickhouseStatement(ctx context.Context, target backup.Target, statement string, timeout time.Duration, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.execute(ctx, target, clickhouseScript, strings.NewReader(statement), out)
}

func (r *backupRuntime) StartBackup(ctx context.Context, target backup.Target, d backup.Destination, c backup.Credentials, prefix string) (string, error) {
	if target.Kind != "database" || target.Engine != "clickhouse" {
		return "", fmt.Errorf("engine-performed backup requires a ClickHouse service")
	}
	database, err := clickhouseIdentifier(target.Database)
	if err != nil {
		return "", err
	}
	id, err := clickhouseOperationID("backup", prefix)
	if err != nil {
		return "", err
	}
	statement := "BACKUP DATABASE " + database + " TO S3(" + clickhouseString(clickhouseDestinationURL(d, prefix)) + ", " + clickhouseString(c.AccessKeyID) + ", " + clickhouseString(c.SecretAccessKey) + ") SETTINGS id = " + clickhouseString(id) + " ASYNC\n"
	if err = r.clickhouseStatement(ctx, target, statement, clickhouseStartTimeout, io.Discard); err != nil {
		return "", fmt.Errorf("ClickHouse did not accept the backup statement; verify the service, its password and the destination endpoint, bucket and credentials")
	}
	return id, nil
}

func (r *backupRuntime) StartRestore(ctx context.Context, target backup.Target, d backup.Destination, c backup.Credentials, prefix string, sourceDatabase string) (string, error) {
	// The same defensive check the logical restore path applies: the caller
	// supplies a generated fresh name, and this refuses anything else.
	if target.Kind != "database" || target.Engine != "clickhouse" || len(target.Database) != 31 || !strings.HasPrefix(target.Database, "hp_restore_") {
		return "", fmt.Errorf("restore target must be a generated fresh database")
	}
	for _, letter := range target.Database {
		if !(letter >= 'a' && letter <= 'z' || letter >= '0' && letter <= '9' || letter == '_') {
			return "", fmt.Errorf("invalid restore database name")
		}
	}
	into, err := clickhouseIdentifier(target.Database)
	if err != nil {
		return "", err
	}
	// The database name comes from the artifact, not from what this service
	// declares now: recovering into a service that declares a different database
	// must still restore what was backed up.
	if sourceDatabase == "" {
		return "", fmt.Errorf("the artifact records no source database to restore")
	}
	from, err := clickhouseIdentifier(sourceDatabase)
	if err != nil {
		return "", err
	}
	id, err := clickhouseOperationID("restore", prefix)
	if err != nil {
		return "", err
	}
	statement := "RESTORE DATABASE " + from + " AS " + into + " FROM S3(" + clickhouseString(clickhouseDestinationURL(d, prefix)) + ", " + clickhouseString(c.AccessKeyID) + ", " + clickhouseString(c.SecretAccessKey) + ") SETTINGS id = " + clickhouseString(id) + " ASYNC\n"
	if err = r.clickhouseStatement(ctx, target, statement, clickhouseStartTimeout, io.Discard); err != nil {
		return "", fmt.Errorf("ClickHouse did not accept the restore statement; verify the service, its password and the destination endpoint, bucket and credentials")
	}
	return id, nil
}

func (r *backupRuntime) PollBackup(ctx context.Context, target backup.Target, ref string) (backup.EngineStatus, error) {
	return r.clickhousePoll(ctx, target, ref, clickhouseBackupRunning, clickhouseBackupDone, clickhouseBackupFailed, "ClickHouse reported that it could not write this backup. Check the ClickHouse server log for the failure; the reason is withheld here because the engine's own message repeats the statement, which contains the destination credentials.")
}

func (r *backupRuntime) PollRestore(ctx context.Context, target backup.Target, ref string) (backup.EngineStatus, error) {
	return r.clickhousePoll(ctx, target, ref, clickhouseRestoreRunning, clickhouseRestoreDone, clickhouseRestoreFailed, "ClickHouse reported that it could not restore these files. Check the ClickHouse server log for the failure; the reason is withheld here because the engine's own message repeats the statement, which contains the destination credentials. The target database may be partial and is never dropped automatically.")
}

func (r *backupRuntime) clickhousePoll(ctx context.Context, target backup.Target, ref, running, done, failed, failure string) (backup.EngineStatus, error) {
	status := backup.EngineStatus{}
	if target.Kind != "database" || target.Engine != "clickhouse" {
		return status, fmt.Errorf("engine-performed backup requires a ClickHouse service")
	}
	if err := clickhouseReference(ref); err != nil {
		return status, err
	}
	// TSV escapes a tab or newline inside a value, so three fields on one line
	// can never be confused with a status that contains whitespace.
	statement := "SELECT status, total_size, num_files FROM system.backups WHERE id = " + clickhouseString(ref) + " ORDER BY start_time DESC LIMIT 1 FORMAT TSV\n"
	out := &boundedWriter{limit: clickhouseOutputLimit}
	if err := r.clickhouseStatement(ctx, target, statement, clickhousePollTimeout, out); err != nil {
		return status, fmt.Errorf("could not read the ClickHouse backup status from the database service")
	}
	return clickhouseEngineStatus(string(out.buffer), running, done, failed, failure)
}

// clickhouseEngineStatus refuses anything it does not recognise. An empty
// response means the server has forgotten the operation, which is a failure to
// report to an operator, never a completed backup.
func clickhouseEngineStatus(output, running, done, failed, failure string) (backup.EngineStatus, error) {
	status := backup.EngineStatus{}
	line := strings.TrimRight(output, "\r\n")
	if line == "" {
		return status, fmt.Errorf("ClickHouse no longer reports this operation; treat it as unfinished and check the server")
	}
	if strings.ContainsAny(line, "\r\n") {
		return status, fmt.Errorf("ClickHouse returned more than one status row for this operation")
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		return status, fmt.Errorf("ClickHouse status response was not the expected three fields")
	}
	bytes, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return status, fmt.Errorf("ClickHouse reported an unreadable backup size")
	}
	files, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return status, fmt.Errorf("ClickHouse reported an unreadable backup file count")
	}
	status.Bytes = bytes
	status.Files = files
	switch fields[0] {
	case running:
		return status, nil
	case done:
		status.Done = true
		return status, nil
	case failed:
		status.Done = true
		status.Failed = true
		status.Message = failure
		return status, nil
	}
	return backup.EngineStatus{}, fmt.Errorf("ClickHouse reported an unrecognized operation status")
}
