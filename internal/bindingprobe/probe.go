// Package bindingprobe checks one connection value from a running workload.
// It never returns the value or a driver error.
package bindingprobe

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxInput       = 4096
	MaxEnvironment = 16384
	MaxCA          = 1 << 20
	MaxAddresses   = 8
	MaxDuration    = 20 * time.Second
)

type Request struct {
	SchemaVersion int    `json:"schema_version"`
	Protocol      string `json:"protocol"`
	Variable      string `json:"variable"`
	CAFile        string `json:"ca_file"`
	TimeoutMS     int    `json:"timeout_ms,omitempty"`
	Nonce         string `json:"fingerprint_nonce,omitempty"`
}

type Stage struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Result struct {
	SchemaVersion int     `json:"schema_version"`
	Protocol      string  `json:"protocol,omitempty"`
	Variable      string  `json:"variable,omitempty"`
	Fingerprint   string  `json:"loaded_fingerprint,omitempty"`
	Stages        []Stage `json:"stages"`
}

type target struct {
	host     string
	port     int
	database string
	user     string
	password string
	protocol string
}

func Run(parent context.Context, req Request, getenv func(string) (string, bool)) Result {
	result := Result{SchemaVersion: 1, Protocol: req.Protocol, Variable: req.Variable}
	fail := func(name, code, message string) Result {
		result.Stages = append(result.Stages, Stage{Name: name, Status: "failed", Code: code, Message: message})
		return result
	}
	if req.SchemaVersion != 1 || !validName(req.Variable) || len(req.CAFile) > 4096 || (req.TimeoutMS != 0 && (req.TimeoutMS < 1000 || req.TimeoutMS > 20000)) {
		return fail("configuration", "invalid_request", "The probe request is invalid.")
	}
	raw, ok := getenv(req.Variable)
	if !ok || raw == "" {
		return fail("configuration", "variable_not_loaded", "The running process did not load this variable.")
	}
	if len(raw) > MaxEnvironment {
		return fail("configuration", "variable_too_large", "The loaded variable exceeds 16 KiB.")
	}
	t, err := parseTarget(req.Protocol, raw)
	if err != nil {
		return fail("configuration", "connection_value_invalid", "The loaded variable is not a supported connection value.")
	}
	if req.Nonce != "" {
		if len(req.Nonce) < 16 || len(req.Nonce) > 256 {
			return fail("configuration", "fingerprint_nonce_invalid", "The comparison nonce is invalid.")
		}
		mac := hmac.New(sha256.New, []byte(req.Nonce))
		_, _ = mac.Write([]byte(raw))
		result.Fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	result.Stages = append(result.Stages, Stage{Name: "configuration", Status: "passed", Code: "variable_loaded", Message: "The running process loaded a valid connection value."})
	if t.protocol == "mongodb" || t.protocol == "clickhouse" || t.protocol == "oracle" {
		result.Stages = append(result.Stages, Stage{Name: "capability", Status: "unsupported", Code: "protocol_probe_unsupported", Message: "This helper version cannot verify this protocol."})
		return result
	}
	duration := time.Duration(req.TimeoutMS) * time.Millisecond
	if duration == 0 {
		duration = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, t.host)
	if err != nil || len(addresses) == 0 || len(addresses) > MaxAddresses {
		return fail("dns", "dns_lookup_failed", "DNS did not return a bounded address set for the connection host.")
	}
	result.Stages = append(result.Stages, Stage{Name: "dns", Status: "passed", Code: "host_resolved", Message: "DNS resolved the connection host."})
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(t.host, strconv.Itoa(t.port)))
	if err != nil {
		return fail("network", "network_unreachable", "The application runtime could not open a TCP connection.")
	}
	_ = conn.Close()
	result.Stages = append(result.Stages, Stage{Name: "network", Status: "passed", Code: "tcp_connected", Message: "The application runtime opened a TCP connection."})
	roots, err := loadRoots(req.CAFile)
	if err != nil {
		return fail("certificate", "ca_unavailable", "The configured CA file is unavailable or invalid.")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.host, RootCAs: roots}
	verified := false
	tlsConfig.VerifyConnection = func(tls.ConnectionState) error {
		verified = true
		return nil
	}
	queryErr := probe(ctx, t, tlsConfig)
	if queryErr != nil {
		stage, code, message := classify(queryErr)
		if !verified && stage != "dns" {
			if stage == "certificate" {
				return fail(stage, code, message)
			}
			return fail("certificate", "tls_handshake_failed", "TLS could not establish a verified connection.")
		}
		if verified {
			result.Stages = append(result.Stages, Stage{Name: "certificate", Status: "passed", Code: "tls_verified", Message: "TLS verified the server hostname and certificate chain."})
			if stage == "query" && code == "read_query_rejected" {
				result.Stages = append(result.Stages, Stage{Name: "authentication", Status: "passed", Code: "authenticated", Message: "The server accepted the loaded credentials."})
			}
		}
		return fail(stage, code, message)
	}
	result.Stages = append(result.Stages,
		Stage{Name: "certificate", Status: "passed", Code: "tls_verified", Message: "TLS verified the server hostname and certificate chain."},
		Stage{Name: "authentication", Status: "passed", Code: "authenticated", Message: "The server accepted the loaded credentials."},
		Stage{Name: "query", Status: "passed", Code: "read_query_succeeded", Message: "A minimal read-only query succeeded. This result does not prove access to application tables or all required grants."},
	)
	return result
}

func validName(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func parseTarget(protocol, raw string) (target, error) {
	aliases := map[string]string{"postgresql": "postgres", "vitess": "mysql", "myduck": "mysql"}
	if aliases[protocol] != "" {
		protocol = aliases[protocol]
	}
	if protocol != "postgres" && protocol != "mysql" && protocol != "redis" && protocol != "mongodb" && protocol != "clickhouse" && protocol != "oracle" {
		return target{}, errors.New("protocol")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || len(u.Hostname()) > 253 {
		return target{}, errors.New("url")
	}
	want := map[string][]string{"postgres": {"postgres", "postgresql"}, "mysql": {"mysql"}, "redis": {"rediss"}, "mongodb": {"mongodb"}, "clickhouse": {"https", "clickhouse"}, "oracle": {"oracle", "tcps"}}[protocol]
	good := false
	for _, scheme := range want {
		good = good || u.Scheme == scheme
	}
	if !good {
		return target{}, errors.New("scheme")
	}
	password, ok := u.User.Password()
	if !ok {
		return target{}, errors.New("password")
	}
	defaults := map[string]int{"postgres": 5432, "mysql": 3306, "redis": 6379, "mongodb": 27017, "clickhouse": 8443, "oracle": 1521}
	port := defaults[protocol]
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return target{}, errors.New("port")
		}
	}
	database := strings.TrimPrefix(u.EscapedPath(), "/")
	if decoded, e := url.PathUnescape(database); e == nil {
		database = decoded
	}
	if database == "" {
		return target{}, errors.New("database")
	}
	return target{host: u.Hostname(), port: port, database: database, user: u.User.Username(), password: password, protocol: protocol}, nil
}

func loadRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, errors.New("CA")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxCA+1))
	if err != nil || len(b) > MaxCA {
		return nil, errors.New("CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(b) {
		return nil, errors.New("CA")
	}
	return roots, nil
}

func probe(ctx context.Context, t target, tlsConfig *tls.Config) error {
	switch t.protocol {
	case "postgres":
		return probePostgres(ctx, t, tlsConfig)
	case "mysql":
		return probeMySQL(ctx, t, tlsConfig)
	case "redis":
		return probeRedis(ctx, t, tlsConfig)
	}
	return errors.New("unsupported")
}

func probePostgres(ctx context.Context, t target, tc *tls.Config) error {
	dsn := (&url.URL{Scheme: "postgres", Host: net.JoinHostPort(t.host, strconv.Itoa(t.port)), Path: "/" + t.database, User: url.UserPassword(t.user, t.password)}).String()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	cfg.TLSConfig, cfg.Fallbacks, cfg.ConnectTimeout = tc, nil, 5*time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	var one int
	return conn.QueryRow(ctx, "SELECT 1").Scan(&one)
}

func probeMySQL(ctx context.Context, t target, tc *tls.Config) error {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = t.user, t.password, "tcp", net.JoinHostPort(t.host, strconv.Itoa(t.port)), t.database
	cfg.TLS, cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = tc, 5*time.Second, 5*time.Second, 5*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	var one int
	return db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

func probeRedis(ctx context.Context, t target, tc *tls.Config) error {
	conn, err := (&tls.Dialer{Config: tc, NetDialer: &net.Dialer{Timeout: 5 * time.Second}}).DialContext(ctx, "tcp", net.JoinHostPort(t.host, strconv.Itoa(t.port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	commands := [][]string{}
	if t.user != "" && t.user != "default" {
		commands = append(commands, []string{"AUTH", t.user, t.password})
	} else {
		commands = append(commands, []string{"AUTH", t.password})
	}
	if t.database != "0" {
		commands = append(commands, []string{"SELECT", t.database})
	}
	commands = append(commands, []string{"PING"})
	r := bufio.NewReaderSize(conn, 4096)
	for _, command := range commands {
		if err = writeRESP(conn, command); err != nil {
			return err
		}
		if err = readRESP(r); err != nil {
			if _, rejected := err.(redisError); rejected {
				if command[0] == "AUTH" {
					return authenticationError{}
				}
				return queryError{}
			}
			return err
		}
	}
	return nil
}

type redisError struct{}

func (redisError) Error() string { return "redis rejection" }
func writeRESP(w io.Writer, values []string) error {
	if _, e := fmt.Fprintf(w, "*%d\r\n", len(values)); e != nil {
		return e
	}
	for _, v := range values {
		if len(v) > MaxEnvironment {
			return errors.New("value")
		}
		if _, e := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(v), v); e != nil {
			return e
		}
	}
	return nil
}
func readRESP(r *bufio.Reader) error {
	line, e := r.ReadString('\n')
	if e != nil || len(line) > 4096 || !strings.HasSuffix(line, "\r\n") {
		return errors.New("response")
	}
	if line[0] == '-' {
		return redisError{}
	}
	if line[0] != '+' && line[0] != ':' && line[0] != '$' {
		return errors.New("response")
	}
	return nil
}

type authenticationError struct{}

func (authenticationError) Error() string { return "authentication" }

type queryError struct{}

func (queryError) Error() string { return "query" }

func classify(err error) (string, string, string) {
	var cert *tls.CertificateVerificationError
	var host x509.HostnameError
	var authority x509.UnknownAuthorityError
	var re redisError
	var auth authenticationError
	var query queryError
	var my *mysql.MySQLError
	var pg *pgconn.PgError
	var dns *net.DNSError
	switch {
	case errors.As(err, &cert) || errors.As(err, &host) || errors.As(err, &authority):
		return "certificate", "tls_verification_failed", "TLS could not verify the server hostname and certificate chain."
	case errors.As(err, &dns):
		return "dns", "dns_lookup_failed", "DNS could not resolve a host required by the driver."
	case errors.As(err, &auth):
		return "authentication", "authentication_rejected", "The server rejected the loaded credentials."
	case errors.As(err, &my) && my.Number == 1045:
		return "authentication", "authentication_rejected", "The server rejected the loaded credentials."
	case errors.As(err, &pg) && strings.HasPrefix(pg.Code, "28"):
		return "authentication", "authentication_rejected", "The server rejected the loaded credentials."
	case errors.As(err, &query) || errors.As(err, &re):
		return "query", "read_query_rejected", "The server rejected the minimal read-only query."
	default:
		return "query", "connection_or_query_failed", "The verified connection or minimal read-only query failed. Server details were removed."
	}
}
