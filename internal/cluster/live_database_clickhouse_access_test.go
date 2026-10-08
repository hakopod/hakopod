package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Exercise the managed controller, verified HTTPS transport and access persistence.
func TestManagedClickHouseTenantAccessLive(t *testing.T) {
	c, ctx := liveClickHouseClient(t)
	d, o := newClickHouseFixture(t, ctx, c, "standalone", 1)
	query := func(sql string, allowed bool) string {
		t.Helper()
		out, err := c.clickhouseQuery(ctx, d, o.Members[0], "app", sql)
		if (err == nil) != allowed {
			t.Fatalf("application access expectation failed: allowed=%v error=%v", allowed, err)
		}
		if !allowed && !clickhouseTenantAccessDenied(err) {
			t.Fatalf("expected an engine access denial, got: %v", err)
		}
		return strings.TrimSpace(out)
	}
	query("CREATE USER tenant_reader", false)
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Spec.ClickHouse = &database.ClickHouseConfig{AccessProfile: "tenant_admin"}
	d.Revision++
	o = waitClickHouseFixture(t, ctx, c, d, secret.Data["password"])
	if o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected {
		t.Fatal("managed TLS enforcement is unverified")
	}
	passwordBytes := make([]byte, 32)
	if _, err = rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(passwordBytes)
	digest := sha256.Sum256([]byte(password))
	for _, sql := range []string{
		"CREATE TABLE app.tenant_events (tenant String,n UInt64) ENGINE=MergeTree ORDER BY n",
		"INSERT INTO app.tenant_events VALUES ('a',1),('b',2)",
		fmt.Sprintf("CREATE USER tenant_reader IDENTIFIED WITH sha256_hash BY '%x' SETTINGS readonly=1", digest),
		"GRANT SELECT ON app.tenant_events TO tenant_reader",
		"CREATE ROW POLICY tenant_filter ON app.tenant_events USING tenant='a' TO tenant_reader",
		"CREATE QUOTA tenant_quota FOR INTERVAL 1 hour MAX queries=1000 TO tenant_reader",
	} {
		query(sql, true)
	}
	reader := func(sql string, allowed bool) string {
		t.Helper()
		out, err := clickhouseTenantHTTPS(ctx, c, d, o.Members[0], "tenant_reader", password, sql)
		if (err == nil) != allowed {
			t.Fatalf("tenant access expectation failed: allowed=%v error=%v", allowed, err)
		}
		if !allowed && !clickhouseTenantAccessDenied(err) {
			t.Fatalf("expected a tenant access denial, got: %v", err)
		}
		return strings.TrimSpace(out)
	}
	if got := reader("SELECT sum(n) FROM app.tenant_events", true); got != "1" {
		t.Fatalf("row isolation failed: %s", got)
	}
	for _, sql := range []string{"INSERT INTO app.tenant_events VALUES ('a',3)", "CREATE USER escaped", "SELECT * FROM system.users", "GRANT SELECT ON app.* TO app"} {
		reader(sql, false)
	}
	for _, sql := range []string{"GRANT ALL ON *.* TO tenant_reader", "GRANT CREATE USER ON *.* TO tenant_reader", "ALTER USER `hakopod-bootstrap` IDENTIFIED WITH no_password", "CREATE DATABASE outside", "SELECT * FROM file('/etc/passwd','RawBLOB')"} {
		query(sql, false)
	}
	// Restart the managed data pod and verify SQL identities and policies persist.
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, o.Members[0].Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uid := pod.UID
	if err = c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		replacement, e := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, pod.Name, metav1.GetOptions{})
		if e == nil && replacement.UID != uid {
			break
		}
		if sleepContext(ctx, time.Second) != nil {
			t.Fatal(ctx.Err())
		}
	}
	o = waitClickHouseFixture(t, ctx, c, d, secret.Data["password"])
	if got := reader("SELECT sum(n) FROM app.tenant_events", true); got != "1" {
		t.Fatalf("row policy did not persist: %s", got)
	}
	query("ALTER ROW POLICY tenant_filter ON app.tenant_events USING tenant='b' TO tenant_reader", true)
	if got := reader("SELECT sum(n) FROM app.tenant_events", true); got != "2" {
		t.Fatalf("row policy update failed: %s", got)
	}
	query("DROP USER tenant_reader", true)
	query("DROP ROW POLICY tenant_filter ON app.tenant_events", true)
	query("DROP QUOTA tenant_quota", true)
	reader("SELECT 1", false)
	t.Log("Managed access activation, verified TLS, tenant isolation, escalation denial, restart persistence and revocation passed")
}

func clickhouseTenantHTTPS(ctx context.Context, c *Client, d database.Resource, m database.Member, user, password, sql string) (string, error) {
	host := clickhouseMemberHost(d, m)
	tlsConfig, err := c.clickhouseTLSConfig(ctx, d, host, false)
	if err != nil {
		return "", err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DialContext: func(dial context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(host, "8443") {
			return nil, fmt.Errorf("unexpected ClickHouse endpoint")
		}
		return c.clickhouseStream(dial, d, m, 8443, false)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(host, "8443")+"/?wait_end_of_query=1", strings.NewReader(sql))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	response, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return "", fmt.Errorf("tenant TLS request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return "", fmt.Errorf("invalid tenant response")
	}
	if response.StatusCode != http.StatusOK {
		code, _ := strconv.Atoi(response.Header.Get("X-ClickHouse-Exception-Code"))
		return "", fmt.Errorf("tenant query rejected (HTTP %d, engine code %d)", response.StatusCode, code)
	}
	return string(body), nil
}

// ClickHouse access errors: READONLY, UNKNOWN_USER, DATABASE_ACCESS_DENIED,
// ACCESS_STORAGE_READONLY, ACCESS_DENIED and AUTHENTICATION_FAILED.
func clickhouseTenantAccessDenied(err error) bool {
	return err != nil && regexp.MustCompile(`engine code (164|192|291|495|497|516)\)`).MatchString(err.Error())
}
