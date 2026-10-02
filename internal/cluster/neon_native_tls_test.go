//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeNeonTLSRequiresTrustedHostnameAndPlaintextRefusal(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	address := strings.TrimPrefix(server.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probeNeonNativeListener(ctx, address, "example.com", roots, false); err != nil {
		t.Fatalf("verified HTTPS listener failed: %v", err)
	}
	if err := probeNeonNativeListener(ctx, address, "invalid-native-probe.invalid", roots, false); err == nil {
		t.Fatal("probe admitted the wrong hostname")
	}
	if err := probeNeonNativeListener(ctx, address, "example.com", x509.NewCertPool(), false); err == nil {
		t.Fatal("probe admitted an untrusted certificate")
	}
}

func TestNativeNeonTLSRejectsPlaintextEvenWhenTLSAlsoWorks(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		secured := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		_ = secured.SetDeadline(time.Now().Add(3 * time.Second))
		_ = secured.Handshake()
		defer secured.Close()
		plain, err := listener.Accept()
		if err != nil {
			return
		}
		defer plain.Close()
		_ = plain.SetDeadline(time.Now().Add(3 * time.Second))
		buffer := make([]byte, 256)
		_, _ = plain.Read(buffer)
		_, _ = io.WriteString(plain, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probeNeonNativeListener(ctx, listener.Addr().String(), "example.com", roots, false); err == nil {
		t.Fatal("probe qualified a listener which also serves plaintext")
	}
	_ = listener.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TLS fixture did not stop")
	}
}

func TestNativeNeonPostgresTLSRequiresExplicitPlaintextRejection(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	for _, rejection := range []bool{true, false} {
		t.Run(map[bool]string{true: "TLS required", false: "authentication only"}[rejection], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
				var sslRequest [8]byte
				if _, err = io.ReadFull(connection, sslRequest[:]); err != nil {
					connection.Close()
					return
				}
				if binary.BigEndian.Uint32(sslRequest[4:]) != 80877103 {
					connection.Close()
					return
				}
				_, _ = connection.Write([]byte{'S'})
				secured := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				_ = secured.Handshake()
				defer secured.Close()
				plain, err := listener.Accept()
				if err != nil {
					return
				}
				defer plain.Close()
				_ = plain.SetDeadline(time.Now().Add(3 * time.Second))
				buffer := make([]byte, 256)
				_, _ = plain.Read(buffer)
				text := "authentication failed"
				if rejection {
					text = "TLS connection required"
				}
				body := []byte("SFATAL\x00C28000\x00M" + text + "\x00\x00")
				message := make([]byte, 5, 5+len(body))
				message[0] = 'E'
				binary.BigEndian.PutUint32(message[1:], uint32(4+len(body)))
				_, _ = plain.Write(append(message, body...))
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = probeNeonNativeListener(ctx, listener.Addr().String(), "example.com", roots, true)
			if rejection && err != nil || !rejection && err == nil {
				t.Fatalf("unexpected plaintext classification: %v", err)
			}
			_ = listener.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("PostgreSQL TLS fixture did not stop")
			}
		})
	}
}
