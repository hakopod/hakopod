package externaldatabase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func nativeFixtureCertificate(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{host}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func nativeFixtureProber(t *testing.T, serve func(net.Conn), roots *x509.CertPool) *Prober {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		serve(conn)
	}()
	p := NewProber()
	p.roots = roots
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}
	return p
}

func TestExternalPostgresNativeTLSQueryAndIdentity(t *testing.T) {
	for _, fault := range []string{"", "hostname", "untrusted", "credentials", "plaintext"} {
		t.Run(fault, func(t *testing.T) {
			s := fixtureSpec()
			s.Engine = "postgresql"
			s.Port = 6432
			cert, roots := nativeFixtureCertificate(t, s.Host)
			if fault == "hostname" {
				cert, roots = nativeFixtureCertificate(t, "other.psdb.cloud")
			}
			if fault == "untrusted" {
				roots = x509.NewCertPool()
			}
			p := nativeFixtureProber(t, func(raw net.Conn) {
				request := make([]byte, 8)
				if _, err := io.ReadFull(raw, request); err != nil {
					return
				}
				if !bytes.Equal(request, []byte{0, 0, 0, 8, 4, 210, 22, 47}) {
					return
				}
				if fault == "plaintext" {
					_, _ = raw.Write([]byte("N"))
					return
				}
				_, _ = raw.Write([]byte("S"))
				conn := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
				if conn.Handshake() != nil {
					return
				}
				backend := pgproto3.NewBackend(conn, conn)
				startup, err := backend.ReceiveStartupMessage()
				if err != nil {
					return
				}
				message, ok := startup.(*pgproto3.StartupMessage)
				if !ok || message.Parameters["user"] != "branch-role" || message.Parameters["database"] != "app" {
					return
				}
				if fault == "credentials" {
					backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "sensitive-provider-error"})
					_ = backend.Flush()
					return
				}
				if backend.SetAuthType(pgproto3.AuthTypeCleartextPassword) != nil {
					return
				}
				backend.Send(&pgproto3.AuthenticationCleartextPassword{})
				if backend.Flush() != nil {
					return
				}
				password, err := backend.Receive()
				if err != nil {
					return
				}
				pw, ok := password.(*pgproto3.PasswordMessage)
				if !ok || pw.Password != "native-fixture-password" {
					return
				}
				backend.Send(&pgproto3.AuthenticationOk{})
				backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
				backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
				backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if backend.Flush() != nil {
					return
				}
				query, err := backend.Receive()
				if err != nil {
					return
				}
				q, ok := query.(*pgproto3.Query)
				if !ok || q.String != "SELECT 1" {
					return
				}
				backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("?column?"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
				backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				_ = backend.Flush()
				_, _ = backend.Receive()
			}, roots)
			o := p.Observe(context.Background(), Resource{Revision: 1, Spec: s}, Credentials{Username: "branch-role", Password: "native-fixture-password"})
			if (o.Status == "ready") != (fault == "") {
				t.Fatalf("fault %q yielded %+v", fault, o)
			}
			if fault == "" && (!o.TLSVerified || !o.QueryVerified || len(o.VerifiedIPs) != 1) {
				t.Fatal("native verification evidence missing")
			}
			if fault != "" && o.Message != ErrUnavailable.Error() {
				t.Fatal("provider error escaped")
			}
		})
	}
}

func mysqlFixturePacket(conn net.Conn, sequence byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), sequence}
	_, err := conn.Write(append(header, payload...))
	return err
}
func mysqlFixtureRead(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if length > 32<<10 {
		return nil, ErrUnavailable
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(conn, payload)
	return payload, err
}

func TestExternalMySQLNativeTLSAuthenticationAndQuery(t *testing.T) {
	for _, fault := range []string{"", "hostname", "credentials", "plaintext"} {
		t.Run(fault, func(t *testing.T) {
			s := fixtureSpec()
			cert, roots := nativeFixtureCertificate(t, s.Host)
			if fault == "hostname" {
				cert, roots = nativeFixtureCertificate(t, "other.psdb.cloud")
			}
			p := nativeFixtureProber(t, func(raw net.Conn) {
				capabilities := uint32(1 | 8 | 512 | 2048 | 32768 | 1<<19)
				if fault == "plaintext" {
					capabilities &^= 2048
				}
				salt := []byte("12345678abcdefghijkl")
				greeting := []byte{10}
				greeting = append(greeting, []byte("8.4.0-fixture\x00")...)
				greeting = append(greeting, 1, 0, 0, 0)
				greeting = append(greeting, salt[:8]...)
				greeting = append(greeting, 0, byte(capabilities), byte(capabilities>>8), 45, 2, 0, byte(capabilities>>16), byte(capabilities>>24), 21)
				greeting = append(greeting, make([]byte, 10)...)
				greeting = append(greeting, salt[8:]...)
				greeting = append(greeting, 0)
				greeting = append(greeting, []byte("mysql_native_password\x00")...)
				if mysqlFixturePacket(raw, 0, greeting) != nil {
					return
				}
				request, err := mysqlFixtureRead(raw)
				if err != nil || len(request) != 32 || binary.LittleEndian.Uint32(request[:4])&2048 == 0 {
					return
				}
				conn := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
				if conn.Handshake() != nil {
					return
				}
				auth, err := mysqlFixtureRead(conn)
				if err != nil || len(auth) < 33 {
					return
				}
				end := bytes.IndexByte(auth[32:], 0)
				if end < 0 || string(auth[32:32+end]) != "branch-user" {
					return
				}
				offset := 33 + end
				if offset >= len(auth) {
					return
				}
				length := int(auth[offset])
				offset++
				if offset+length > len(auth) {
					return
				}
				first := sha1.Sum([]byte("native-fixture-password"))
				second := sha1.Sum(first[:])
				scrambler := sha1.New()
				_, _ = scrambler.Write(salt)
				_, _ = scrambler.Write(second[:])
				mask := scrambler.Sum(nil)
				for i := range mask {
					mask[i] ^= first[i]
				}
				if fault == "credentials" || !bytes.Equal(auth[offset:offset+length], mask) {
					_ = mysqlFixturePacket(conn, 3, append([]byte{255, 0x15, 0x04, '#', '2', '8', '0', '0', '0'}, []byte("sensitive-provider-error")...))
					return
				}
				if mysqlFixturePacket(conn, 3, []byte{0, 0, 0, 2, 0, 0, 0}) != nil {
					return
				}
				query, err := mysqlFixtureRead(conn)
				if err != nil || string(query) != "\x03SELECT 1" {
					return
				}
				_ = mysqlFixturePacket(conn, 1, []byte{1})
				column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 1, '1', 0, 12, 63, 0, 1, 0, 0, 0, 3, 0, 0, 0, 0, 0}
				_ = mysqlFixturePacket(conn, 2, column)
				_ = mysqlFixturePacket(conn, 3, []byte{254, 0, 0, 2, 0})
				_ = mysqlFixturePacket(conn, 4, []byte{1, '1'})
				_ = mysqlFixturePacket(conn, 5, []byte{254, 0, 0, 2, 0})
				_, _ = mysqlFixtureRead(conn)
			}, roots)
			o := p.Observe(context.Background(), Resource{Revision: 1, Spec: s}, Credentials{Username: "branch-user", Password: "native-fixture-password"})
			if (o.Status == "ready") != (fault == "") {
				t.Fatalf("fault %q yielded %+v", fault, o)
			}
			if fault == "" && (!o.TLSVerified || !o.QueryVerified) {
				t.Fatal("native TLS/query evidence missing")
			}
			if fault != "" && o.Message != ErrUnavailable.Error() {
				t.Fatal("provider error escaped")
			}
		})
	}
}
