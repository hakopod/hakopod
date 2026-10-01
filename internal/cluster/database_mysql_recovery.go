package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mysqlObservedPrimary(o database.Observation) (database.Member, error) {
	for _, m := range o.Members {
		if m.Name == o.Primary && m.Role == "primary" {
			return m, nil
		}
	}
	return database.Member{}, fmt.Errorf("MySQL primary is unavailable")
}

func (c *Client) dumpMySQLDatabase(ctx context.Context, d database.Resource, o database.Observation, out io.Writer) error {
	m, err := mysqlObservedPrimary(o)
	if err != nil {
		return err
	}
	// A global read lock gives one consistent schema/data capture, including
	// routines and nontransactional tables. Writes wait for the bounded dump.
	// This cost is explicit in the backup UI and guide; it is not an online PITR.
	command := []string{"mysqldump", "--no-defaults", "--protocol=SOCKET", "--socket=/var/run/mysqld/mysql.sock", "--user=localroot", "--lock-all-tables", "--set-gtid-purged=OFF", "--no-tablespaces", "--hex-blob", "--routines", "--events", "--triggers", "--skip-add-locks", "--skip-dump-date", "--column-statistics=0", "app"}
	if err = c.DatabaseExec(ctx, d, m, command, nil, out); err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("MySQL topology changed during backup")
	}
	return nil
}

func (c *Client) mysqlDatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	m, err := mysqlObservedPrimary(o)
	if err != nil {
		return err
	}
	query := `SELECT (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='app') + (SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema='app') + (SELECT COUNT(*) FROM information_schema.events WHERE event_schema='app')`
	out := &databaseBoundedWriter{limit: 256}
	if err = c.DatabaseExec(ctx, d, m, mysqlLocalCommand(query), nil, out); err != nil {
		return err
	}
	if strings.TrimSpace(out.String()) != "0" {
		return fmt.Errorf("MySQL recovery requires a separate empty database")
	}
	return nil
}

func (c *Client) RestoreMySQLDatabase(ctx context.Context, d database.Resource, o database.Observation, input io.Reader) error {
	if d.Spec.Engine != "mysql" || !d.Spec.TLSRequired() {
		return fmt.Errorf("MySQL recovery requires verified TLS")
	}
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	m, err := mysqlObservedPrimary(o)
	if err != nil {
		return err
	}
	d.Status = "restoring"
	if err = c.databaseNetworkPolicy(ctx, d, func() error { return nil }); err != nil {
		return err
	}
	// Drop existing application sessions before rechecking emptiness. SQL from
	// the archive then authenticates as app, without any global privileges.
	out := &databaseBoundedWriter{limit: 4096}
	if err = c.DatabaseExec(ctx, d, m, mysqlLocalCommand("SELECT ID FROM information_schema.processlist WHERE USER='app' LIMIT 201"), nil, out); err != nil {
		return err
	}
	ids := strings.Fields(out.String())
	if len(ids) > 200 {
		return fmt.Errorf("MySQL recovery session inventory exceeds its bound")
	}
	for _, id := range ids {
		if _, err = strconv.ParseUint(id, 10, 64); err != nil {
			return fmt.Errorf("MySQL session identity is invalid")
		}
		if err = c.DatabaseExec(ctx, d, m, mysqlLocalCommand("KILL CONNECTION "+id), nil, io.Discard); err != nil {
			return err
		}
	}
	if err = c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || bytes.ContainsAny(secret.Data["password"], "\r\n") {
		return fmt.Errorf("MySQL recovery credentials are unavailable")
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return err
	}
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	script := `set -eu
IFS= read -r MYSQL_PWD; export MYSQL_PWD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
printf '%s' "$2" > "$work/ca.crt"
mysql --no-defaults --protocol=TCP --host="$1" --port=6446 --user=app --database=app --connect-timeout=5 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --binary-mode=1 --local-infile=0 --batch`
	stream := io.MultiReader(bytes.NewReader(append(append([]byte(nil), secret.Data["password"]...), '\n')), input)
	if err = c.DatabaseExec(ctx, d, m, []string{"sh", "-c", script, "restore-mysql", host, trust.CertificatePEM}, stream, io.Discard); err != nil {
		return err
	}
	// Application ingress reopens only after durable completion and inspection.
	return nil
}
