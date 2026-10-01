// Disposable, explicitly synthetic ClickHouse public endpoint acceptance.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type options struct {
	mode, check, purpose, host, ca, fingerprint, password, address, expectedAddress string
	port, shards, replicas                                                          int
	revoke                                                                          time.Duration
	caBytes                                                                         []byte
}

func main() {
	o := options{}
	flag.StringVar(&o.mode, "mode", "probe", "probe or hold")
	flag.StringVar(&o.check, "check", "verified", "acceptance check")
	flag.StringVar(&o.purpose, "purpose", "native", "native or https")
	flag.StringVar(&o.host, "host", "", "reviewed public hostname")
	flag.StringVar(&o.expectedAddress, "expect-address", "", "optional reviewed IPv4 address required for every DNS result")
	flag.IntVar(&o.port, "port", 0, "public TCP port")
	flag.StringVar(&o.ca, "ca", "", "public CA PEM file")
	flag.StringVar(&o.fingerprint, "expect-fingerprint", "", "optional SHA256 leaf fingerprint")
	flag.DurationVar(&o.revoke, "expect-revocation-within", 0, "require existing session closure within this bound")
	flag.IntVar(&o.shards, "shards", 1, "shards: 1, 2 or 8")
	flag.IntVar(&o.replicas, "replicas", 1, "members per shard: 1, 2 or 6")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if o.mode == "hold" {
		select {
		case <-ctx.Done():
		case <-time.After(24 * time.Hour):
		}
		return
	}
	if err := o.validate(); err != nil {
		fail("invalid probe arguments")
	}
	o.password = os.Getenv("CLICKHOUSE_PASSWORD")
	if len(o.password) != 64 {
		fail("provide a protected hexadecimal CLICKHOUSE_PASSWORD")
	}
	if raw, err := hex.DecodeString(o.password); err != nil || len(raw) != 32 {
		fail("invalid password format")
	}
	file, err := os.Open(o.ca)
	if err != nil {
		fail("public CA file is unavailable")
	}
	o.caBytes, err = readBounded(file, 32<<10)
	_ = file.Close()
	if err != nil {
		fail("public CA file is invalid or oversized")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(o.caBytes) {
		fail("public CA is invalid")
	}
	lookup, stop := context.WithTimeout(ctx, 3*time.Second)
	ips, err := net.DefaultResolver.LookupIPAddr(lookup, o.host)
	stop()
	if err != nil || len(ips) == 0 || len(ips) > 16 {
		fail("DNS resolution failed or exceeded its bound")
	}
	pinned, err := reviewedDNSAddress(ips, o.expectedAddress)
	if err != nil {
		fail("DNS results do not match the reviewed IPv4 address")
	}
	o.address = net.JoinHostPort(pinned, strconv.Itoa(o.port))
	if o.check == "unreachable" {
		step, done := context.WithTimeout(ctx, 4*time.Second)
		defer done()
		conn, err := (&net.Dialer{}).DialContext(step, "tcp", o.address)
		if err == nil {
			config, e := tlsConfig(o, "")
			if e != nil {
				conn.Close()
				fail("CA configuration failed")
			}
			secure := tls.Client(conn, config)
			err = secure.HandshakeContext(step)
			_ = secure.Close()
			if err == nil {
				fail("unlisted source completed verified TLS")
			}
		}
		var dns *net.DNSError
		var certificate *tls.CertificateVerificationError
		if errors.As(err, &dns) || errors.As(err, &certificate) {
			fail("DNS or certificate errors do not prove source refusal")
		}
		var timeout net.Error
		if !closedRemotely(err) && !errors.Is(err, syscall.ECONNREFUSED) && !(errors.As(err, &timeout) && timeout.Timeout()) {
			fail("failure did not prove TCP refusal")
		}
		fmt.Println("PASS unreachable")
		return
	}
	run, done := context.WithTimeout(ctx, 4*time.Minute+o.revoke)
	defer done()
	if err = verified(run, o); err != nil {
		fail("verified ClickHouse query failed")
	}
	if o.revoke > 0 {
		if o.purpose == "native" {
			err = nativeRevocation(run, o)
		} else {
			err = httpsRevocation(run, o)
		}
		if err != nil {
			fail("existing ClickHouse session revocation was not proved")
		}
		fmt.Println("REVOKED existing ClickHouse session closed")
		return
	}
	switch o.check {
	case "verified":
	case "crud", "data-preserved":
		err = dataCheck(run, o)
	default:
		err = negative(run, o)
	}
	if err != nil {
		fail("requested ClickHouse check failed")
	}
	if err = verified(run, o); err != nil {
		fail("successful query after check failed")
	}
	fmt.Println("PASS " + o.check)
}
func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
func reviewedDNSAddress(ips []net.IPAddr, expected string) (string, error) {
	if len(ips) == 0 || len(ips) > 16 {
		return "", errors.New("DNS result bound")
	}
	if expected != "" {
		reviewed, err := netip.ParseAddr(expected)
		if err != nil || !reviewed.Is4() {
			return "", errors.New("reviewed address must be IPv4")
		}
		for _, ip := range ips {
			if ip.Zone != "" || ip.IP.To4() == nil || ip.IP.String() != reviewed.String() {
				return "", errors.New("DNS address changed")
			}
		}
		return reviewed.String(), nil
	}
	for _, ip := range ips {
		if ip.Zone == "" && ip.IP.To4() != nil {
			return ip.IP.String(), nil
		}
	}
	return "", errors.New("no IPv4 address")
}
func (o options) validate() error {
	checks := map[string]bool{"verified": true, "crud": true, "data-preserved": true, "plaintext-rejected": true, "wrong-hostname-rejected": true, "wrong-ca-rejected": true, "bad-password-rejected": true, "unreachable": true}
	if o.mode != "probe" || !checks[o.check] || (o.purpose != "native" && o.purpose != "https") || o.port < 1 || o.port > 65535 || o.ca == "" || o.revoke < 0 || o.revoke > 10*time.Minute || o.revoke > 0 && o.check != "verified" || (o.shards != 1 && o.shards != 2 && o.shards != 8) || (o.replicas != 1 && o.replicas != 2 && o.replicas != 6) {
		return errors.New("arguments")
	}
	if len(o.host) < 1 || len(o.host) > 253 || net.ParseIP(o.host) != nil {
		return errors.New("hostname")
	}
	for _, label := range strings.Split(o.host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("hostname")
			}
		}
	}
	if o.fingerprint != "" {
		raw, err := hex.DecodeString(o.fingerprint)
		if err != nil || len(raw) != 32 {
			return errors.New("fingerprint")
		}
	}
	if o.expectedAddress != "" {
		address, err := netip.ParseAddr(o.expectedAddress)
		if err != nil || !address.Is4() {
			return errors.New("reviewed address")
		}
	}
	return nil
}
func tlsConfig(o options, check string) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(o.caBytes) {
		return nil, errors.New("CA")
	}
	host := o.host
	if check == "wrong-hostname-rejected" {
		host = "wrong-hostname.invalid"
	}
	if check == "wrong-ca-rejected" {
		roots = x509.NewCertPool()
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: roots, NextProtos: []string{"http/1.1"}}
	if o.fingerprint != "" {
		config.VerifyConnection = func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 {
				return errors.New("missing leaf")
			}
			sum := sha256.Sum256(s.PeerCertificates[0].Raw)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), o.fingerprint) {
				return errors.New("leaf changed")
			}
			return nil
		}
	}
	return config, nil
}
func tlsOpen(ctx context.Context, o options, check string) (*tls.Conn, error) {
	config, err := tlsConfig(o, check)
	if err != nil {
		return nil, err
	}
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(step, "tcp", o.address)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, config)
	if err = conn.HandshakeContext(step); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
func verified(ctx context.Context, o options) error {
	out, err := query(ctx, o, "SELECT currentUser(), currentDatabase() FORMAT TSV")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "app\tapp" {
		return errors.New("identity")
	}
	return nil
}
func query(ctx context.Context, o options, sql string) (string, error) {
	if o.purpose == "native" {
		return nativeQuery(ctx, o, sql, "verified")
	}
	conn, err := tlsOpen(ctx, o, "")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	out, code, err := httpsQuery(ctx, conn, o, sql, o.password)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", errors.New("query rejected")
	}
	return out, nil
}
func negative(ctx context.Context, o options) error {
	if o.purpose == "native" {
		_, err := nativeQuery(ctx, o, "SELECT currentUser(), currentDatabase() FORMAT TSV", o.check)
		if err == nil {
			return errors.New("negative succeeded")
		}
		var rejected *nativeRejection
		if !errors.As(err, &rejected) || rejected.check != o.check {
			return errors.New("unclassified native failure")
		}
		return nil
	}
	if o.check == "wrong-hostname-rejected" || o.check == "wrong-ca-rejected" {
		conn, err := tlsOpen(ctx, o, o.check)
		if err == nil {
			conn.Close()
			return errors.New("TLS accepted")
		}
		var host x509.HostnameError
		var ca x509.UnknownAuthorityError
		if o.check == "wrong-hostname-rejected" && errors.As(err, &host) || o.check == "wrong-ca-rejected" && errors.As(err, &ca) {
			return nil
		}
		return errors.New("wrong TLS failure")
	}
	if o.check == "plaintext-rejected" {
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, err := (&net.Dialer{}).DialContext(step, "tcp", o.address)
		if err != nil {
			return errors.New("plaintext TCP failed")
		}
		defer raw.Close()
		_, _, err = httpsQuery(step, raw, o, "SELECT 1", "")
		if closedRemotely(err) {
			return nil
		}
		return errors.New("plaintext not rejected")
	}
	conn, err := tlsOpen(ctx, o, "")
	if err != nil {
		return err
	}
	defer conn.Close()
	_, code, err := httpsQuery(ctx, conn, o, "SELECT 1", "invalid-public-probe-password")
	if err == nil && code == 516 {
		return nil
	}
	return errors.New("authentication not rejected")
}
func unrelatedCA() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic unrelated acceptance CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
