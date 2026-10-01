package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestReviewedDNSAddressPinsOnlyApprovedSet(t *testing.T) {
	approved := net.IPAddr{IP: net.ParseIP("192.0.2.10")}
	wrong := net.IPAddr{IP: net.ParseIP("192.0.2.11")}
	ipv6 := net.IPAddr{IP: net.ParseIP("2001:db8::10")}
	cases := []struct {
		name, expected string
		ips            []net.IPAddr
		want           string
	}{
		{"approved", "192.0.2.10", []net.IPAddr{approved}, "192.0.2.10"},
		{"duplicate approved", "192.0.2.10", []net.IPAddr{approved, approved}, "192.0.2.10"},
		{"wrong only", "192.0.2.10", []net.IPAddr{wrong}, ""},
		{"extra IPv4", "192.0.2.10", []net.IPAddr{approved, wrong}, ""},
		{"extra IPv6", "192.0.2.10", []net.IPAddr{approved, ipv6}, ""},
		{"IPv6 only", "192.0.2.10", []net.IPAddr{ipv6}, ""},
		{"empty", "192.0.2.10", nil, ""},
		{"invalid result", "192.0.2.10", []net.IPAddr{{}}, ""},
		{"zone", "192.0.2.10", []net.IPAddr{{IP: approved.IP, Zone: "eth0"}}, ""},
		{"invalid expectation", "example.test", []net.IPAddr{approved}, ""},
		{"IPv6 expectation", "2001:db8::10", []net.IPAddr{ipv6}, ""},
		{"mapped expectation", "::ffff:192.0.2.10", []net.IPAddr{approved}, ""},
		{"direct example", "", []net.IPAddr{ipv6, approved}, "192.0.2.10"},
		{"direct requires IPv4", "", []net.IPAddr{ipv6}, ""},
		{"bounded inventory", "192.0.2.10", make([]net.IPAddr, 17), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reviewedDNSAddress(tc.ips, tc.expected)
			if tc.want == "" {
				if err == nil || got != "" {
					t.Fatal("unreviewed DNS set accepted", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatal("approved destination was not pinned", got, err)
			}
		})
	}
}

func syntheticServerHello(version uint16) []byte {
	body := make([]byte, 38)
	binary.BigEndian.PutUint16(body[:2], tls.VersionTLS12)
	// Zero session ID, TLS_AES_128_GCM_SHA256, null compression.
	body[35], body[36] = 0x13, 1
	if version == tls.VersionTLS13 {
		body = append(body, 0, 6, 0, 43, 0, 2, 3, 4)
	}
	handshake := append([]byte{2, 0, 0, byte(len(body))}, body...)
	return append([]byte{22, 3, 3, 0, byte(len(handshake))}, handshake...)
}
func TestNativeTLSVersionEvidenceHandlesFragmentedWrites(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		wire := syntheticServerHello(version)
		for split := 0; split <= len(wire); split++ {
			var output bytes.Buffer
			got := make(chan uint16, 1)
			tap := &serverHelloTap{destination: &output, version: got}
			if _, err := tap.Write(wire[:split]); err != nil {
				t.Fatal(err)
			}
			if _, err := tap.Write(wire[split:]); err != nil {
				t.Fatal(err)
			}
			select {
			case actual := <-got:
				if actual != version {
					t.Fatal("wrong negotiated TLS", actual)
				}
			default:
				t.Fatal("fragmented ServerHello lost version", split)
			}
			if !bytes.Equal(output.Bytes(), wire) {
				t.Fatal("relay modified TLS bytes")
			}
		}
	}
}
func TestNativeTLSVersionEvidenceRejectsMalformedInput(t *testing.T) {
	for _, wire := range [][]byte{{23, 3, 3, 0, 1, 0}, {22, 3, 3, 255, 255}, append([]byte{22, 3, 3, 0, 4}, 1, 0, 0, 0)} {
		if version, complete := parseServerHello(wire); !complete || version != 0 {
			t.Fatal("invalid handshake accepted", version, complete)
		}
	}
	var output bytes.Buffer
	tap := &serverHelloTap{destination: &output, version: make(chan uint16, 1)}
	if _, err := tap.Write(make([]byte, 65537)); err != nil {
		t.Fatal(err)
	}
	if !tap.done || tap.pending != nil {
		t.Fatal("handshake capture exceeded bound")
	}
}
func TestNativeNegativeClassificationDoesNotAcceptUnrelatedFailures(t *testing.T) {
	cases := []struct {
		check, text string
		want        bool
	}{
		{"bad-password-rejected", "Code: 516. Authentication failed", true},
		{"plaintext-rejected", "Code: 210. Connection reset by peer", true},
		{"plaintext-rejected", "Code: 210. Connection timed out", false},
		{"plaintext-rejected", "Code: 516. Authentication failed", false},
		{"wrong-hostname-rejected", "Code: 210. SSL certificate verification failed", true},
		{"wrong-ca-rejected", "Code: 210. SSL certificate verify failed", true},
		{"wrong-ca-rejected", "Code: 210. DNS resolution failed", false},
		{"wrong-hostname-rejected", "Code: 516. Authentication failed", false},
	}
	for _, tc := range cases {
		if got := nativeFailure(tc.check, tc.text); got != tc.want {
			t.Fatal("wrong classification", tc.check, tc.text, got)
		}
	}
}
func TestHTTPSRejectsRedirectsAndOversizedHeaders(t *testing.T) {
	for _, response := range []string{"HTTP/1.1 302 Found\r\nLocation: https://elsewhere.invalid/\r\nContent-Length: 0\r\n\r\n", "HTTP/1.1 200 OK\r\nX-Large: " + string(bytes.Repeat([]byte("x"), 100<<10)) + "\r\n\r\n"} {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, request.Body)
			_ = request.Body.Close()
			_, _ = io.WriteString(server, response)
		}()
		_, _, err := httpsQuery(context.Background(), client, options{host: "database.example.test", port: 8443}, "SELECT 1", "")
		client.Close()
		if err == nil || closedRemotely(err) {
			t.Fatal("HTTP policy failure counted as remote refusal", err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server remained blocked")
		}
	}
}
func TestRemoteClosureExcludesCancellationAndTimeout(t *testing.T) {
	if closedRemotely(context.DeadlineExceeded) || closedRemotely(context.Canceled) || closedRemotely(net.ErrClosed) {
		t.Fatal("local failure impersonated remote closure")
	}
	if !closedRemotely(io.EOF) || !closedRemotely(io.ErrUnexpectedEOF) {
		t.Fatal("remote EOF not recognized")
	}
	b := &boundedBuffer{limit: 3}
	if _, err := b.Write([]byte("1234")); err == nil {
		t.Fatal("unbounded child output")
	}
	if _, err := readBounded(bytes.NewReader([]byte("1234")), 3); err == nil || errors.Is(err, io.EOF) {
		t.Fatal("size bound missing")
	}
}

type rejectedWriter struct{}

func (rejectedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestNativeRelayDoesNotConfuseDownstreamFailureWithRemoteClosure(t *testing.T) {
	closed := false
	remote := &remoteClosureReader{Reader: bytes.NewReader([]byte("still connected")), closed: func() { closed = true }}
	if _, err := io.Copy(rejectedWriter{}, remote); err == nil {
		t.Fatal("downstream failure missing")
	}
	if closed {
		t.Fatal("local client failure impersonated remote database closure")
	}
	if _, err := io.Copy(io.Discard, remote); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("actual upstream EOF was not observed")
	}
}
