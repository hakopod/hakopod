package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This disposable fixture tests driver outcomes after stopping only its own
// Group Replication member. It does not qualify managed database lifecycle.
func TestMySQLRollbackPersistenceControlLive(t *testing.T) {
	if os.Getenv("HAKOPOD_MYSQL_ROLLBACK_CONTROL_TEST") != "1" {
		t.Skip("set HAKOPOD_MYSQL_ROLLBACK_CONTROL_TEST=1 for owned development rollback control")
	}
	if os.Getenv("HAKOPOD_MYSQL_FIXTURE_ID") != "" || os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") != "" {
		t.Fatal("rollback control requires a fresh disposable fixture with cleanup")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("rollback control requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal("development client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d, password := newMySQLFixture(t, ctx, c, "standalone")
	d.Spec.CPU = "500m"
	waitMySQLFixture(t, ctx, c, d, password)
	observation, err := c.ObserveDatabase(ctx, d)
	if err != nil {
		t.Fatal("control observation")
	}
	var primary database.Member
	for _, member := range observation.Members {
		if member.Name == observation.Primary {
			primary = member
		}
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" || primary.UID == "" {
		t.Fatal("control namespace ownership")
	}
	// Local administrative SQL is restricted to this newly created fixture.
	step, stop := context.WithTimeout(ctx, 20*time.Second)
	err = c.DatabaseExec(step, d, primary, mysqlLocalCommand(""), strings.NewReader("STOP GROUP_REPLICATION; SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF;"), io.Discard)
	stop()
	if err != nil {
		t.Fatal("owned control Group Replication stop unavailable")
	}
	native, err := c.mysqlQueryClient(ctx, d, primary)
	if err != nil {
		t.Fatal("control query connection")
	}
	defer native.Close()
	step, stop = context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	conn, err := native.Conn(step)
	if err != nil {
		t.Fatal("control dedicated connection")
	}
	defer conn.Close()
	var groupState bytes.Buffer
	if c.DatabaseExec(step, d, primary, mysqlLocalCommand("SELECT COUNT(*) FROM performance_schema.replication_group_members WHERE MEMBER_STATE='ONLINE'"), nil, &mysqlControlCountWriter{buffer: &groupState}) != nil || strings.TrimSpace(groupState.String()) != "0" {
		t.Fatal("control Group Replication is still active or unverified")
	}
	if _, err = conn.ExecContext(step, "CREATE TABLE app.hakopod_rollback_control(value INT PRIMARY KEY) ENGINE=MyISAM"); err != nil {
		t.Fatal("control MyISAM creation")
	}
	var engine string
	if conn.QueryRowContext(step, "SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA='app' AND TABLE_NAME='hakopod_rollback_control'").Scan(&engine) != nil || engine != "MyISAM" {
		t.Fatal("control actual engine")
	}
	tx, err := conn.BeginTx(step, nil)
	if err != nil {
		t.Fatal("control transaction")
	}
	if _, err = tx.ExecContext(step, "INSERT INTO app.hakopod_rollback_control VALUES(1)"); err != nil {
		tx.Rollback()
		t.Fatal("control nontransactional positive write")
	}
	if outcome := sqlDriverRollbackOutcome(step, tx, "mysql"); outcome != "unknown" {
		t.Fatal("persisted nontransactional write claimed rollback")
	}
	var count int
	if conn.QueryRowContext(step, "SELECT COUNT(*) FROM app.hakopod_rollback_control").Scan(&count) != nil || count != 1 {
		t.Fatal("control persisted row positive proof")
	}
	if _, err = conn.ExecContext(step, "CREATE TABLE app.hakopod_innodb_rollback_control(value INT PRIMARY KEY) ENGINE=InnoDB"); err != nil {
		t.Fatal("control InnoDB creation")
	}
	tx, err = conn.BeginTx(step, nil)
	if err != nil {
		t.Fatal("control InnoDB transaction")
	}
	if _, err = tx.ExecContext(step, "INSERT INTO app.hakopod_innodb_rollback_control VALUES(1)"); err != nil {
		tx.Rollback()
		t.Fatal("control InnoDB positive write")
	}
	if sqlDriverRollbackOutcome(step, tx, "mysql") != "rolled_back" {
		t.Fatal("control InnoDB rollback was not confirmed")
	}
	if conn.QueryRowContext(step, "SELECT COUNT(*) FROM app.hakopod_innodb_rollback_control").Scan(&count) != nil || count != 0 {
		t.Fatal("control InnoDB write persisted")
	}
	t.Log("MySQL driver-only control preserved the MyISAM write and reported unknown rollback")
}

// The administrator diagnostic emits only one decimal count.
type mysqlControlCountWriter struct{ buffer *bytes.Buffer }

func (w *mysqlControlCountWriter) Write(p []byte) (int, error) {
	if w.buffer.Len()+len(p) > 32 {
		return 0, errors.New("control count exceeds 32 bytes")
	}
	return w.buffer.Write(p)
}
