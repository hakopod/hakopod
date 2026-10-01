// Disposable native MySQL public endpoint acceptance probe. Never prints credentials.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
func step() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
func main() {
	mode := flag.String("mode", "probe", "probe or hold")
	check := flag.String("check", "verified", "verified, crud, data-preserved, reader-write-rejected, plaintext-rejected, wrong-hostname-rejected, wrong-ca-rejected, bad-password-rejected or unreachable")
	host := flag.String("host", "", "reviewed public hostname")
	port := flag.Uint("port", 0, "reviewed public port")
	ca := flag.String("ca", "", "public CA file")
	fingerprint := flag.String("expect-fingerprint", "", "optional SHA256 of the expected served leaf")
	purpose := flag.String("purpose", "read_write", "read_write or read_only")
	revocation := flag.Duration("expect-revocation-within", 0, "require the held session to close within this duration")
	flag.Parse()
	if *mode == "hold" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Hour):
		}
		return
	}
	checks := map[string]bool{"verified": true, "crud": true, "data-preserved": true, "reader-write-rejected": true, "plaintext-rejected": true, "wrong-hostname-rejected": true, "wrong-ca-rejected": true, "bad-password-rejected": true, "unreachable": true}
	if *mode != "probe" || !checks[*check] || *host == "" || len(*host) > 253 || *port < 1 || *port > 65535 || *ca == "" || os.Getenv("MYSQL_PWD") == "" || (*purpose != "read_write" && *purpose != "read_only") || *revocation < 0 || *revocation > 10*time.Minute {
		fail("provide valid bounded probe options and MYSQL_PWD")
	}
	file, err := os.Open(*ca)
	if err != nil {
		fail("public CA unavailable")
	}
	pem, err := io.ReadAll(io.LimitReader(file, 65537))
	file.Close()
	roots := x509.NewCertPool()
	if err != nil || len(pem) > 65536 || !roots.AppendCertsFromPEM(pem) {
		fail("public CA invalid")
	}
	if *fingerprint != "" {
		raw, err := hex.DecodeString(*fingerprint)
		if err != nil || len(raw) != 32 {
			fail("expected fingerprint is invalid")
		}
	}
	address := net.JoinHostPort(*host, strconv.Itoa(int(*port)))
	config := mysql.NewConfig()
	config.User = "app"
	config.Passwd = os.Getenv("MYSQL_PWD")
	config.Net = "tcp"
	config.Addr = address
	config.DBName = "app"
	config.MaxAllowedPacket = 1 << 20
	config.Logger = log.New(io.Discard, "", 0)
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 5 * time.Second
	config.WriteTimeout = 5 * time.Second
	config.TLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: *host}
	if *fingerprint != "" {
		config.TLS.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("server certificate missing")
			}
			hash := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(hash[:]) != *fingerprint {
				return errors.New("server certificate differs from expected issued leaf")
			}
			return nil
		}
	}
	open := func(candidate *mysql.Config) (*sql.DB, *sql.Conn, error) {
		connector, err := mysql.NewConnector(candidate)
		if err != nil {
			return nil, nil, err
		}
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(0)
		ctx, cancel := step()
		defer cancel()
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			return nil, nil, err
		}
		if err = conn.PingContext(ctx); err != nil {
			conn.Close()
			db.Close()
			return nil, nil, err
		}
		return db, conn, nil
	}
	db, conn, err := open(config)
	if *check == "unreachable" {
		if err == nil {
			conn.Close()
			db.Close()
			fail("unlisted source reached the public MySQL endpoint")
		}
		var dns *net.DNSError
		var authentication *mysql.MySQLError
		var certificate *tls.CertificateVerificationError
		var hostname x509.HostnameError
		var authority x509.UnknownAuthorityError
		if errors.As(err, &dns) || errors.As(err, &authentication) || errors.As(err, &certificate) || errors.As(err, &hostname) || errors.As(err, &authority) {
			fail("source refusal cannot be proved by DNS, authentication or certificate failure")
		}
		fmt.Println("PASS unreachable")
		return
	}
	if err != nil {
		fail("verified MySQL TLS connection failed")
	}
	defer db.Close()
	defer conn.Close()
	ctx, cancel := step()
	var name, version string
	var readOnly int
	err = conn.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Ssl_version'").Scan(&name, &version)
	cancel()
	if err != nil || name != "Ssl_version" || (version != "TLSv1.2" && version != "TLSv1.3") {
		fail("native session TLS evidence unavailable")
	}
	ctx, cancel = step()
	err = conn.QueryRowContext(ctx, "SELECT @@super_read_only").Scan(&readOnly)
	cancel()
	if err != nil || (readOnly != 0 && readOnly != 1) || (readOnly == 1) != (*purpose == "read_only") {
		fail("endpoint served wrong MySQL role")
	}
	if *check == "crud" || *check == "data-preserved" || *check == "reader-write-rejected" {
		payload := []byte{0, 255, 10, 13, 128, 65}
		exec := func(query string, args ...any) error {
			ctx, cancel := step()
			defer cancel()
			_, err := conn.ExecContext(ctx, query, args...)
			return err
		}
		if *check == "crud" {
			if readOnly != 0 {
				fail("CRUD requires writer")
			}
			if exec("CREATE TABLE IF NOT EXISTS public_endpoint_acceptance (id INT PRIMARY KEY, payload VARBINARY(64) NOT NULL)") != nil {
				fail("fixture table creation failed")
			}
			if exec("INSERT INTO public_endpoint_acceptance VALUES (1, ?) ON DUPLICATE KEY UPDATE payload=VALUES(payload)", []byte("initial")) != nil || exec("UPDATE public_endpoint_acceptance SET payload=? WHERE id=1", payload) != nil {
				fail("binary write failed")
			}
			if exec("INSERT INTO public_endpoint_acceptance VALUES (2, ?)", payload) != nil || exec("DELETE FROM public_endpoint_acceptance WHERE id=2") != nil {
				fail("fixture insert/delete failed")
			}
		}
		var actual []byte
		ctx, cancel = step()
		err = conn.QueryRowContext(ctx, "SELECT payload FROM public_endpoint_acceptance WHERE id=1").Scan(&actual)
		cancel()
		if err != nil || !bytes.Equal(actual, payload) {
			fail("binary data differs")
		}
		if *check == "data-preserved" && readOnly == 0 {
			if exec("INSERT INTO public_endpoint_acceptance VALUES (4, ?)", payload) != nil || exec("DELETE FROM public_endpoint_acceptance WHERE id=4") != nil {
				fail("recovered writer did not accept new writes")
			}
		}
		if *check == "reader-write-rejected" {
			if readOnly != 1 {
				fail("write refusal requires reader")
			}
			err = exec("INSERT INTO public_endpoint_acceptance VALUES (3, ?)", payload)
			var mysqlErr *mysql.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1290 {
				fail("reader did not explicitly reject write")
			}
		}
		fmt.Println("PASS " + *check)
		return
	}
	if *check != "verified" {
		negative := config.Clone()
		switch *check {
		case "plaintext-rejected":
			negative.TLS = nil
			negative.TLSConfig = "false"
		case "wrong-hostname-rejected":
			negative.TLS = config.TLS.Clone()
			negative.TLS.ServerName = "wrong-hostname.invalid"
		case "wrong-ca-rejected":
			negative.TLS = config.TLS.Clone()
			negative.TLS.RootCAs = x509.NewCertPool()
		case "bad-password-rejected":
			negative.Passwd = "invalid-fixture-password"
		}
		badDB, badConn, badErr := open(negative)
		if badErr == nil {
			badConn.Close()
			badDB.Close()
			fail("negative connection unexpectedly succeeded")
		}
		var mysqlErr *mysql.MySQLError
		var hostErr x509.HostnameError
		var caErr x509.UnknownAuthorityError
		switch *check {
		case "bad-password-rejected":
			if !errors.As(badErr, &mysqlErr) || mysqlErr.Number != 1045 {
				fail("bad password did not produce authentication refusal")
			}
		case "wrong-hostname-rejected":
			if !errors.As(badErr, &hostErr) {
				fail("hostname failure was not certificate validation")
			}
		case "wrong-ca-rejected":
			if !errors.As(badErr, &caErr) {
				fail("CA failure was not certificate validation")
			}
		case "plaintext-rejected":
			if !errors.As(badErr, &mysqlErr) || (mysqlErr.Number != 3159 && mysqlErr.Number != 2026 && mysqlErr.Number != 1045) {
				fail("plaintext failure lacked explicit MySQL refusal")
			}
		}
		// Prove the valid path still works after the negative attempt.
		goodDB, goodConn, goodErr := open(config)
		if goodErr != nil {
			fail("verified reconnect failed after negative check")
		}
		goodConn.Close()
		goodDB.Close()
		fmt.Println("PASS " + *check)
		return
	}
	fmt.Printf("READY tls=%s read_only=%t\n", version, readOnly == 1)
	if *revocation == 0 {
		return
	}
	deadline := time.Now().Add(*revocation)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = conn.PingContext(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				fail("session closure probe timed out")
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				fail("session closure probe timed out")
			}
			fmt.Println("REVOKED existing MySQL session closed")
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	fail("existing MySQL session remained open after deadline")
}
