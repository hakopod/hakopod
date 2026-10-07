package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	myduckMySQLLongPassword     = uint32(1)
	myduckMySQLConnectWithDB    = uint32(1 << 3)
	myduckMySQLProtocol41       = uint32(1 << 9)
	myduckMySQLSSL              = uint32(1 << 11)
	myduckMySQLTransactions     = uint32(1 << 13)
	myduckMySQLSecureConnection = uint32(1 << 15)
	myduckMySQLMultiResults     = uint32(1 << 17)
	myduckMySQLPluginAuth       = uint32(1 << 19)
)

func myduckMySQLPacket(conn net.Conn) ([]byte, byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, 0, err
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if size < 1 || size > 1<<20 {
		return nil, 0, fmt.Errorf("invalid MySQL packet size")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(conn, payload)
	return payload, header[3], err
}

func writeMyDuckMySQLPacket(conn net.Conn, sequence byte, payload []byte) error {
	if len(payload) > 1<<20 {
		return fmt.Errorf("MySQL packet exceeds test bound")
	}
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), sequence}
	packet := append(header, payload...)
	for len(packet) > 0 {
		n, err := conn.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		packet = packet[n:]
	}
	return nil
}

func myduckMySQLError(payload []byte) error {
	if len(payload) == 0 || payload[0] != 0xff {
		return nil
	}
	if len(payload) < 3 {
		return fmt.Errorf("short MySQL error packet")
	}
	message := payload[3:]
	if len(message) >= 6 && message[0] == '#' {
		message = message[6:]
	}
	return &mysqlclient.MySQLError{Number: binary.LittleEndian.Uint16(payload[1:3]), Message: string(message)}
}

func myduckNativePassword(password []byte, seed []byte) []byte {
	first := sha1.Sum(password)
	second := sha1.Sum(first[:])
	h := sha1.New()
	h.Write(seed)
	h.Write(second[:])
	third := h.Sum(nil)
	for i := range first {
		first[i] ^= third[i]
	}
	return first[:]
}

// myduckMySQLInitDB performs the protocol operation which SQL `USE` normally
// represents but which does not pass through the query-text security filter.
func myduckMySQLInitDB(ctx context.Context, c *Client, d database.Resource, observed database.Observation, password []byte, identity *tls.Config, initialDatabase, nextDatabase string) error {
	conn, err := c.myduckStream(ctx, d, observed.Members[0], 3306)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if ok {
		_ = conn.SetDeadline(deadline)
	}
	handshake, sequence, err := myduckMySQLPacket(conn)
	if err != nil || sequence != 0 || len(handshake) < 34 || handshake[0] != 10 {
		return fmt.Errorf("invalid MySQL handshake")
	}
	serverEnd := bytes.IndexByte(handshake[1:], 0)
	if serverEnd < 0 {
		return fmt.Errorf("invalid MySQL server identity")
	}
	offset := serverEnd + 2 + 4
	if offset+8+1+2+1+2+2+1+10 > len(handshake) {
		return fmt.Errorf("short MySQL handshake")
	}
	seed := append([]byte(nil), handshake[offset:offset+8]...)
	offset += 9
	serverCaps := uint32(binary.LittleEndian.Uint16(handshake[offset : offset+2]))
	offset += 2 + 1 + 2
	serverCaps |= uint32(binary.LittleEndian.Uint16(handshake[offset:offset+2])) << 16
	offset += 2
	authLength := int(handshake[offset])
	offset += 1 + 10
	// Native-password challenges contain 20 random bytes followed by a NUL.
	// Preserve random trailing zero bytes rather than trimming the challenge.
	if authLength != 21 || offset+13 > len(handshake) || handshake[offset+12] != 0 {
		return fmt.Errorf("invalid MySQL authentication challenge")
	}
	seed = append(seed, handshake[offset:offset+12]...)
	offset += 13
	plugin := "mysql_native_password"
	if offset < len(handshake) {
		plugin = string(bytes.TrimRight(handshake[offset:], "\x00"))
	}
	if plugin != "mysql_native_password" {
		return fmt.Errorf("unsupported MySQL test auth plugin %q", plugin)
	}
	caps := myduckMySQLLongPassword | myduckMySQLProtocol41 | myduckMySQLSSL | myduckMySQLTransactions | myduckMySQLSecureConnection | myduckMySQLMultiResults | myduckMySQLPluginAuth
	if initialDatabase != "" {
		caps |= myduckMySQLConnectWithDB
	}
	caps &= serverCaps
	if caps&myduckMySQLSSL == 0 || caps&myduckMySQLProtocol41 == 0 || caps&myduckMySQLPluginAuth == 0 || initialDatabase != "" && caps&myduckMySQLConnectWithDB == 0 {
		return fmt.Errorf("MySQL server lacks required protocol capabilities")
	}
	sslRequest := make([]byte, 32)
	binary.LittleEndian.PutUint32(sslRequest, caps)
	binary.LittleEndian.PutUint32(sslRequest[4:], 1<<20)
	sslRequest[8] = 45
	if err = writeMyDuckMySQLPacket(conn, 1, sslRequest); err != nil {
		return err
	}
	tlsConn := tls.Client(conn, identity.Clone())
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		return err
	}
	auth := myduckNativePassword(password, seed)
	response := make([]byte, 0, 64+len(initialDatabase))
	header := make([]byte, 32)
	binary.LittleEndian.PutUint32(header, caps)
	binary.LittleEndian.PutUint32(header[4:], 1<<20)
	header[8] = 45
	response = append(response, header...)
	response = append(response, "root"...)
	response = append(response, 0, byte(len(auth)))
	response = append(response, auth...)
	if initialDatabase != "" {
		response = append(response, initialDatabase...)
		response = append(response, 0)
	}
	response = append(response, plugin...)
	response = append(response, 0)
	if err = writeMyDuckMySQLPacket(tlsConn, 2, response); err != nil {
		return err
	}
	reply, _, err := myduckMySQLPacket(tlsConn)
	if err != nil {
		return err
	}
	if rejection := myduckMySQLError(reply); rejection != nil {
		return rejection
	}
	if len(reply) == 0 || reply[0] != 0x00 {
		return fmt.Errorf("unexpected MySQL authentication response")
	}
	if nextDatabase == "" {
		return nil
	}
	if err = writeMyDuckMySQLPacket(tlsConn, 0, append([]byte{0x02}, nextDatabase...)); err != nil {
		return err
	}
	reply, _, err = myduckMySQLPacket(tlsConn)
	if err != nil {
		return err
	}
	if rejection := myduckMySQLError(reply); rejection != nil {
		// A rejected schema change must leave this authenticated connection in
		// app. The predicate prevents writes even if a regression reaches the
		// protected table, while a successful statement still fails the test.
		if err = writeMyDuckMySQLPacket(tlsConn, 0, append([]byte{0x03}, "DELETE FROM persistent_variable WHERE false"...)); err != nil {
			return fmt.Errorf("MySQL connection failed after rejected schema change: %w", err)
		}
		probe, _, probeErr := myduckMySQLPacket(tlsConn)
		if probeErr != nil {
			return fmt.Errorf("MySQL scope probe failed: %w", probeErr)
		}
		var sqlError *mysqlclient.MySQLError
		if !errors.As(myduckMySQLError(probe), &sqlError) || sqlError.Number == 1045 {
			return fmt.Errorf("MySQL rejected schema change did not preserve the application scope")
		}
		return rejection
	}
	if len(reply) == 0 || reply[0] != 0x00 {
		return fmt.Errorf("unexpected MySQL schema-change response")
	}
	return nil
}

func newMyDuckFixture(t *testing.T, ctx context.Context, c *Client) (database.Resource, []byte, database.Observation) {
	t.Helper()
	identity, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d := myduckFixture()
	d.ID, d.Spec.Name = hex.EncodeToString(identity), "myduck-development-fixture"
	d.Spec.Placement.NodeNames = developmentRecoveryFixtureNodes(t)
	secret := []byte(hex.EncodeToString(password))
	t.Log("Development MyDuck namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if err != nil {
				t.Error("MyDuck fixture cleanup failed", err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("MyDuck fixture cleanup did not finish")
	})
	if err := c.ApplyDatabase(ctx, d, secret, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	return d, secret, waitMyDuckFixture(t, ctx, c, d)
}

func waitMyDuckFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource) database.Observation {
	t.Helper()
	var observed database.Observation
	var err error
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		step, stop := context.WithTimeout(wait, 30*time.Second)
		observed, err = c.ObserveDatabase(step, d)
		stop()
		if err == nil && observed.Status == "ready" {
			return observed
		}
		t.Log("Waiting for MyDuck", observed.Message, err)
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("MyDuck did not become ready", observed.Message, err)
	return observed
}

func testMyDuckReads(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, expected string) {
	t.Helper()
	password, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	mysql, err := c.myduckMySQLClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer mysql.Close()
	var mysqlValue string
	if err := mysql.QueryRowContext(ctx, "SELECT value FROM acceptance WHERE id=1").Scan(&mysqlValue); err != nil || mysqlValue != expected {
		t.Fatal("MySQL shared data read failed", err)
	}
	postgres, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close(ctx)
	var pgValue string
	if err := postgres.QueryRow(ctx, "SELECT value FROM acceptance WHERE id=1").Scan(&pgValue); err != nil || pgValue != expected {
		t.Fatal("PostgreSQL shared data read failed", err)
	}
}

func testMyDuckAlternateUsersRejected(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, password []byte, identity *tls.Config) {
	t.Helper()
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	mysqlConfig := mysqlclient.NewConfig()
	mysqlConfig.User, mysqlConfig.Passwd, mysqlConfig.Net, mysqlConfig.Addr, mysqlConfig.DBName = "attacker", string(password), "tcp", host+":3306", "app"
	mysqlConfig.TLS = identity
	mysqlConfig.DialFunc = func(_ context.Context, _, _ string) (net.Conn, error) {
		return c.myduckStream(ctx, d, observed.Members[0], 3306)
	}
	mysqlConnector, err := mysqlclient.NewConnector(mysqlConfig)
	if err != nil {
		t.Fatal(err)
	}
	mysql := sql.OpenDB(mysqlConnector)
	step, cancel := context.WithTimeout(ctx, 12*time.Second)
	err = mysql.PingContext(step)
	cancel()
	_ = mysql.Close()
	var mysqlAuthError *mysqlclient.MySQLError
	if !errors.As(err, &mysqlAuthError) || mysqlAuthError.Number != 1045 {
		t.Fatal("MySQL did not return an authentication rejection for an alternate user", err)
	}
	postgresConfig, err := pgx.ParseConfig("host=" + host + " port=5432 user=attacker dbname=app connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	postgresConfig.Password, postgresConfig.TLSConfig, postgresConfig.Fallbacks = string(password), identity, nil
	postgresConfig.DialFunc = func(_ context.Context, _, _ string) (net.Conn, error) {
		return c.myduckStream(ctx, d, observed.Members[0], 5432)
	}
	postgresConfig.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	step, cancel = context.WithTimeout(ctx, 12*time.Second)
	postgres, err := pgx.ConnectConfig(step, postgresConfig)
	if err == nil {
		_ = postgres.Close(step)
		cancel()
		t.Fatal("PostgreSQL accepted the managed password for an alternate user")
	}
	cancel()
	var postgresAuthError *pgconn.PgError
	if !errors.As(err, &postgresAuthError) || postgresAuthError.Code != "28P01" {
		t.Fatal("PostgreSQL did not return an authentication rejection for an alternate user", err)
	}
}

func testMyDuckProtocolScope(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, password []byte, identity *tls.Config) {
	t.Helper()
	control, stopControl := context.WithTimeout(ctx, 12*time.Second)
	controlErr := myduckMySQLInitDB(control, c, d, observed, password, identity, "app", "app")
	stopControl()
	if controlErr != nil {
		t.Fatal("MySQL protocol scope control could not authenticate and select app", controlErr)
	}
	for _, schema := range []string{"mysql", "__sys__"} {
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := myduckMySQLInitDB(step, c, d, observed, password, identity, schema, "")
		cancel()
		var rejection *mysqlclient.MySQLError
		if !errors.As(err, &rejection) || rejection.Number == 1045 {
			t.Fatalf("MySQL handshake selected protected schema %q: %v", schema, err)
		}
	}
	for _, schema := range []string{"mysql", "__sys__"} {
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := myduckMySQLInitDB(step, c, d, observed, password, identity, "app", schema)
		cancel()
		var rejection *mysqlclient.MySQLError
		if !errors.As(err, &rejection) || rejection.Number == 1045 {
			t.Fatalf("MySQL COM_INIT_DB selected protected schema %q: %v", schema, err)
		}
	}

	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	for _, databaseName := range []string{"mysql", "__sys__"} {
		config, err := pgx.ParseConfig("host=" + host + " port=5432 user=postgres dbname=" + databaseName + " connect_timeout=5")
		if err != nil {
			t.Fatal(err)
		}
		config.Password, config.TLSConfig, config.Fallbacks = string(password), identity, nil
		config.DialFunc = func(_ context.Context, _, _ string) (net.Conn, error) {
			return c.myduckStream(ctx, d, observed.Members[0], 5432)
		}
		config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		conn, connectErr := pgx.ConnectConfig(step, config)
		if connectErr == nil {
			_ = conn.Close(step)
		}
		cancel()
		var rejection *pgconn.PgError
		if !errors.As(connectErr, &rejection) || rejection.Code == "28P01" {
			t.Fatalf("PostgreSQL selected non-app database %q: %v", databaseName, connectErr)
		}
	}

	for _, startup := range []map[string]string{
		{"search_path": "__sys__"},
		{"options": "-c search_path=__sys__"},
	} {
		testMyDuckPostgresStartupScope(t, ctx, c, d, observed, password, identity, startup)
	}
}

func testMyDuckPostgresStartupScope(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, password []byte, identity *tls.Config, startup map[string]string) {
	t.Helper()
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	config, err := pgx.ParseConfig("host=" + host + " port=5432 user=postgres dbname=app connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	config.Password, config.TLSConfig, config.Fallbacks = string(password), identity, nil
	for key, value := range startup {
		config.RuntimeParams[key] = value
	}
	config.DialFunc = func(_ context.Context, _, _ string) (net.Conn, error) {
		return c.myduckStream(ctx, d, observed.Members[0], 5432)
	}
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	step, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, connectErr := pgx.ConnectConfig(step, config)
	if connectErr != nil {
		var rejection *pgconn.PgError
		if !errors.As(connectErr, &rejection) || rejection.Code == "28P01" {
			t.Fatal("PostgreSQL startup search_path did not return a SQL rejection", connectErr)
		}
		return
	}
	defer conn.Close(step)
	var schema string
	if err = conn.QueryRow(step, "SELECT current_schema()").Scan(&schema); err != nil || schema != "app" {
		t.Fatal("PostgreSQL startup search_path escaped the managed app schema", schema, err)
	}
	if _, err = conn.Exec(step, "DELETE FROM persistent_variable WHERE false"); err == nil {
		t.Fatal("PostgreSQL accepted an unqualified protected-table mutation after startup search_path")
	} else {
		var rejection *pgconn.PgError
		if !errors.As(err, &rejection) {
			t.Fatal("PostgreSQL unqualified protected-table mutation did not return a SQL rejection", err)
		}
	}
}

func assertMyDuckFailedRestoreIsolated(t *testing.T, ctx context.Context, c *Client, d database.Resource, jobID string, helperExpected bool) {
	t.Helper()
	namespace := DatabaseNamespace(d.ID)
	set, err := c.kube.AppsV1().StatefulSets(namespace).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Spec.Replicas == nil || *set.Spec.Replicas != 0 {
		t.Fatal("failed MyDuck restore target resumed")
	}
	record, err := myduckReadColdRecord(set, d, jobID)
	if err != nil || !record.Restore {
		t.Fatal("failed MyDuck restore lost its durable storage fence", err)
	}
	_, err = c.kube.CoreV1().Pods(namespace).Get(ctx, myduckColdName(jobID), metav1.GetOptions{})
	if helperExpected && err != nil {
		t.Fatal("failed MyDuck restore lost its fenced storage helper", err)
	}
	if !helperExpected && !apierrors.IsNotFound(err) {
		t.Fatal("failed MyDuck restore helper was not removed", err)
	}
}

func assertMyDuckFailedRestoreCleaned(t *testing.T, ctx context.Context, c *Client, d database.Resource, jobID string, before func() error) {
	t.Helper()
	assertMyDuckFailedRestoreIsolated(t, ctx, c, d, jobID, true)
	if err := c.ReconcileMyDuckColdStorage(ctx, d, jobID, false, before); err != nil {
		t.Fatal("failed MyDuck restore cleanup did not finish", err)
	}
	namespace := DatabaseNamespace(d.ID)
	set, err := c.kube.AppsV1().StatefulSets(namespace).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || set.Spec.Replicas == nil || *set.Spec.Replicas != 0 || set.Annotations[myduckColdAnnotation] != "" {
		t.Fatal("failed MyDuck restore cleanup did not preserve the stopped target", err)
	}
	if _, err = c.kube.CoreV1().Pods(namespace).Get(ctx, myduckColdName(jobID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("failed MyDuck restore cleanup left its helper", err)
	}
	policy, err := c.kube.NetworkingV1().NetworkPolicies(namespace).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || len(policy.Spec.Ingress) != 0 {
		t.Fatal("failed MyDuck restore target retained application ingress", err)
	}
	members, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: myduckMemberLabel + "=true", Limit: 2})
	if err != nil || members.Continue != "" || len(members.Items) != 0 {
		t.Fatal("failed MyDuck restore target retained a database process", err)
	}
}

func TestManagedMyDuckLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYDUCK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYDUCK_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, 20*time.Minute)
	d, password, observed := newMyDuckFixture(t, ctx, c)
	if len(observed.Endpoints) != 2 || observed.TLS == nil || !observed.TLS.Verified || !observed.TLS.PlaintextRejected {
		t.Fatal("MyDuck did not verify both encrypted protocols")
	}
	_, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("alternate_users", func(t *testing.T) {
		testMyDuckAlternateUsersRejected(t, ctx, c, d, observed, password, identity)
	})
	t.Run("protocol_scope", func(t *testing.T) {
		testMyDuckProtocolScope(t, ctx, c, d, observed, password, identity)
	})
	mysql, err := c.myduckMySQLClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer mysql.Close()
	if _, err = mysql.ExecContext(ctx, "CREATE TABLE acceptance (id INTEGER PRIMARY KEY, value VARCHAR(255))"); err != nil {
		t.Fatal("MySQL create failed", err)
	}
	if _, err = mysql.ExecContext(ctx, "INSERT INTO acceptance VALUES (1, 'from mysql')"); err != nil {
		t.Fatal("MySQL insert failed", err)
	}
	testMyDuckReads(t, ctx, c, d, observed, "from mysql")
	metrics, err := c.myduckEngineMetrics(ctx, d, observed)
	if err != nil || !metrics.Available || metrics.DataBytes == nil || metrics.SampledAt == nil {
		t.Fatal("MyDuck did not report real DuckDB storage statistics", err)
	}
	pg, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, "UPDATE acceptance SET value='from postgres' WHERE id=1"); err != nil {
		t.Fatal("PostgreSQL update failed", err)
	}
	_ = pg.Close(ctx)
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	for _, query := range []string{
		"SELECT * FROM read_text('/etc/hakopod-app/password')",
		`SELECT * FROM "read_text"('/etc/hakopod-app/password')`,
		"SELECT * FROM '/etc/hakopod-app/password'",
		"COPY acceptance TO '/tmp/export.csv'",
		"EXPORT DATABASE '/tmp/exported-database'",
		"IMPORT DATABASE '/tmp/exported-database'",
		"ATTACH '/tmp/other.db' AS other",
		"INSTALL httpfs", "LOAD httpfs",
		"CREATE USER attacker IDENTIFIED BY 'unsafe'",
		"ALTER USER root IDENTIFIED BY 'unsafe'",
		"RENAME USER root TO attacker",
		"UPDATE mysql.user SET authentication_string=''",
		"CREATE TABLE mysql.restore_guard AS SELECT 1 AS value",
		"SET schema='__sys__'",
		"SET search_path='__sys__'",
		"USE __sys__",
		"SET enable_external_access=true",
		"SET memory_limit='100TiB'",
		"PRAGMA enable_external_access=true",
		"PRAGMA memory_limit='100TiB'",
	} {
		_, err = mysql.ExecContext(ctx, query)
		var mysqlRejection *mysqlclient.MySQLError
		if !errors.As(err, &mysqlRejection) {
			t.Fatalf("MySQL did not return a SQL rejection for forbidden statement %q: %v", query, err)
		}
		pg, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
		if err != nil {
			t.Fatal(err)
		}
		_, queryErr := pg.Exec(ctx, query)
		_ = pg.Close(ctx)
		var postgresRejection *pgconn.PgError
		if !errors.As(queryErr, &postgresRejection) {
			t.Fatalf("PostgreSQL did not return a SQL rejection for forbidden statement %q: %v", query, queryErr)
		}
	}
	for _, testcase := range []struct {
		name     string
		password []byte
		tls      *tls.Config
	}{
		{"wrong password", []byte("not-the-managed-password"), identity},
		{"plaintext", password, nil},
		{"wrong hostname", password, func() *tls.Config { config := identity.Clone(); config.ServerName = "wrong.invalid"; return config }()},
		{"wrong CA", password, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: identity.ServerName}},
	} {
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		client, err := c.myduckMySQLClient(step, d, observed.Members[0], testcase.password, testcase.tls)
		if err == nil {
			err = client.PingContext(step)
			_ = client.Close()
		}
		if err == nil {
			cancel()
			t.Fatal("MySQL accepted", testcase.name)
		}
		conn, pgErr := c.myduckPostgresClient(step, d, observed.Members[0], testcase.password, testcase.tls)
		if pgErr == nil {
			_ = conn.Close(step)
			cancel()
			t.Fatal("PostgreSQL accepted", testcase.name)
		}
		cancel()
	}
	oldUID := observed.Members[0].UID
	pod, _, err := c.databaseExecTarget(ctx, d, observed.Members[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		t.Fatal(err)
	}
	observed = waitMyDuckFixture(t, ctx, c, d)
	if observed.Members[0].UID == oldUID {
		t.Fatal("MyDuck pod replacement was not observed")
	}
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	observed = testMyDuckRenewal(t, ctx, c, d, observed)
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	t.Log("Both protocols, authenticated TLS, access refusal, file/network settings and persistent replacement verified")
}

func testMyDuckRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, previous database.Observation) database.Observation {
	t.Helper()
	password, oldIdentity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	// Applications trust the database CA and hostname. The control-plane
	// observation additionally pins the current leaf, which changes on renewal.
	oldIdentity = oldIdentity.Clone()
	oldIdentity.VerifyConnection = nil
	issuer, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(issuer.Data["ca.crt"], issuer.Data["ca.key"])
	if err != nil {
		t.Fatal("MyDuck fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	issuer.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(issuer.Namespace).Update(ctx, issuer, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		next, observeErr := c.ObserveDatabase(wait, d)
		if observeErr == nil && next.Status == "ready" && len(next.Members) == 1 && next.Members[0].UID != previous.Members[0].UID && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != previous.TLS.Fingerprint && next.TLS.CAFingerprint != previous.TLS.CAFingerprint {
			mysql, err := c.myduckMySQLClient(wait, d, next.Members[0], password, oldIdentity)
			if err != nil {
				t.Fatal(err)
			}
			err = mysql.PingContext(wait)
			_ = mysql.Close()
			if err != nil {
				t.Fatal("MySQL lost existing CA trust during renewal", err)
			}
			pg, err := c.myduckPostgresClient(wait, d, next.Members[0], password, oldIdentity)
			if err != nil {
				t.Fatal("PostgreSQL lost existing CA trust during renewal", err)
			}
			_ = pg.Close(wait)
			return next
		}
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("MyDuck did not serve its renewed identity on both protocols")
	return previous
}

func TestManagedMyDuckColdRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYDUCK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYDUCK_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, 25*time.Minute)
	source, password, observed := newMyDuckFixture(t, ctx, c)
	_, identity, err := c.myduckClientIdentity(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c.myduckMySQLClient(ctx, source, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecContext(ctx, "CREATE TABLE acceptance (id INTEGER PRIMARY KEY, value VARCHAR(255))"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecContext(ctx, "INSERT INTO acceptance VALUES (1, 'recovered')"); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	jobID := fmt.Sprintf("%032x", time.Now().UnixNano())
	before := func() error { return ctx.Err() }
	archive := &databaseBoundedWriter{limit: 32 << 20}
	if err = c.WithMyDuckColdStorage(ctx, source, observed, jobID, false, before, nil, archive); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileMyDuckColdStorage(ctx, source, jobID, true, before); err != nil {
		t.Fatal(err)
	}
	observed = waitMyDuckFixture(t, ctx, c, source)
	testMyDuckReads(t, ctx, c, source, observed, "recovered")
	nonempty, nonemptyPassword, nonemptyHealth := newMyDuckFixture(t, ctx, c)
	_, nonemptyIdentity, err := c.myduckClientIdentity(ctx, nonempty)
	if err != nil {
		t.Fatal(err)
	}
	nonemptyClient, err := c.myduckPostgresClient(ctx, nonempty, nonemptyHealth.Members[0], nonemptyPassword, nonemptyIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nonemptyClient.Exec(ctx, "CREATE TABLE restore_guard AS SELECT 1 AS value"); err != nil {
		_ = nonemptyClient.Close(ctx)
		t.Fatal("create nonempty MyDuck target", err)
	}
	_ = nonemptyClient.Close(ctx)
	nonemptyID := fmt.Sprintf("%032x", time.Now().UnixNano())
	nonempty.Status = "restoring"
	nonempty.Recovery = &database.Recovery{JobID: nonemptyID}
	t.Run("nonempty_target", func(t *testing.T) {
		input := bytes.NewReader(archive.Bytes())
		remaining := input.Len()
		if err = c.WithMyDuckColdStorage(ctx, nonempty, nonemptyHealth, nonemptyID, true, before, input, nil); err == nil {
			t.Fatal("MyDuck restore accepted an existing user table")
		}
		if input.Len() != remaining {
			t.Fatal("MyDuck restore consumed archive bytes before rejecting a nonempty target")
		}
		assertMyDuckFailedRestoreCleaned(t, ctx, c, nonempty, nonemptyID, before)
	})

	baseline, baselinePassword, baselineHealth := newMyDuckFixture(t, ctx, c)
	t.Run("baseline_replacement", func(t *testing.T) {
		_, identity, err := c.myduckClientIdentity(ctx, baseline)
		if err != nil {
			t.Fatal(err)
		}
		client, err := c.myduckPostgresClient(ctx, baseline, baselineHealth.Members[0], baselinePassword, identity)
		if err != nil {
			t.Fatal(err)
		}
		// Preserve the catalog object name and count while changing its
		// definition and storing user data. Either the runtime must reject
		// this write or the stopped-target check must reject the restore.
		_, mutationErr := client.Exec(ctx, "CREATE OR REPLACE TABLE app.__sys__.persistent_variable AS SELECT 'restore guard' AS user_payload")
		_ = client.Close(ctx)
		if mutationErr != nil {
			var rejection *pgconn.PgError
			if !errors.As(mutationErr, &rejection) {
				t.Fatal("baseline replacement did not return a SQL rejection", mutationErr)
			}
			waitMyDuckFixture(t, ctx, c, baseline)
			t.Log("Managed runtime rejected replacement of a baseline catalog table")
			return
		}
		jobID := fmt.Sprintf("%032x", time.Now().UnixNano())
		baseline.Status = "restoring"
		baseline.Recovery = &database.Recovery{JobID: jobID}
		input := bytes.NewReader(archive.Bytes())
		remaining := input.Len()
		if err = c.WithMyDuckColdStorage(ctx, baseline, baselineHealth, jobID, true, before, input, nil); err == nil {
			t.Fatal("MyDuck restore accepted user data replacing a baseline catalog table")
		}
		if input.Len() != remaining {
			t.Fatal("MyDuck restore consumed archive bytes before rejecting an altered baseline table")
		}
		assertMyDuckFailedRestoreCleaned(t, ctx, c, baseline, jobID, before)
	})

	corrupt, _, corruptHealth := newMyDuckFixture(t, ctx, c)
	corruptID := fmt.Sprintf("%032x", time.Now().UnixNano())
	corrupt.Status = "restoring"
	corrupt.Recovery = &database.Recovery{JobID: corruptID}
	broken := append([]byte(nil), archive.Bytes()...)
	if len(broken) == 0 {
		t.Fatal("MyDuck capture returned an empty archive")
	}
	broken[len(broken)-1] ^= 0xff
	t.Run("corrupt_restore", func(t *testing.T) {
		if err = c.WithMyDuckColdStorage(ctx, corrupt, corruptHealth, corruptID, true, before, bytes.NewReader(broken), nil); err == nil {
			t.Fatal("MyDuck restore accepted a corrupt archive")
		}
		assertMyDuckFailedRestoreCleaned(t, ctx, c, corrupt, corruptID, before)
	})

	target, _, targetHealth := newMyDuckFixture(t, ctx, c)
	restoreID := fmt.Sprintf("%032x", time.Now().UnixNano())
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: restoreID}
	t.Run("successful_restore", func(t *testing.T) {
		if err = c.WithMyDuckColdStorage(ctx, target, targetHealth, restoreID, true, before, bytes.NewReader(archive.Bytes()), nil); err != nil {
			t.Fatal(err)
		}
		if err = c.ReconcileMyDuckColdStorage(ctx, target, restoreID, true, before); err != nil {
			t.Fatal(err)
		}
		targetHealth = waitMyDuckFixture(t, ctx, c, target)
		testMyDuckReads(t, ctx, c, target, targetHealth, "recovered")
		identities := make(map[string]bool)
		for _, fixture := range []database.Resource{source, nonempty, baseline, corrupt, target} {
			if identities[fixture.ID] {
				t.Fatal("recovery fixtures did not use separate resources")
			}
			identities[fixture.ID] = true
		}
	})
	t.Log("Cold backup resumed its source; failed targets stayed isolated; separate target restored both protocols with its own credentials")
}
