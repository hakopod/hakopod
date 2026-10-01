package externaldatabase

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// Prober limits provider connections across observations and deployment
// snapshots. Its private transport hooks exist only for package tests.
type Prober struct {
	slots  chan struct{}
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	roots  *x509.CertPool
}

func NewProber() *Prober {
	return &Prober{slots: make(chan struct{}, 4), lookup: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
}

var DefaultProber = NewProber()

// PublicAddress excludes private, reserved, translation and metadata ranges.
// The same check guards probes and application egress snapshots.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func (p *Prober) safeDial(ctx context.Context, host string, port int) (net.Conn, []string, error) {
	addresses, err := p.lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		return nil, nil, ErrUnavailable
	}
	verified := make([]string, 0, len(addresses))
	for _, ip := range addresses {
		if !PublicAddress(ip) {
			return nil, nil, ErrUnavailable
		}
		verified = append(verified, ip.Unmap().String())
	}
	// Validate the entire DNS answer, then connect to one checked address.
	// The provider hostname remains the certificate identity, never the IP.
	for _, ip := range addresses {
		conn, err := p.dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(port)))
		if err == nil {
			return conn, verified, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, nil, ErrUnavailable
}

type quietMySQLLog struct{}

func (quietMySQLLog) Print(...any) {}

func (p *Prober) Observe(parent context.Context, r Resource, c Credentials) Observation {
	o := Observation{Revision: r.Revision, Status: "unreachable", Message: ErrUnavailable.Error()}
	ctx, cancel := context.WithTimeout(parent, ProbeTimeout)
	defer cancel()
	if r.Spec.Validate() != nil || c.Validate() != nil {
		o.ObservedAt = time.Now().UTC()
		return o
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		o.ObservedAt = time.Now().UTC()
		return o
	}
	tlsVerified := false
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: r.Spec.Host, RootCAs: p.roots, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 {
			return ErrUnavailable
		}
		tlsVerified = true
		return nil
	}}
	started := time.Now()
	var err error
	var addresses []string
	if r.Spec.Engine == "mysql" {
		addresses, err = p.mysql(ctx, r.Spec, c, config)
	} else {
		addresses, err = p.postgres(ctx, r.Spec, c, config)
	}
	o.ObservedAt = time.Now().UTC()
	if err != nil || !tlsVerified {
		return o
	}
	latency := time.Since(started).Milliseconds()
	o.Status, o.Message, o.TLSVerified, o.QueryVerified, o.LatencyMS = "ready", "A SELECT 1 query completed through a verified TLS connection.", true, true, &latency
	o.VerifiedIPs = addresses
	return o
}

func (p *Prober) mysql(ctx context.Context, s Spec, c Credentials, tlsConfig *tls.Config) ([]string, error) {
	config := mysql.NewConfig()
	config.User, config.Passwd, config.DBName = c.Username, c.Password, s.Database
	config.Net, config.Addr = "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	config.TLS = tlsConfig
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	config.MaxAllowedPacket = 32 << 10
	config.Logger = quietMySQLLog{}
	var addresses []string
	config.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, ips, err := p.safeDial(ctx, s.Host, s.Port)
		addresses = ips
		return conn, err
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, ErrUnavailable
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(ProbeTimeout)
	var one int
	if err = db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		return nil, ErrUnavailable
	}
	return addresses, nil
}

func (p *Prober) postgres(ctx context.Context, s Spec, c Credentials, tlsConfig *tls.Config) ([]string, error) {
	config, err := pgx.ParseConfig("postgresql://")
	if err != nil {
		return nil, ErrUnavailable
	}
	config.Host, config.Port, config.User, config.Password, config.Database = s.Host, uint16(s.Port), c.Username, c.Password, s.Database
	config.TLSConfig, config.Fallbacks = tlsConfig, nil
	config.RuntimeParams = map[string]string{"application_name": "hakopod-connectivity", "statement_timeout": "5000"}
	config.ConnectTimeout = 5 * time.Second
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{s.Host}, nil }
	var addresses []string
	config.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, ips, err := p.safeDial(ctx, s.Host, s.Port)
		addresses = ips
		return conn, err
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var one int
	if err = conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		return nil, ErrUnavailable
	}
	return addresses, nil
}
