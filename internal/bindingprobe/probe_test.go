package bindingprobe

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func testCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return pair, path
}

func redisFixture(t *testing.T, pair tls.Certificate, accept bool) int {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				r := bufio.NewReader(c)
				for {
					first, e := r.ReadString('\n')
					if e != nil {
						return
					}
					count, e := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(first, "*"), "\r\n"))
					if e != nil || count < 1 || count > 4 {
						return
					}
					values := make([]string, 0, count)
					for i := 0; i < count; i++ {
						line, e := r.ReadString('\n')
						if e != nil || line[0] != '$' {
							return
						}
						size, e := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "$"), "\r\n"))
						if e != nil || size < 0 || size > MaxEnvironment {
							return
						}
						value := make([]byte, size+2)
						if _, e = io.ReadFull(r, value); e != nil {
							return
						}
						values = append(values, string(value[:size]))
					}
					if len(values) == 0 {
						return
					}
					if values[0] == "AUTH" && !accept {
						io.WriteString(c, "-ERR invalid credentials\r\n")
						continue
					}
					io.WriteString(c, "+OK\r\n")
				}
			}(conn)
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestRedisStagesTLSAuthenticationAndReadOnlyCommand(t *testing.T) {
	pair, ca := testCertificate(t)
	for _, tc := range []struct {
		name                string
		accept              bool
		wantStage, wantCode string
	}{{"success", true, "query", "read_query_succeeded"}, {"authentication", false, "authentication", "authentication_rejected"}} {
		t.Run(tc.name, func(t *testing.T) {
			port := redisFixture(t, pair, tc.accept)
			secret := "loaded-password-value"
			value := "rediss://default:" + secret + "@localhost:" + strconv.Itoa(port) + "/0"
			result := Run(context.Background(), Request{SchemaVersion: 1, Protocol: "redis", Variable: "REDIS_URL", CAFile: ca, TimeoutMS: 2000, Nonce: "0123456789abcdef"}, func(name string) (string, bool) { return value, name == "REDIS_URL" })
			last := result.Stages[len(result.Stages)-1]
			if last.Name != tc.wantStage || last.Code != tc.wantCode {
				t.Fatalf("last stage: %#v", last)
			}
			if result.Fingerprint == "" {
				t.Fatal("fingerprint missing")
			}
			mac := hmac.New(sha256.New, []byte("0123456789abcdef"))
			_, _ = mac.Write([]byte(value))
			if result.Fingerprint != fmt.Sprintf("%x", mac.Sum(nil)) {
				t.Fatal("fingerprint does not bind the nonce and loaded value")
			}
			if strings.Contains(fmt.Sprintf("%#v", result), secret) {
				t.Fatal("result leaked credential")
			}
		})
	}
}

func TestDriverErrorsAreMappedWithoutRawDetails(t *testing.T) {
	stage, code, message := classify(&mysql.MySQLError{Number: 1045, Message: "secret server detail"})
	if stage != "authentication" || code != "authentication_rejected" || strings.Contains(message, "secret") {
		t.Fatalf("unsafe classification: %s %s %q", stage, code, message)
	}
}

func TestConfigurationFailureDoesNotEchoLoadedValue(t *testing.T) {
	secret := "postgres://user:do-not-leak@host/not-valid-for-redis"
	result := Run(context.Background(), Request{SchemaVersion: 1, Protocol: "redis", Variable: "DATABASE_URL", CAFile: "/missing"}, func(string) (string, bool) { return secret, true })
	if got := fmt.Sprintf("%#v", result); strings.Contains(got, "do-not-leak") {
		t.Fatal("result leaked loaded value")
	}
	if result.Stages[0].Code != "connection_value_invalid" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCertificateHostnameFailureIsSanitized(t *testing.T) {
	pair, ca := testCertificate(t)
	port := redisFixture(t, pair, true)
	value := "rediss://default:secret@127.0.0.1:" + strconv.Itoa(port) + "/0"
	result := Run(context.Background(), Request{SchemaVersion: 1, Protocol: "redis", Variable: "REDIS_URL", CAFile: ca, TimeoutMS: 2000}, func(string) (string, bool) { return value, true })
	last := result.Stages[len(result.Stages)-1]
	if last.Name != "certificate" || last.Code != "tls_verification_failed" {
		t.Fatalf("certificate result: %#v", last)
	}
	if strings.Contains(last.Message, "127.0.0.1") || strings.Contains(last.Message, "x509") {
		t.Fatalf("raw TLS detail leaked: %q", last.Message)
	}
}

func TestRequestBoundsAndAliases(t *testing.T) {
	for _, protocol := range []string{"postgres", "postgresql", "mysql", "vitess", "myduck", "redis", "mongodb", "clickhouse", "oracle"} {
		scheme := map[string]string{"postgres": "postgres", "postgresql": "postgres", "mysql": "mysql", "vitess": "mysql", "myduck": "mysql", "redis": "rediss", "mongodb": "mongodb", "clickhouse": "https", "oracle": "oracle"}[protocol]
		if _, err := parseTarget(protocol, scheme+"://user:password@database.example/app"); err != nil {
			t.Errorf("%s: %v", protocol, err)
		}
	}
	if _, err := parseTarget("redis", "redis://user:password@database.example/0"); err == nil {
		t.Fatal("plaintext Redis accepted")
	}
	if validName("lowercase") || validName(strings.Repeat("A", 129)) {
		t.Fatal("invalid variable accepted")
	}
}

func TestUnavailableProtocolIsExplicit(t *testing.T) {
	for _, protocol := range []string{"mongodb", "clickhouse", "oracle"} {
		scheme := map[string]string{"mongodb": "mongodb", "clickhouse": "https", "oracle": "oracle"}[protocol]
		value := scheme + "://user:password@database.example/app"
		result := Run(context.Background(), Request{SchemaVersion: 1, Protocol: protocol, Variable: "DATABASE_URL", CAFile: "/not-read"}, func(string) (string, bool) { return value, true })
		last := result.Stages[len(result.Stages)-1]
		if last.Status != "unsupported" || last.Code != "protocol_probe_unsupported" {
			t.Fatalf("%s: %#v", protocol, result)
		}
	}
}
