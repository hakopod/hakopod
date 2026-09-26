package api

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

const testApplicationID = "0123456789abcdef0123456789abcdef"

// The runtime must keep satisfying the engine-performed backup contract.
var _ backup.EngineBackup = (*backupRuntime)(nil)

func TestDeclaredBackupSourceRecognizesClickHouse(t *testing.T) {
	application := store.Application{ID: testApplicationID}
	source, ok := declaredBackupSource(application, "main", spec.Service{Image: "docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893"})
	if !ok || source.Engine != "clickhouse" || source.Database != "default" {
		t.Fatalf("blueprint ClickHouse service was not recognized: %+v %v", source, ok)
	}
	source, ok = declaredBackupSource(application, "main", spec.Service{Image: "clickhouse/clickhouse-server", Env: map[string]string{"CLICKHOUSE_DB": "lumen"}})
	if !ok || source.Database != "lumen" {
		t.Fatalf("declared ClickHouse database was not used: %+v", source)
	}
	for _, image := range []string{"clickhouse/clickhouse-keeper", "redis", "docker.io/library/nginx", "clickhouse/clickhouse-server-extra"} {
		if _, ok := declaredBackupSource(application, "main", spec.Service{Image: image}); ok {
			t.Fatalf("image %s must not be accepted as a backup source", image)
		}
	}
}

// The security decision this file exists to protect: every element of the exec
// command becomes a repeated command query parameter on the Kubernetes exec
// request URL, which kube-apiserver audit records verbatim. The statement that
// carries the object-storage credentials therefore travels on standard input.
func TestClickHouseStatementKeepsCredentialsOutOfArgv(t *testing.T) {
	destination := backup.Destination{Endpoint: "https://objects.example.com", Bucket: "backups", Prefix: "hakopod"}
	credentials := backup.Credentials{AccessKeyID: "AKIAEXAMPLEKEYID", SecretAccessKey: "s3cr3t-Value/With+Chars"}
	id, err := clickhouseOperationID("backup", "hakopod/app/job-1")
	if err != nil {
		t.Fatal(err)
	}
	statement := "BACKUP DATABASE " + must(t, "metrics") + " TO S3(" + clickhouseString(clickhouseDestinationURL(destination, "hakopod/app/job-1")) + ", " + clickhouseString(credentials.AccessKeyID) + ", " + clickhouseString(credentials.SecretAccessKey) + ") SETTINGS id = " + clickhouseString(id) + " ASYNC\n"

	// The argv is the shell, the script and the two fixed positional arguments
	// the shared exec helper supplies. Nothing else is ever added.
	argv := []string{"/bin/sh", "-c", clickhouseScript, "hakopod-backup", "metrics"}
	for _, element := range argv {
		for _, secret := range []string{credentials.AccessKeyID, credentials.SecretAccessKey, "s3cr3t"} {
			if strings.Contains(element, secret) {
				t.Fatalf("a credential reached argv, where the Kubernetes exec URL records it: %q", element)
			}
		}
	}
	if strings.Contains(strings.Join(argv, " "), "BACKUP DATABASE") {
		t.Fatal("the statement reached argv; it must travel on standard input")
	}
	if !strings.Contains(statement, credentials.SecretAccessKey) || !strings.Contains(statement, "BACKUP DATABASE `metrics` TO S3('https://objects.example.com/backups/hakopod/app/job-1'") {
		t.Fatalf("the stdin statement is not the expected shape: %s", statement)
	}
	if !strings.Contains(clickhouseScript, `--password "${CLICKHOUSE_PASSWORD`) {
		t.Fatal("the database password must come from the pod environment, not argv")
	}
}

func must(t *testing.T, name string) string {
	t.Helper()
	quoted, err := clickhouseIdentifier(name)
	if err != nil {
		t.Fatal(err)
	}
	return quoted
}

func TestClickHouseEngineStatusMapping(t *testing.T) {
	status, err := clickhouseEngineStatus("CREATING_BACKUP\t0\t0\n", clickhouseBackupRunning, clickhouseBackupDone, clickhouseBackupFailed, "failed")
	if err != nil || status.Done || status.Failed || status.Bytes != 0 {
		t.Fatalf("in-flight backup was misread: %+v %v", status, err)
	}
	status, err = clickhouseEngineStatus("BACKUP_CREATED\t4096\t12\n", clickhouseBackupRunning, clickhouseBackupDone, clickhouseBackupFailed, "failed")
	if err != nil || !status.Done || status.Failed || status.Bytes != 4096 || status.Files != 12 {
		t.Fatalf("successful backup was misread: %+v %v", status, err)
	}
	status, err = clickhouseEngineStatus("BACKUP_FAILED\t0\t0\n", clickhouseBackupRunning, clickhouseBackupDone, clickhouseBackupFailed, "sanitized prose")
	if err != nil || !status.Done || !status.Failed || status.Message != "sanitized prose" {
		t.Fatalf("failed backup was misread: %+v %v", status, err)
	}
	status, err = clickhouseEngineStatus("RESTORED\t8\t1\n", clickhouseRestoreRunning, clickhouseRestoreDone, clickhouseRestoreFailed, "failed")
	if err != nil || !status.Done || status.Failed {
		t.Fatalf("restore completion was misread: %+v %v", status, err)
	}
	for _, output := range []string{"", "\n", "BACKUP_CREATED\n", "BACKUP_CREATED\t4096\n", "BACKUP CREATED\t0\t0\n", "BACKUP_CREATED\tlots\t1\n", "SOMETHING_ELSE\t0\t0\n", "BACKUP_CREATED\t1\t1\nBACKUP_FAILED\t0\t0\n"} {
		status, err := clickhouseEngineStatus(output, clickhouseBackupRunning, clickhouseBackupDone, clickhouseBackupFailed, "failed")
		if err == nil {
			t.Fatalf("unparseable response %q was accepted as %+v", output, status)
		}
		if status.Done || status.Failed {
			t.Fatalf("unparseable response %q produced a terminal status", output)
		}
	}
}

func TestClickHouseReferenceAndQuoting(t *testing.T) {
	id, err := clickhouseOperationID("restore", "hakopod/app/job 1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "hakopod-restore-hakopod-app-job-1" {
		t.Fatalf("operation id is not deterministic and correlatable: %s", id)
	}
	if err := clickhouseReference(id); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "x", "hakopod-backup-'; DROP", "hakopod-backup-a b", strings.Repeat("hakopod-", 40)} {
		if err := clickhouseReference(ref); err == nil {
			t.Fatalf("reference %q must be refused", ref)
		}
	}
	if _, err := clickhouseOperationID("backup", ""); err == nil {
		t.Fatal("an empty prefix must be refused")
	}
	if clickhouseString(`a'b\c`) != `'a\'b\\c'` {
		t.Fatalf("string quoting does not escape: %s", clickhouseString(`a'b\c`))
	}
	for _, name := range []string{"", "a b", "a'b", "a;b", strings.Repeat("a", 64)} {
		if _, err := clickhouseIdentifier(name); err == nil {
			t.Fatalf("database name %q must be refused", name)
		}
	}
}

func TestBoundedWriterRefusesOversizedResponse(t *testing.T) {
	out := &boundedWriter{limit: 8}
	if _, err := out.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("9")); err == nil {
		t.Fatal("the bounded writer accepted more than its limit")
	}
}
