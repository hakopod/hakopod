//go:build hakopod_native_acceptance && linux

package cluster

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type neonNativeTLSObservation struct {
	ClientVerificationEnforced bool     `json:"client_verification_enforced"`
	ServerVerified             bool     `json:"server_verified"`
	PlaintextRefused           bool     `json:"plaintext_refused"`
	Services                   []string `json:"services"`
}

// These facts concern the contacted listeners and certificate verification.
// They do not establish application authentication or mutual TLS.
func probeNeonNativeTLS(ctx context.Context, request NeonRuntimeRequest) (neonNativeTLSObservation, error) {
	var result neonNativeTLSObservation
	namespace := "managed-platform-" + request.Operation.PlatformID
	if request.Render.Spec.Neon == nil || request.Render.Spec.Neon.Pageservers < 2 || request.Render.Spec.Neon.Pageservers > 8 || request.Render.Spec.Neon.Safekeepers != 3 || request.Render.Spec.Neon.ComputeReplicas < 1 || request.Render.Spec.Neon.ComputeReplicas > 6 {
		return result, fmt.Errorf("native Neon TLS topology is invalid")
	}
	roots := func(logical string) (*x509.CertPool, error) {
		ref := request.Render.Spec.Secrets[logical]
		values := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
		pool := x509.NewCertPool()
		if len(values["ca.crt"]) == 0 || !pool.AppendCertsFromPEM(values["ca.crt"]) {
			return nil, fmt.Errorf("native Neon TLS trust is unavailable")
		}
		return pool, nil
	}
	type listener struct {
		role, secret, host, port string
		postgres                 bool
	}
	listeners := []listener{{"storage-controller", "controller-auth", "neon-storage-controller." + namespace + ".svc", "6699", false}}
	for i := 0; i < request.Render.Spec.Neon.Pageservers; i++ {
		listeners = append(listeners, listener{"pageserver", "pageserver-auth", "neon-pageserver-" + strconv.Itoa(i) + "." + namespace + ".svc", "9898", false})
	}
	for i := 0; i < 3; i++ {
		listeners = append(listeners, listener{"safekeeper", "safekeeper-auth", "neon-safekeeper-" + strconv.Itoa(i) + "." + namespace + ".svc", "7676", false})
	}
	for i := 0; i < request.Render.Spec.Neon.ComputeReplicas; i++ {
		listeners = append(listeners, listener{"compute", "compute-auth", "neon-compute-" + strconv.Itoa(i) + "-control." + namespace + ".svc", "3081", false})
	}
	// The proxy certificate itself is the accepted trust anchor. It is kept
	// inside the server snapshot; no certificate or secret is returned.
	proxyRef := request.Render.Spec.Secrets["proxy-auth"]
	proxyValues := request.SecretSnapshots[proxyRef.Name+"-r"+strconv.FormatInt(proxyRef.Revision, 10)]
	proxyRoots := x509.NewCertPool()
	if !proxyRoots.AppendCertsFromPEM(proxyValues["tls.crt"]) {
		return result, fmt.Errorf("native Neon proxy TLS trust is unavailable")
	}
	listeners = append(listeners, listener{"proxy", "proxy-auth", "neon-proxy." + namespace + ".svc", "5432", true})
	roles := map[string]bool{}
	for _, target := range listeners {
		pool := proxyRoots
		if target.secret != "proxy-auth" {
			var err error
			pool, err = roots(target.secret)
			if err != nil {
				return result, err
			}
		}
		if err := probeNeonNativeListener(ctx, net.JoinHostPort(target.host, target.port), target.host, pool, target.postgres); err != nil {
			return result, fmt.Errorf("native Neon %s TLS listener was not verified", target.role)
		}
		roles[target.role] = true
	}
	for role := range roles {
		result.Services = append(result.Services, role)
	}
	sort.Strings(result.Services)
	result.ClientVerificationEnforced, result.ServerVerified, result.PlaintextRefused = true, true, true
	return result, nil
}

func probeNeonNativeListener(ctx context.Context, address, hostname string, roots *x509.CertPool, postgres bool) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := neonNativeConnectionDeadline(ctx)
	if err = connection.SetDeadline(deadline); err != nil {
		return err
	}
	if postgres {
		if _, err = connection.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47}); err != nil {
			return err
		}
		var response [1]byte
		if _, err = io.ReadFull(connection, response[:]); err != nil || response[0] != 'S' {
			return fmt.Errorf("PostgreSQL listener did not accept TLS")
		}
	}
	secured := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: hostname})
	if err = secured.HandshakeContext(ctx); err != nil {
		return err
	}
	state := secured.ConnectionState()
	if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return fmt.Errorf("TLS certificate chain was not verified")
	}
	if state.PeerCertificates[0].VerifyHostname("invalid-native-probe.invalid") == nil {
		return fmt.Errorf("TLS hostname mismatch was accepted")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	if _, err = state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: x509.NewCertPool(), Intermediates: intermediates, DNSName: hostname}); err == nil {
		return fmt.Errorf("TLS untrusted certificate was accepted")
	}
	// Dial the exact reached address again to distinguish plaintext refusal
	// from DNS drift or an unreachable listener.
	plain, err := dialer.DialContext(ctx, "tcp", connection.RemoteAddr().String())
	if err != nil {
		return err
	}
	defer plain.Close()
	deadline = neonNativeConnectionDeadline(ctx)
	if err = plain.SetDeadline(deadline); err != nil {
		return err
	}
	if postgres {
		payload := append([]byte{0, 3, 0, 0}, []byte("user\x00native_tls_probe\x00database\x00postgres\x00\x00")...)
		message := make([]byte, 4, 4+len(payload))
		binary.BigEndian.PutUint32(message, uint32(4+len(payload)))
		if _, err = plain.Write(append(message, payload...)); err != nil {
			return err
		}
		var header [5]byte
		if _, err = io.ReadFull(plain, header[:]); err != nil {
			return err
		}
		length := int(binary.BigEndian.Uint32(header[1:]))
		if header[0] != 'E' || length < 4 || length > 4096 {
			return fmt.Errorf("PostgreSQL plaintext was not explicitly refused")
		}
		body := make([]byte, length-4)
		if _, err = io.ReadFull(plain, body); err != nil {
			return err
		}
		text := strings.ToLower(string(body))
		if !strings.Contains(text, "ssl") && !strings.Contains(text, "tls") && !strings.Contains(text, "insecure") && !strings.Contains(text, "encryption") {
			return fmt.Errorf("PostgreSQL rejection did not require TLS")
		}
		return nil
	}
	if _, err = io.WriteString(plain, "GET / HTTP/1.1\r\nHost: "+hostname+"\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	limited := &io.LimitedReader{R: plain, N: 8193}
	response, err := http.ReadResponse(bufio.NewReader(limited), &http.Request{Method: http.MethodGet})
	if limited.N == 0 {
		return fmt.Errorf("plaintext response headers exceed their bound")
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
			return fmt.Errorf("plaintext refusal timed out")
		}
		// An immediately closed TLS listener cannot serve plaintext HTTP.
		if err == io.EOF || err == io.ErrUnexpectedEOF || strings.Contains(err.Error(), "connection reset") {
			return nil
		}
		return fmt.Errorf("plaintext response could not be classified")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	if readErr != nil || len(body) > 4096 {
		return fmt.Errorf("plaintext rejection response exceeds its bound")
	}
	text := strings.ToLower(string(body))
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(text, "https") && !strings.Contains(text, "ssl") && !strings.Contains(text, "tls") {
		return fmt.Errorf("listener served plaintext HTTP")
	}
	return nil
}

func neonNativeConnectionDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(3 * time.Second)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		return outer
	}
	return deadline
}
