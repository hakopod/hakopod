package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func oracleRecoveryName() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "HP_" + strings.ToUpper(hex.EncodeToString(random)), nil
}

func (c *Client) oracleDatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	m, err := oracleObservedPrimary(d, o)
	if err != nil {
		return err
	}
	query := `SELECT COUNT(*) FROM cdb_objects WHERE owner='APP' AND oracle_maintained='N'`
	value, err := c.oracleLocalQuery(ctx, d, m, query)
	if err != nil {
		return err
	}
	if value != "0" {
		return fmt.Errorf("Oracle recovery requires a separate empty schema")
	}
	return nil
}

func oraclePumpSQL(name, file, operation string) string {
	// Account policy belongs to target provisioning. Oracle's GRANT path only
	// covers object grants; roles, system privileges and quotas are separate.
	setup := ""
	finish := ""
	if operation == "EXPORT" {
		setup = "DBMS_DATAPUMP.SET_PARAMETER(h,'FLASHBACK_SCN',capture_scn);"
		finish = "DBMS_OUTPUT.PUT_LINE('HAKOPOD_SCN='||TO_CHAR(capture_scn,'FM99999999999999999999'));"
	}
	guard := ""
	release := ""
	if operation == "EXPORT" {
		// The startup DDL trigger holds the matching shared lock until each APP
		// DDL commits. Take the exclusive lock before choosing the snapshot SCN.
		guard = `lock_result:=DBMS_LOCK.REQUEST(id=>82635001,lockmode=>DBMS_LOCK.X_MODE,timeout=>10,release_on_commit=>FALSE);
 IF lock_result NOT IN (0,4) THEN RAISE_APPLICATION_ERROR(-20011,'Schema capture is busy'); END IF;
 capture_scn:=DBMS_FLASHBACK.GET_SYSTEM_CHANGE_NUMBER;`
		release = "IF capture_scn IS NOT NULL THEN lock_result:=DBMS_LOCK.RELEASE(82635001); END IF;"
	}
	return fmt.Sprintf(`SET SERVEROUTPUT ON SIZE 32767
DECLARE h NUMBER; state VARCHAR2(30); status KU$_STATUS; capture_scn NUMBER; lock_result NUMBER; error_index PLS_INTEGER; error_count PLS_INTEGER; code_index PLS_INTEGER; error_code VARCHAR2(10); error_number NUMBER;
BEGIN
 %s
 h:=DBMS_DATAPUMP.OPEN(operation=>'%s',job_mode=>'SCHEMA',job_name=>'%s');
 DBMS_DATAPUMP.ADD_FILE(h,'%s','HAKOPOD_BACKUP',filetype=>DBMS_DATAPUMP.KU$_FILE_TYPE_DUMP_FILE);
 DBMS_DATAPUMP.METADATA_FILTER(h,'SCHEMA_EXPR','IN (''APP'')');
 DBMS_DATAPUMP.METADATA_FILTER(h,'EXCLUDE_PATH_EXPR','IN (''USER'',''GRANT'',''SYSTEM_GRANT'',''ROLE_GRANT'',''DEFAULT_ROLE'',''TABLESPACE_QUOTA'',''STATISTICS'')');
 %s
 DBMS_DATAPUMP.START_JOB(h);
	 LOOP
	  DBMS_DATAPUMP.GET_STATUS(h,DBMS_DATAPUMP.KU$_STATUS_JOB_ERROR+DBMS_DATAPUMP.KU$_STATUS_JOB_STATUS,10,state,status);
	  IF BITAND(status.mask,DBMS_DATAPUMP.KU$_STATUS_JOB_ERROR)<>0 THEN
	   error_index:=status.error.FIRST; error_count:=0;
	   WHILE error_index IS NOT NULL AND error_count<8 LOOP
	    DBMS_OUTPUT.PUT_LINE('ORA-'||TO_CHAR(ABS(status.error(error_index).errorNumber),'FM00000'));
	    error_count:=error_count+1; code_index:=1;
	    WHILE error_count<8 AND code_index<=8 LOOP
	     error_code:=REGEXP_SUBSTR(status.error(error_index).LogText,'ORA-[0-9]{5}',1,code_index,'c');
	     EXIT WHEN error_code IS NULL;
	     IF error_code<>'ORA-'||TO_CHAR(ABS(status.error(error_index).errorNumber),'FM00000') THEN
	      DBMS_OUTPUT.PUT_LINE(error_code); error_count:=error_count+1;
	     END IF;
	     code_index:=code_index+1;
	    END LOOP;
	    error_index:=status.error.NEXT(error_index);
	   END LOOP;
	   RAISE_APPLICATION_ERROR(-20001,'Data Pump reported an error');
	  END IF;
	  EXIT WHEN state='COMPLETED';
	  IF state='STOPPED' THEN RAISE_APPLICATION_ERROR(-20002,'Data Pump stopped before completion'); END IF;
	 END LOOP;
	 DBMS_DATAPUMP.DETACH(h);
	 %s
	 %s
EXCEPTION WHEN OTHERS THEN
	 -- Data Pump cleanup may clear Oracle's error stack. Save only its code.
	 error_number:=SQLCODE;
	 DBMS_OUTPUT.PUT_LINE('ORA-'||TO_CHAR(ABS(error_number),'FM00000'));
	 IF h IS NOT NULL THEN BEGIN DBMS_DATAPUMP.STOP_JOB(h,immediate=>1,keep_master=>0); EXCEPTION WHEN OTHERS THEN NULL; END; END IF;
	 %s
	 RAISE_APPLICATION_ERROR(-20012,'Schema transfer failed');
END;
/`, guard, operation, name, file, setup, finish, release, release)
}

func (c *Client) oracleRecoveryCleanup(d database.Resource, m database.Member, name, file string) error {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	// A canceled client does not cancel its server-side Data Pump job.
	query := fmt.Sprintf(`ALTER SESSION SET CONTAINER=%s;
DECLARE h NUMBER;
BEGIN
	 FOR job IN (SELECT owner_name FROM dba_datapump_jobs WHERE job_name='%s' AND owner_name IN ('SYS','APP')) LOOP
	  h:=DBMS_DATAPUMP.ATTACH('%s',job.owner_name);
	  DBMS_DATAPUMP.STOP_JOB(h,immediate=>1,keep_master=>0);
	 END LOOP;
END;
/`, oraclePDB(d), name, name)
	if _, err := c.oracleLocalQuery(ctx, d, m, query); err != nil {
		return fmt.Errorf("Oracle recovery job cleanup could not be verified")
	}
	if err := c.DatabaseExec(ctx, d, m, []string{"rm", "-f", "--", "/opt/oracle/hakopod-backup/" + file}, nil, io.Discard); err != nil {
		return fmt.Errorf("Oracle recovery staging cleanup could not be verified")
	}
	return nil
}

func (c *Client) dumpOracleDatabase(ctx context.Context, d database.Resource, o database.Observation, out io.Writer) (result error) {
	m, err := oracleObservedPrimary(d, o)
	if err != nil {
		return err
	}
	name, err := oracleRecoveryName()
	if err != nil {
		return err
	}
	file := name + ".dmp"
	defer func() { result = errors.Join(result, c.oracleRecoveryCleanup(d, m, name, file)) }()
	if _, err = c.oracleLocalQuery(ctx, d, m, `ALTER SESSION SET CONTAINER=`+oraclePDB(d)+`;
CREATE OR REPLACE DIRECTORY HAKOPOD_BACKUP AS '/opt/oracle/hakopod-backup'`); err != nil {
		return err
	}
	value, err := c.oracleLocalQuery(ctx, d, m, "ALTER SESSION SET CONTAINER="+oraclePDB(d)+";\n"+oraclePumpSQL(name, file, "EXPORT"))
	if err != nil {
		return fmt.Errorf("Oracle schema capture did not complete: %w", err)
	}
	value = strings.TrimPrefix(value, "HAKOPOD_SCN=")
	scn, err := strconv.ParseUint(value, 10, 64)
	if err != nil || scn == 0 {
		return fmt.Errorf("Oracle recovery point is unavailable")
	}
	sizeOut := &databaseBoundedWriter{limit: 64}
	if err = c.DatabaseExec(ctx, d, m, []string{"stat", "-c", "%s", "/opt/oracle/hakopod-backup/" + file}, nil, sizeOut); err != nil {
		return err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOut.String()), 10, 64)
	if err != nil || size > d.Spec.StorageGiB<<30 {
		return fmt.Errorf("Oracle backup exceeded its staging capacity")
	}
	a := database.OracleArchive{SchemaVersion: 1, DatabaseID: d.ID, Revision: d.Revision, Version: d.Spec.Version, Edition: d.Spec.Oracle.Edition, SCN: scn, Bytes: size}
	if err = database.WriteOracleArchive(out, a, func(w io.Writer) error {
		return c.DatabaseExec(ctx, d, m, []string{"cat", "/opt/oracle/hakopod-backup/" + file}, nil, w)
	}); err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("Oracle instance changed during capture")
	}
	return nil
}

func (c *Client) RestoreOracleDatabase(ctx context.Context, d database.Resource, o database.Observation, in io.Reader) (result error) {
	if d.Spec.Engine != "oracle" || d.Spec.Oracle == nil || !d.Spec.TLSRequired() {
		return fmt.Errorf("Oracle recovery requires a matching managed target with verified TCPS")
	}
	if oracleEnterprise(d.Spec) {
		if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
			return err
		}
	} else if err := oracleRuntimeSupported(d.Spec); err != nil {
		return err
	}
	m, err := oracleObservedPrimary(d, o)
	if err != nil {
		return err
	}
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	name, err := oracleRecoveryName()
	if err != nil {
		return err
	}
	file := name + ".dmp"
	defer func() { result = errors.Join(result, c.oracleRecoveryCleanup(d, m, name, file)) }()
	_, err = database.ReadOracleArchive(in, func(a database.OracleArchive, r io.Reader) error {
		if a.DatabaseID == d.ID || a.Version != d.Spec.Version || a.Edition != d.Spec.Oracle.Edition || a.Bytes > d.Spec.StorageGiB<<30 {
			return fmt.Errorf("Oracle recovery requires a separate matching edition and version with enough staging capacity")
		}
		return c.DatabaseExec(ctx, d, m, []string{"bash", "-c", `set -eu; umask 077; set -o noclobber; cat > "$1"`, "stage-oracle", "/opt/oracle/hakopod-backup/" + file}, r, io.Discard)
	})
	if err != nil {
		return err
	}
	d.Status = "restoring"
	if err = c.databaseNetworkPolicy(ctx, d, func() error { return ctx.Err() }); err != nil {
		return err
	}
	if oracleEnterprise(d.Spec) {
		if err = c.reconcileOracleEnterpriseRoute(ctx, d, nil, func() error { return ctx.Err() }); err != nil {
			return err
		}
	}
	pod, _, err := c.databaseExecTarget(ctx, d, m)
	if err != nil {
		return err
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		return err
	}
	step, stop := context.WithTimeout(ctx, 8*time.Minute)
	defer stop()
	var fresh database.Observation
	for step.Err() == nil {
		if oracleEnterprise(d.Spec) {
			fresh, err = c.observeOracleEnterpriseCore(step, d)
			if err == nil {
				err = c.verifyOracleEnterpriseTLS(step, d, &fresh)
			}
			if err == nil {
				fresh.Status = "ready"
			}
		} else {
			fresh, err = c.ObserveDatabase(step, d)
		}
		if err == nil && fresh.Status == "ready" {
			replacement, primaryErr := oracleObservedPrimary(d, fresh)
			if primaryErr == nil && replacement.UID != m.UID {
				// Only the accepted primary may restart during restore. A role
				// change could point at another member's unrelated staging PVC.
				if oracleEnterprise(d.Spec) {
					for i, previous := range o.Members {
						if previous.Name == m.Name && fresh.Members[i].Name != replacement.Name {
							return fmt.Errorf("Oracle primary changed during recovery isolation")
						}
						if previous.Name != m.Name && fresh.Members[i].UID != previous.UID {
							return fmt.Errorf("Oracle topology changed during recovery isolation")
						}
					}
				}
				m = replacement
				break
			}
		}
		if sleepContext(step, 3*time.Second) != nil {
			return fmt.Errorf("Oracle recovery could not revoke existing sessions")
		}
	}
	if step.Err() != nil {
		return fmt.Errorf("Oracle recovery could not revoke existing sessions")
	}
	if err = c.DatabaseEmpty(ctx, d, fresh); err != nil {
		return err
	}
	// Import executes with only the application's own schema privileges. The
	// temporary directory grant exists only while this target has closed ingress.
	setup := `ALTER SESSION SET CONTAINER=` + oraclePDB(d) + `;
CREATE OR REPLACE DIRECTORY HAKOPOD_BACKUP AS '/opt/oracle/hakopod-backup';
GRANT READ, WRITE ON DIRECTORY HAKOPOD_BACKUP TO APP`
	if _, err = c.oracleLocalQuery(ctx, d, m, setup); err != nil {
		return err
	}
	directoryGranted := true
	revokeDirectory := func(cleanup context.Context) error {
		if _, e := c.oracleLocalQuery(cleanup, d, m, "ALTER SESSION SET CONTAINER="+oraclePDB(d)+";\nREVOKE READ, WRITE ON DIRECTORY HAKOPOD_BACKUP FROM APP"); e != nil {
			return fmt.Errorf("Oracle recovery directory access cleanup could not be verified")
		}
		directoryGranted = false
		return nil
	}
	defer func() {
		if !directoryGranted {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result = errors.Join(result, revokeDirectory(cleanup))
	}()
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID {
		return fmt.Errorf("Oracle recovery credentials are unavailable")
	}
	password := string(secret.Data["password"])
	if len(password) != 64 {
		return fmt.Errorf("Oracle recovery credential is invalid")
	}
	if _, err = hex.DecodeString(password); err != nil {
		return fmt.Errorf("Oracle recovery credential is invalid")
	}
	script := `set -euo pipefail
IFS= read -r password
{ printf 'whenever sqlerror exit failure\nwhenever oserror exit failure\nset echo off verify off feedback off\n';
  printf 'connect app/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=IPC)(KEY=HAKOPOD))(CONNECT_DATA=(SERVICE_NAME=` + oraclePDB(d) + `)))"\n' "$password";
  cat;
} | sqlplus -s /nolog 2>&1 | awk '{ while (match($0,/(ORA|PLS|SP2)-[0-9][0-9][0-9][0-9][0-9]?|HAKOPOD_OBJECT=[A-Z_]+/)) { code=substr($0,RSTART,RLENGTH); if (!seen[code]++ && count++<8) print code; $0=substr($0,RSTART+RLENGTH) } }'`
	input := password + "\n" + oraclePumpSQL(name, file, "IMPORT") + "\nexit\n"
	output := &databaseBoundedWriter{limit: 512}
	if err = c.DatabaseExec(ctx, d, m, []string{"bash", "-c", script}, strings.NewReader(input), output); err != nil {
		return fmt.Errorf("Oracle schema import did not complete%s", oracleErrorSuffix(output.String()))
	}
	if err = revokeDirectory(ctx); err != nil {
		return err
	}
	if oracleEnterprise(d.Spec) {
		if err = c.waitOracleRecoveryReplay(ctx, d, fresh); err != nil {
			return err
		}
		after, err := c.observeOracleEnterpriseCore(ctx, d)
		if err != nil || after.TopologyFingerprint != fresh.TopologyFingerprint {
			return fmt.Errorf("Oracle topology or replication changed during recovery")
		}
		if err = c.verifyOracleEnterpriseTLS(ctx, d, &after); err != nil {
			return err
		}
	}
	return nil
}

// A healthy redo transport can still be ahead of standby apply. Recovery waits
// for each mounted standby to replay the import and its privilege cleanup.
func (c *Client) waitOracleRecoveryReplay(ctx context.Context, d database.Resource, expected database.Observation) error {
	if d.Spec.Mode != "cluster" {
		return nil
	}
	primary, err := oracleObservedPrimary(d, expected)
	if err != nil {
		return err
	}
	value, err := c.oracleLocalQuery(ctx, d, primary, "SELECT TO_CHAR(current_scn,'FM99999999999999999999') FROM v$database")
	scn, parseErr := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parseErr != nil || scn == 0 {
		return fmt.Errorf("Oracle recovery replay position is unavailable")
	}
	if _, err = c.oracleLocalQuery(ctx, d, primary, "ALTER SYSTEM ARCHIVE LOG CURRENT"); err != nil {
		return fmt.Errorf("Oracle recovery redo could not be finalized")
	}
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		current, err := c.observeOracleEnterpriseCore(wait, d)
		if err != nil {
			return fmt.Errorf("Oracle recovery replication could not be verified")
		}
		if current.TopologyFingerprint != expected.TopologyFingerprint || current.Primary != expected.Primary {
			return fmt.Errorf("Oracle topology changed while waiting for recovery replay")
		}
		complete := true
		for _, member := range current.Members {
			if member.Role != "replica" {
				continue
			}
			value, queryErr := c.oracleLocalQuery(wait, d, member, "SELECT TO_CHAR(current_scn,'FM99999999999999999999') FROM v$database")
			applied, parseErr := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if queryErr != nil || parseErr != nil || applied == 0 {
				return fmt.Errorf("Oracle standby recovery replay position is unavailable")
			}
			complete = complete && applied >= scn
		}
		if complete {
			return nil
		}
		if sleepContext(wait, 2*time.Second) != nil {
			break
		}
	}
	return fmt.Errorf("Oracle standbys have not applied the recovered schema")
}

func oracleObservedPrimary(d database.Resource, o database.Observation) (database.Member, error) {
	var primary database.Member
	if d.Spec.Engine != "oracle" || len(o.Members) != d.Spec.Members() || o.Primary == "" {
		return primary, fmt.Errorf("Oracle operation requires the complete observed topology")
	}
	for _, member := range o.Members {
		if !member.Ready || member.UID == "" {
			return database.Member{}, fmt.Errorf("Oracle operation requires every member to be ready")
		}
		if member.Role == "primary" {
			if primary.Name != "" || member.Name != o.Primary {
				return database.Member{}, fmt.Errorf("Oracle operation has no unique observed primary")
			}
			primary = member
		}
	}
	if primary.Name == "" {
		return primary, fmt.Errorf("Oracle operation has no observed primary")
	}
	return primary, nil
}
