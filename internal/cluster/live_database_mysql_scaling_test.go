package cluster

import (
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedMySQLQuorumLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1")
	}
	c, ctx := liveRecoveryClient(t, 12*time.Minute)
	d, password := newMySQLFixture(t, ctx, c, "cluster")
	d.Spec.CPU = "500m"
	health := waitMySQLFixture(t, ctx, c, d, password)
	if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "CREATE TABLE IF NOT EXISTS scale_check(id INT PRIMARY KEY,value VARBINARY(16)); INSERT IGNORE INTO scale_check VALUES(1,0x000AFF80); DELETE FROM scale_check WHERE id=99"); err != nil {
		t.Fatal(err)
	}
	testMySQLQuorumLoss(t, ctx, c, d, health)
	health = waitMySQLFixture(t, ctx, c, d, password)
	if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM scale_check WHERE id=1"); err != nil || got != "000AFF80" {
		t.Fatal("MySQL quorum restoration lost previously committed data", err)
	}
	testMySQLCredentialLogs(t, ctx, c, d)
}

func TestManagedMySQLScalingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1")
	}
	c, ctx := liveRecoveryClient(t, 30*time.Minute)
	d, password := newMySQLFixture(t, ctx, c, "cluster")
	d.Spec.CPU = "500m"
	health := waitMySQLFixture(t, ctx, c, d, password)
	if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "CREATE TABLE IF NOT EXISTS scale_check(id INT PRIMARY KEY,value VARBINARY(16)); INSERT IGNORE INTO scale_check VALUES(1,0x000AFF80); DELETE FROM scale_check WHERE id=99"); err != nil {
		t.Fatal(err)
	}
	for _, replicas := range []int{4, 6, 2} {
		d.Spec.Replicas = replicas
		d.Revision++
		health = waitMySQLFixture(t, ctx, c, d, password)
		if len(health.Members) != replicas+1 || health.Routing == nil || len(health.Routing.Members) != 2 {
			t.Fatal("MySQL resize did not converge to the complete voting and routing inventory")
		}
		if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM scale_check WHERE id=1"); err != nil || got != "000AFF80" {
			t.Fatal("MySQL resize changed application data", err)
		}
		if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_only", "INSERT INTO scale_check VALUES(2,0x01)"); err == nil {
			t.Fatal("MySQL replica endpoint accepted writes after resize")
		}
		t.Log("Verified MySQL voting member count", replicas+1)
	}
	router := health.Routing.Members[0]
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, router.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != router.UID {
		t.Fatal("MySQL Router identity changed before replacement")
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0)), Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		t.Fatal(err)
	}
	health = waitMySQLFixture(t, ctx, c, d, password)
	for _, member := range health.Routing.Members {
		if member.UID == router.UID {
			t.Fatal("MySQL observation retained the replaced Router")
		}
	}
	if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM scale_check WHERE id=1"); err != nil || got != "000AFF80" {
		t.Fatal("MySQL Router replacement lost access to data", err)
	}
	testMySQLCertificateRefusal(t, ctx, c, d, health, password)
	testMySQLQuorumLoss(t, ctx, c, d, health)
	health = waitMySQLFixture(t, ctx, c, d, password)
	if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM scale_check WHERE id=1"); err != nil || got != "000AFF80" {
		t.Fatal("MySQL quorum restoration lost previously committed data", err)
	}
	testMySQLCredentialLogs(t, ctx, c, d)
	t.Log("MySQL 3-to-5-to-7-to-3 member changes and Router replacement preserved data and explicit read-only routing")
}
