package cluster

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
)

var nativeProvisionIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{2,47}$`)

// ProvisionPostgresApplication creates and verifies only the reviewed role and
// logical database. Password material travels on stdin and is never included in
// a command, durable observation, or returned error.
func (c *Client) ProvisionPostgresApplication(ctx context.Context, d database.Resource, plan database.ApplicationProvisioningPlan, password []byte, phase string, before func() error) (bool, error) {
	if d.Spec.Engine != "postgresql" || d.ID != plan.DatabaseID || d.Revision != plan.DatabaseRevision || !nativeProvisionIdentifier.MatchString(plan.Role) || !nativeProvisionIdentifier.MatchString(plan.LogicalDatabase) || len(password) != 64 {
		return false, fmt.Errorf("invalid PostgreSQL application provisioning contract")
	}
	decodedPassword, decodeErr := hex.DecodeString(string(password))
	if decodeErr != nil || len(decodedPassword) != 32 {
		return false, fmt.Errorf("generated PostgreSQL password is invalid")
	}
	if phase != "accepted" && phase != "verifying" {
		return false, fmt.Errorf("unsupported PostgreSQL application provisioning phase")
	}
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	if len(d.Observation.Members) == 0 {
		return false, fmt.Errorf("PostgreSQL primary observation is unavailable")
	}
	member := d.Observation.Members[0]
	for _, candidate := range d.Observation.Members {
		if candidate.Role == "primary" {
			member = candidate
		}
	}
	marker := "hakopod:" + plan.ID
	// The generated password is lowercase hexadecimal, so it cannot terminate
	// the SQL literal. Identifiers have already passed the strict allow-list.
	sql := fmt.Sprintf(`\set ON_ERROR_STOP on
DO $block$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='%[1]s') THEN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='%[1]s' AND COALESCE(shobj_description(oid,'pg_authid'),'') <> '%[4]s') THEN RAISE EXCEPTION 'role ownership conflict'; END IF;
 ELSE
  CREATE ROLE %[1]s LOGIN PASSWORD '%[3]s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  COMMENT ON ROLE %[1]s IS '%[4]s';
 END IF;
 IF EXISTS(SELECT 1 FROM pg_database d WHERE d.datname='%[2]s' AND COALESCE(shobj_description(d.oid,'pg_database'),'') <> '%[4]s' AND NOT (shobj_description(d.oid,'pg_database') IS NULL AND EXISTS(SELECT 1 FROM pg_roles r WHERE r.oid=d.datdba AND r.rolname='%[1]s' AND shobj_description(r.oid,'pg_authid')='%[4]s'))) THEN RAISE EXCEPTION 'database ownership conflict'; END IF;
END $block$;
SELECT format('CREATE DATABASE %%I OWNER %%I', '%[2]s', '%[1]s') WHERE NOT EXISTS(SELECT 1 FROM pg_database WHERE datname='%[2]s') \gexec
COMMENT ON DATABASE %[2]s IS '%[4]s';
REVOKE ALL ON DATABASE %[2]s FROM PUBLIC;
GRANT CONNECT,TEMPORARY ON DATABASE %[2]s TO %[1]s;
`, plan.Role, plan.LogicalDatabase, string(password), marker)
	if phase == "accepted" {
		if before != nil {
			if err := before(); err != nil {
				return false, err
			}
		}
		if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres"}, strings.NewReader(sql), io.Discard); err != nil {
			return false, fmt.Errorf("PostgreSQL role and database provisioning failed")
		}
		return false, nil
	}
	ownership := fmt.Sprintf(`SELECT count(*) FROM pg_roles r,pg_database d WHERE r.rolname='%[1]s' AND d.datname='%[2]s' AND shobj_description(r.oid,'pg_authid')='%[3]s' AND shobj_description(d.oid,'pg_database')='%[3]s' AND d.datdba=r.oid AND NOT r.rolsuper AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)`, plan.Role, plan.LogicalDatabase, marker)
	owned := &databaseBoundedWriter{limit: 128}
	if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", ownership}, nil, owned); err != nil || strings.TrimSpace(owned.String()) != "1" {
		return false, fmt.Errorf("PostgreSQL provisioning ownership evidence changed")
	}
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	probe := "hp_probe_" + plan.ID[:12]
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return false, fmt.Errorf("PostgreSQL trust material is unavailable")
	}
	verify := `set -eu
IFS= read -r PGPASSWORD
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
export PGPASSWORD PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt"
psql -Xq -v ON_ERROR_STOP=1 -v probe="$3" -h database-rw -U "$1" -d "$2" <<'SQL'
BEGIN;
CREATE TABLE public.:"probe"(value integer);
INSERT INTO public.:"probe"(value) VALUES(1);
SELECT value FROM public.:"probe" LIMIT 1;
ROLLBACK;
SQL
if psql -Xq -v ON_ERROR_STOP=1 -h database-rw -U "$1" -d "$2" -c "BEGIN; CREATE ROLE $3; ROLLBACK" >/dev/null 2>&1; then
  exit 41
fi
if psql -Xq -v ON_ERROR_STOP=1 -h database-rw -U "$1" -d postgres -c "BEGIN; CREATE TABLE public.$3(value integer); ROLLBACK" >/dev/null 2>&1; then
  exit 42
fi
`
	input := append(append([]byte(nil), password...), '\n')
	input = append(input, trust.CertificatePEM...)
	if err := c.DatabaseExec(ctx, d, member, []string{"sh", "-c", verify, "verify-provisioned-postgres", plan.Role, plan.LogicalDatabase, probe}, bytes.NewReader(input), io.Discard); err != nil {
		return false, fmt.Errorf("PostgreSQL application privileges could not be verified")
	}
	return true, nil
}
