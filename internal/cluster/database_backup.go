package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) DumpDatabase(ctx context.Context, d database.Resource, observed database.Observation, out io.Writer) error {
	if observed.Status != "ready" || observed.Revision != d.Revision {
		return fmt.Errorf("database backup requires verified current health")
	}
	if d.Spec.Engine == "vitess" {
		return c.dumpVitessDatabase(ctx, d, observed, out)
	}
	if d.Spec.Engine == "mysql" {
		return c.dumpMySQLDatabase(ctx, d, observed, out)
	}
	if d.Spec.Engine == "mongodb" {
		return c.dumpMongoDBDatabase(ctx, d, observed, out)
	}
	if d.Spec.Engine == "clickhouse" {
		return c.dumpClickHouseDatabase(ctx, d, observed, out)
	}
	if d.Spec.Engine == "oracle" {
		return c.dumpOracleDatabase(ctx, d, observed, out)
	}
	if d.Spec.Engine == "postgresql" {
		for _, m := range observed.Members {
			if m.Name == observed.Primary {
				return c.DatabaseExec(ctx, d, m, []string{"pg_dump", "--username=postgres", "--dbname=app", "--format=custom", "--compress=0", "--no-owner", "--no-acl"}, nil, out)
			}
		}
		return fmt.Errorf("PostgreSQL primary is unavailable")
	}
	if d.Spec.Engine != "redis" {
		return fmt.Errorf("unsupported managed database backup engine")
	}
	manifest := database.RedisArchive{SchemaVersion: 1, DatabaseID: d.ID, Revision: d.Revision, TopologyFingerprint: observed.TopologyFingerprint}
	for _, m := range observed.Members {
		if m.Role == "primary" {
			manifest.Shards = append(manifest.Shards, m)
		}
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID {
		return fmt.Errorf("Redis credentials are unavailable")
	}
	err = database.WriteRedisArchive(out, manifest, func(m database.Member, w io.Writer) error {
		if d.Spec.TLSRequired() {
			return c.captureRedisTLSSnapshot(ctx, d, m, secret.Data["password"], w)
		}
		input := append(append([]byte(nil), secret.Data["password"]...), '\n')
		// redis-cli's stdout mode retains a diskless replication EOF marker. A
		// private temporary file lets the official tool truncate it correctly and
		// redis-check-rdb verify the complete snapshot before streaming the bytes.
		script := `set -eu
IFS= read -r REDISCLI_AUTH
export REDISCLI_AUTH
umask 077
directory=$(mktemp -d /tmp/hakopod-snapshot.XXXXXXXX)
trap 'rm -rf "$directory"' EXIT HUP INT TERM
redis-cli --rdb "$directory/snapshot.rdb" >/dev/null
# Redis dispatches its bundled RDB checker through the executable name. The
# operator image contains redis-server but omits the usual checker symlink.
ln -s "$(command -v redis-server)" "$directory/redis-check-rdb"
"$directory/redis-check-rdb" "$directory/snapshot.rdb" >/dev/null
cat "$directory/snapshot.rdb"`
		return c.DatabaseExec(ctx, d, m, []string{"sh", "-c", script}, bytes.NewReader(input), w)
	})
	if err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != observed.TopologyFingerprint {
		return fmt.Errorf("Redis topology changed while capturing the backup")
	}
	return nil
}

func (c *Client) DatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	if d.Spec.Engine == "duckdb" {
		return c.myduckDatabaseEmpty(ctx, d, o)
	}
	if d.Spec.Engine == "vitess" {
		return c.vitessDatabaseEmpty(ctx, d, o)
	}
	if o.Status != "ready" {
		return fmt.Errorf("database health is not ready")
	}
	if d.Spec.Engine == "mysql" {
		return c.mysqlDatabaseEmpty(ctx, d, o)
	}
	if d.Spec.Engine == "mongodb" {
		return c.mongodbDatabaseEmpty(ctx, d, o)
	}
	if d.Spec.Engine == "clickhouse" {
		return c.clickhouseDatabaseEmpty(ctx, d, o)
	}
	if d.Spec.Engine == "oracle" {
		return c.oracleDatabaseEmpty(ctx, d, o)
	}
	if d.Spec.Engine == "postgresql" {
		for _, m := range o.Members {
			if m.Name == o.Primary {
				out := &databaseBoundedWriter{limit: 4096}
				query := `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','v','m','S','f')`
				if err := c.DatabaseExec(ctx, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", query}, nil, out); err != nil {
					return err
				}
				if strings.TrimSpace(out.String()) != "0" {
					return fmt.Errorf("recovery requires a separate empty database")
				}
				return nil
			}
		}
		return fmt.Errorf("PostgreSQL primary is unavailable")
	}
	if d.Spec.Engine != "redis" {
		return fmt.Errorf("unsupported managed database recovery engine")
	}
	for _, m := range o.Members {
		if m.Role == "primary" {
			value, err := c.redisCommand(ctx, d, m, "DBSIZE")
			if err != nil || strings.TrimSpace(value) != "0" {
				return fmt.Errorf("recovery requires a separate empty Redis database")
			}
		}
	}
	return nil
}
func (c *Client) RestorePostgresDatabase(ctx context.Context, d database.Resource, o database.Observation, input io.Reader) error {
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	for _, m := range o.Members {
		if m.Name == o.Primary {
			// A restored archive is untrusted SQL. Authenticate as the database
			// owner directly: SET ROLE from a superuser session could be reset by
			// archive SQL. Network isolation blocks application clients meanwhile.
			d.Status = "restoring"
			if err := c.databaseNetworkPolicy(ctx, d, func() error { return nil }); err != nil {
				return err
			}
			guard := `REVOKE CONNECT ON DATABASE app FROM PUBLIC; GRANT CONNECT ON DATABASE app TO app; SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='app' AND pid<>pg_backend_pid();`
			if err := c.DatabaseExec(ctx, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", guard}, nil, io.Discard); err != nil {
				return err
			}
			if err := c.DatabaseEmpty(ctx, d, o); err != nil {
				return err
			}
			secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
			if err != nil || secret.Labels[databaseOwner] != d.ID {
				return fmt.Errorf("recovery credentials are unavailable")
			}
			command := []string{"sh", "-c", `set -eu; IFS= read -r PGPASSWORD; export PGPASSWORD; exec pg_restore --host=127.0.0.1 --username=app --dbname=app --no-password --exit-on-error --single-transaction --no-owner --no-acl`}
			if d.Spec.TLSRequired() {
				trust, err := c.DatabaseTrust(ctx, d)
				if err != nil {
					return err
				}
				host := ""
				for _, endpoint := range o.Endpoints {
					if endpoint.Purpose == "read_write" {
						host = endpoint.Host
					}
				}
				if host == "" {
					return fmt.Errorf("verified recovery endpoint is unavailable")
				}
				// Only public CA material travels in argv. The password is consumed
				// from the first stdin line; the remaining binary archive is untouched.
				command = []string{"sh", "-c", `set -eu
IFS= read -r PGPASSWORD; export PGPASSWORD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
printf '%s' "$2" > "$work/ca.crt"
export PGHOST="$1" PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt" PGCONNECT_TIMEOUT=5
pg_restore --username=app --dbname=app --no-password --exit-on-error --single-transaction --no-owner --no-acl`, "restore-with-verified-tls", host, trust.CertificatePEM}
			}
			stream := io.MultiReader(bytes.NewReader(append(append([]byte(nil), secret.Data["password"]...), '\n')), input)
			if err := c.DatabaseExec(ctx, d, m, command, stream, io.Discard); err != nil {
				return err
			}
			// Application ingress reopens only after durable completion and inspection.
			return nil
		}
	}
	return fmt.Errorf("PostgreSQL recovery primary is unavailable")
}
