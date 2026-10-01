// Disposable Oracle Database Free public-TCPS acceptance probe.
package main

import (
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
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	goora "github.com/sijms/go-ora/v3"
)

type options struct {
	mode, check, host, expectedAddress, caPath, expectedFingerprint, password string
	port                                                                      int
	revocation                                                                time.Duration
	caPEM                                                                     []byte
	address                                                                   string
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (d dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

func main() {
	o := options{}
	flag.StringVar(&o.mode, "mode", "probe", "probe or hold")
	flag.StringVar(&o.check, "check", "verified", "acceptance check")
	flag.StringVar(&o.host, "host", "", "reviewed public hostname")
	flag.StringVar(&o.expectedAddress, "expect-address", "", "reviewed IPv4 address")
	flag.IntVar(&o.port, "port", 0, "public TCPS port")
	flag.StringVar(&o.caPath, "ca", "", "reviewed public CA PEM file")
	flag.StringVar(&o.expectedFingerprint, "expect-fingerprint", "", "expected SHA256 leaf fingerprint")
	flag.DurationVar(&o.revocation, "expect-revocation-within", 0, "require the existing session to close")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if o.mode == "hold" && o.host == "" {
		select {
		case <-ctx.Done():
		case <-time.After(24 * time.Hour):
		}
		return
	}
	if err := o.prepare(ctx); err != nil {
		fail("invalid or unavailable probe input")
	}
	if o.mode == "hold" {
		if err := hold(ctx, o); err != nil {
			fail("existing Oracle session revocation was not proved")
		}
		return
	}
	if o.check == "unreachable" {
		if err := unreachable(ctx, o); err != nil {
			fail("source refusal was not proved")
		}
		fmt.Println("PASS unreachable")
		return
	}
	if err := verified(ctx, o); err != nil {
		fail("initial APP FREEPDB1 TCPS verification failed")
	}
	var err error
	switch o.check {
	case "verified":
	case "crud", "data-preserved":
		err = durableData(ctx, o)
	case "privileges":
		err = privilegeBoundary(ctx, o)
	case "wrong-ca-rejected", "wrong-hostname-rejected", "bad-password-rejected", "plaintext-rejected":
		err = negative(ctx, o, o.check)
	case "raw-ports-refused":
		err = rawPortsRefused(ctx, o)
	default:
		err = errors.New("unknown check")
	}
	if err != nil {
		fail("requested Oracle check failed")
	}
	if err = verified(ctx, o); err != nil {
		fail("valid reconnect after requested check failed")
	}
	fmt.Println("PASS " + o.check)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func (o *options) prepare(ctx context.Context) error {
	checks := map[string]bool{"verified": true, "crud": true, "data-preserved": true, "privileges": true,
		"wrong-ca-rejected": true, "wrong-hostname-rejected": true, "bad-password-rejected": true,
		"plaintext-rejected": true, "raw-ports-refused": true, "unreachable": true}
	if (o.mode != "probe" && o.mode != "hold") || !checks[o.check] || o.port < 1024 || o.port > 65535 ||
		o.caPath == "" || o.revocation < 0 || o.revocation > 2*time.Minute || len(o.host) > 253 {
		return errors.New("arguments")
	}
	if !validHostname(o.host) {
		return errors.New("hostname")
	}
	o.password = os.Getenv("ORACLE_PASSWORD")
	if raw, err := hex.DecodeString(o.password); err != nil || len(raw) != 32 {
		return errors.New("password")
	}
	file, err := os.Open(o.caPath)
	if err != nil {
		return err
	}
	o.caPEM, err = readBounded(file, 32<<10)
	_ = file.Close()
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(o.caPEM) {
		return errors.New("CA")
	}
	if o.expectedFingerprint != "" {
		raw, err := hex.DecodeString(o.expectedFingerprint)
		if err != nil || len(raw) != 32 {
			return errors.New("fingerprint")
		}
	}
	lookup, stop := context.WithTimeout(ctx, 3*time.Second)
	ips, err := net.DefaultResolver.LookupIPAddr(lookup, o.host)
	stop()
	if err != nil || len(ips) == 0 || len(ips) > 16 {
		return errors.New("DNS")
	}
	address, err := reviewedDNSAddress(ips, o.expectedAddress)
	if err != nil {
		return err
	}
	o.address = address
	return nil
}

func validHostname(value string) bool {
	if len(value) < 1 || len(value) > 253 || net.ParseIP(value) != nil || value != strings.ToLower(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func reviewedDNSAddress(ips []net.IPAddr, expected string) (string, error) {
	reviewed, err := netip.ParseAddr(expected)
	if err != nil || !reviewed.Is4() || reviewed.IsUnspecified() || reviewed.IsMulticast() {
		return "", errors.New("reviewed address")
	}
	for _, ip := range ips {
		if ip.Zone != "" || ip.IP.To4() == nil || ip.IP.String() != reviewed.String() {
			return "", errors.New("DNS address changed")
		}
	}
	return reviewed.String(), nil
}

func tlsConfig(o options, hostname string, wrongCA bool) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !wrongCA && !roots.AppendCertsFromPEM(o.caPEM) {
		return nil, errors.New("CA")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname, RootCAs: roots}
	if o.expectedFingerprint != "" && !wrongCA && hostname == o.host {
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("unexpected leaf chain")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), o.expectedFingerprint) {
				return errors.New("leaf fingerprint changed")
			}
			return nil
		}
	}
	return config, nil
}

func open(ctx context.Context, o options, password, hostname string, secure, wrongCA bool) (*sql.DB, error) {
	options := map[string]string{"CONNECTION TIMEOUT": "5", "TIMEOUT": "8", "FAST LOGIN": "false", "SSL VERIFY": "true"}
	if secure {
		options["SSL"] = "enable"
	}
	connector := goora.NewConnector(goora.BuildUrl(o.host, o.port, "FREEPDB1", "APP", password, options)).(*goora.OracleConnector)
	if secure {
		config, err := tlsConfig(o, hostname, wrongCA)
		if err != nil {
			return nil, err
		}
		connector.WithTLSConfig(config)
	}
	connector.Dialer(dialerFunc(func(dial context.Context, network, requested string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || requested != net.JoinHostPort(o.host, strconv.Itoa(o.port)) {
			return nil, errors.New("driver requested an unexpected endpoint")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(dial, "tcp", net.JoinHostPort(o.address, strconv.Itoa(o.port)))
	}))
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(20 * time.Second)
	step, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(step); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func identity(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	step, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var value string
	if err := db.QueryRowContext(step, "SELECT SYS_CONTEXT('USERENV','SESSION_USER')||'|'||SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&value); err != nil {
		return err
	}
	if value != "APP|FREEPDB1" {
		return errors.New("unexpected identity")
	}
	return nil
}

func verified(ctx context.Context, o options) error {
	db, err := open(ctx, o, o.password, o.host, true, false)
	if err != nil {
		return err
	}
	defer db.Close()
	return identity(ctx, db)
}

func durableData(ctx context.Context, o options) error {
	db, err := open(ctx, o, o.password, o.host, true, false)
	if err != nil {
		return err
	}
	defer db.Close()
	statements := []string{
		"BEGIN EXECUTE IMMEDIATE 'CREATE TABLE hakopod_public_acceptance (id NUMBER PRIMARY KEY, value VARCHAR2(80))'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;",
		"MERGE INTO hakopod_public_acceptance d USING (SELECT 7 id, 'durable-tcps' value FROM dual) s ON (d.id=s.id) WHEN MATCHED THEN UPDATE SET d.value=s.value WHEN NOT MATCHED THEN INSERT (id,value) VALUES (s.id,s.value)",
	}
	for _, statement := range statements {
		step, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = db.ExecContext(step, statement)
		cancel()
		if err != nil {
			return err
		}
	}
	var value string
	step, cancel := context.WithTimeout(ctx, 8*time.Second)
	err = db.QueryRowContext(step, "SELECT value FROM hakopod_public_acceptance WHERE id=7").Scan(&value)
	cancel()
	if err != nil || value != "durable-tcps" {
		return errors.New("durable row changed")
	}
	return nil
}

func privilegeBoundary(ctx context.Context, o options) error {
	checks := []string{"CREATE USER hakopod_public_attacker IDENTIFIED BY invalid_password",
		"SELECT * FROM sys.user$", "ALTER SYSTEM SET sessions=500 SCOPE=SPFILE"}
	for _, query := range checks {
		db, err := open(ctx, o, o.password, o.host, true, false)
		if err != nil {
			return err
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, err = db.ExecContext(step, query)
		cancel()
		_ = db.Close()
		if err == nil {
			return errors.New("APP exceeded its schema privilege boundary")
		}
		if err = verified(ctx, o); err != nil {
			return errors.New("valid reconnect after privilege rejection failed")
		}
	}
	return nil
}

func negative(ctx context.Context, o options, check string) error {
	if check == "wrong-ca-rejected" || check == "wrong-hostname-rejected" {
		hostname, wrongCA := o.host, false
		if check == "wrong-ca-rejected" {
			wrongCA = true
		} else {
			hostname = "wrong-hostname.invalid"
		}
		config, err := tlsConfig(o, hostname, wrongCA)
		if err != nil {
			return err
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		raw, err := (&net.Dialer{}).DialContext(step, "tcp", net.JoinHostPort(o.address, strconv.Itoa(o.port)))
		if err != nil {
			return errors.New("TLS negative could not reach the reviewed listener")
		}
		secured := tls.Client(raw, config)
		err = secured.HandshakeContext(step)
		_ = secured.Close()
		if err == nil {
			return errors.New("negative TLS identity succeeded")
		}
		var verification *tls.CertificateVerificationError
		var unknown x509.UnknownAuthorityError
		var host x509.HostnameError
		if !errors.As(err, &verification) && !errors.As(err, &unknown) && !errors.As(err, &host) {
			return errors.New("TLS negative did not fail certificate verification")
		}
		return verified(ctx, o)
	}
	password, hostname, secure, wrongCA := o.password, o.host, true, false
	switch check {
	case "bad-password-rejected":
		password = "invalid-public-probe-password"
	case "plaintext-rejected":
		secure = false
	}
	db, err := open(ctx, o, password, hostname, secure, wrongCA)
	if err == nil {
		_ = db.Close()
		return errors.New("negative connection succeeded")
	}
	if check == "bad-password-rejected" && !oracleInvalidCredentials(err) {
		return errors.New("bad password did not return ORA-01017")
	}
	return verified(ctx, o)
}

func oracleInvalidCredentials(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "ORA-01017")
}

func rawPortsRefused(ctx context.Context, o options) error {
	for _, port := range []int{1521, 5500} {
		step, cancel := context.WithTimeout(ctx, 4*time.Second)
		connection, err := (&net.Dialer{}).DialContext(step, "tcp", net.JoinHostPort(o.address, strconv.Itoa(port)))
		cancel()
		if err == nil {
			_ = connection.Close()
			return errors.New("raw Oracle listener is reachable")
		}
		if err = verified(ctx, o); err != nil {
			return errors.New("valid reconnect after raw-port refusal failed")
		}
	}
	return nil
}

func unreachable(ctx context.Context, o options) error {
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(step, "tcp", net.JoinHostPort(o.address, strconv.Itoa(o.port)))
	if err == nil {
		_ = connection.Close()
		return errors.New("denied source reached public TCP")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return errors.New("DNS failure does not prove source refusal")
	}
	return nil
}

func hold(ctx context.Context, o options) error {
	db, err := open(ctx, o, o.password, o.host, true, false)
	if err != nil {
		return err
	}
	defer db.Close()
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err = identity(ctx, connection); err != nil {
		return err
	}
	fmt.Println("READY existing Oracle session")
	deadline := time.Now().Add(o.revocation)
	for time.Now().Before(deadline) {
		step, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = identity(step, connection)
		cancel()
		if err != nil {
			fmt.Println("REVOKED existing Oracle session closed")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("session remained open")
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	value, err := io.ReadAll(limited)
	if err != nil || int64(len(value)) > maximum {
		return nil, errors.New("bounded read")
	}
	return value, nil
}
