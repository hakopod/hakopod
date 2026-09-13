package readinessprobe

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func probeCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.example.com"}, DNSNames: []string{"smtp.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
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
	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return pair, path
}

func smtpServer(t *testing.T, pair *tls.Certificate, banner string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				if banner != "" {
					io.WriteString(conn, banner)
					return
				}
				io.WriteString(conn, "220 fixture ready\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch line {
					case "EHLO hakopod-probe.invalid\r\n":
						if pair != nil {
							io.WriteString(conn, "250-fixture\r\n250 STARTTLS\r\n")
						} else {
							io.WriteString(conn, "250 fixture\r\n")
						}
					case "STARTTLS\r\n":
						if pair == nil {
							return
						}
						io.WriteString(conn, "220 ready for TLS\r\n")
						secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{*pair}, MinVersion: tls.VersionTLS12})
						if err := secure.Handshake(); err != nil {
							return
						}
						conn = secure
						reader = bufio.NewReader(conn)
					case "NOOP\r\n":
						io.WriteString(conn, "250 OK\r\n")
					default:
						io.WriteString(conn, "500 forbidden test command\r\n")
						return
					}
				}
			}(conn)
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestSMTPReadinessProtocolAndVerifiedSTARTTLS(t *testing.T) {
	pair, ca := probeCertificate(t)
	plain := smtpServer(t, nil, "")
	secure := smtpServer(t, &pair, "")
	for _, tc := range []struct {
		name, protocol string
		port           int
		host, ca       string
		want           bool
	}{
		{"tcp-listener", "tcp", plain, "", "", true},
		{"smtp-protocol", "smtp", plain, "", "", true},
		{"verified-starttls", "smtp_starttls", secure, "smtp.example.com", ca, true},
		{"missing-starttls", "smtp_starttls", plain, "smtp.example.com", ca, false},
		{"wrong-hostname", "smtp_starttls", secure, "wrong.example.com", ca, false},
		{"untrusted-certificate", "smtp_starttls", secure, "smtp.example.com", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(context.Background(), Config{Protocol: tc.protocol, Port: tc.port, ServerName: tc.host, CAFile: tc.ca, Timeout: time.Second})
			if (err == nil) != tc.want {
				t.Fatalf("readiness: %v", err)
			}
		})
	}
}

func TestHTTPAndSMTPMustBothPass(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
	defer httpServer.Close()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(httpServer.URL, "http://"))
	httpPort, _ := strconv.Atoi(portText)
	port := smtpServer(t, nil, "")
	cfg := Config{Protocol: "smtp", Port: port, HTTPPort: httpPort, HTTPPath: "/health", Timeout: time.Second}
	if err := Check(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	status.Store(503)
	if err := Check(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("ignored HTTP failure: %v", err)
	}
	status.Store(200)
	dead, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	if err := Check(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "listener") {
		t.Fatalf("healthy HTTP hid SMTP failure: %v", err)
	}
	cfg.Port = port
	if err := Check(context.Background(), cfg); err != nil {
		t.Fatalf("listener recovery: %v", err)
	}
}

func TestSMTPReplyAndDeadlineBounds(t *testing.T) {
	for _, reply := range []string{"220 " + strings.Repeat("x", 1<<16) + "\r\n", strings.Repeat("220-fixture\r\n", 33), "500 sensitive server message\r\n"} {
		port := smtpServer(t, nil, reply)
		err := Check(context.Background(), Config{Protocol: "smtp", Port: port, Timeout: time.Second})
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("unbounded or leaking reply: %v", err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			io.Copy(io.Discard, conn)
		}
	}()
	start := time.Now()
	err = Check(context.Background(), Config{Protocol: "smtp", Port: listener.Addr().(*net.TCPAddr).Port, Timeout: time.Second})
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("probe deadline: %v (%s)", err, time.Since(start))
	}
	for _, cfg := range []Config{{Protocol: "smtp", Port: 25, Timeout: time.Hour}, {Protocol: "http", Port: 25, Timeout: time.Second}} {
		if err := Check(context.Background(), cfg); err == nil {
			t.Fatal("unbounded configuration accepted")
		}
	}
}

func TestSMTPReplyParser(t *testing.T) {
	_, err := readReply(bufio.NewReaderSize(strings.NewReader(fmt.Sprintf("220 %s\r\n", strings.Repeat("x", 5000))), 4096), 220)
	if err == nil {
		t.Fatal("oversized reply accepted")
	}
}
