//go:build hakopod_native_acceptance && linux

package cluster

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type neonNativeTLSObservation struct {
	ClientVerificationEnforced bool     `json:"client_verification_enforced"`
	ServerVerified             bool     `json:"server_verified"`
	PlaintextRefused           bool     `json:"plaintext_refused"`
	Services                   []string `json:"services"`
	ComputeSQLNames            []string `json:"compute_sql_names"`
}

// These facts concern the contacted listeners and certificate verification.
// Owned Pod streams deliver plaintext to the real listener while preserving
// end-to-end TLS. It does not establish NetworkPolicy enforcement, application
// authentication or mutual TLS; those are separate acceptance observations.
func (c *Client) probeNeonNativeTLS(ctx context.Context, request NeonRuntimeRequest, ns *corev1.Namespace, claims map[string]store.PlatformResourceClaim) (neonNativeTLSObservation, error) {
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
	listeners := neonNativeTLSListeners(request, namespace)
	// The proxy certificate itself is the accepted trust anchor. It is kept
	// inside the server snapshot; no certificate or secret is returned.
	proxyRef := request.Render.Spec.Secrets["proxy-auth"]
	proxyValues := request.SecretSnapshots[proxyRef.Name+"-r"+strconv.FormatInt(proxyRef.Revision, 10)]
	proxyRoots := x509.NewCertPool()
	if !proxyRoots.AppendCertsFromPEM(proxyValues["tls.crt"]) {
		return result, fmt.Errorf("native Neon proxy TLS trust is unavailable")
	}
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
		pod, err := c.neonNativeTLSProbePod(ctx, request, ns, claims, target)
		if err != nil {
			return result, err
		}
		// Both connections recheck the same accepted member. A replacement
		// between the TLS handshake and plaintext probe cannot qualify.
		dial := func(probe context.Context) (net.Conn, func(), error) {
			current, err := c.neonNativeTLSProbePod(probe, request, ns, claims, target)
			if err != nil || current.UID != pod.UID || !reflect.DeepEqual(current.OwnerReferences, pod.OwnerReferences) {
				return nil, func() {}, fmt.Errorf("native Neon TLS member changed before connection")
			}
			return c.platformTLSStream(probe, current, target.port)
		}
		probe, stop := context.WithTimeout(ctx, 12*time.Second)
		err = probeNeonNativeListener(probe, dial, target.host, pool, target.postgres, target.role == "compute-sql")
		stop()
		if err != nil {
			return result, fmt.Errorf("native Neon %s TLS listener was not verified", target.role)
		}
		after, err := c.neonNativeTLSProbePod(ctx, request, ns, claims, target)
		if err != nil || after.UID != pod.UID || !reflect.DeepEqual(after.OwnerReferences, pod.OwnerReferences) {
			return result, fmt.Errorf("native Neon TLS member changed during probe")
		}
		roles[target.role] = true
		if target.role == "compute-sql" {
			result.ComputeSQLNames = append(result.ComputeSQLNames, strings.TrimPrefix(strings.SplitN(target.host, ".", 2)[0], "neon-"))
		}
	}
	for role := range roles {
		result.Services = append(result.Services, role)
	}
	sort.Strings(result.Services)
	result.ClientVerificationEnforced, result.ServerVerified, result.PlaintextRefused = true, true, true
	return result, nil
}

type neonNativeTLSListener struct {
	role, secret, component, container, host string
	port                                     int
	postgres                                 bool
}

func neonNativeTLSListeners(request NeonRuntimeRequest, namespace string) []neonNativeTLSListener {
	listener := func(role, secret, component, container, service string, port int, postgres bool) neonNativeTLSListener {
		return neonNativeTLSListener{role, secret, component, container, service + "." + namespace + ".svc", port, postgres}
	}
	result := []neonNativeTLSListener{
		listener("broker", "broker-auth", "broker", "broker", "neon-broker", 50051, false),
		listener("controller-database", "controller-database-password", "controller-database", "controller-database", "neon-controller-database", 5432, true),
		listener("storage-controller", "controller-auth", "storage-controller", "storage-controller", "neon-storage-controller", 6699, false),
	}
	for i := 0; i < request.Render.Spec.Neon.Pageservers; i++ {
		name := "pageserver-" + strconv.Itoa(i)
		result = append(result, listener("pageserver", "pageserver-auth", name, "pageserver", "neon-"+name, 9898, false))
	}
	for i := 0; i < request.Render.Spec.Neon.Safekeepers; i++ {
		name := "safekeeper-" + strconv.Itoa(i)
		result = append(result, listener("safekeeper", "safekeeper-auth", name, "safekeeper", "neon-"+name, 7676, false))
	}
	for i := 0; i < request.Render.Spec.Neon.ComputeReplicas; i++ {
		name := "compute-" + strconv.Itoa(i)
		result = append(result, listener("compute", "compute-auth", name, "compute-tls", "neon-"+name+"-control", 3081, false))
		result = append(result, listener("compute-sql", "compute-auth", name, "compute", "neon-"+name, 55433, true))
	}
	return append(result, listener("proxy", "proxy-auth", "proxy", "proxy", "neon-proxy", 5432, true))
}

func (c *Client) neonNativeTLSProbePod(ctx context.Context, request NeonRuntimeRequest, ns *corev1.Namespace, claims map[string]store.PlatformResourceClaim, target neonNativeTLSListener) (*corev1.Pod, error) {
	if c == nil || c.kube == nil || ns == nil || ns.UID == "" || ns.Name != "managed-platform-"+request.Operation.PlatformID {
		return nil, fmt.Errorf("native Neon TLS namespace is invalid")
	}
	current, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err != nil || current.UID != ns.UID || current.DeletionTimestamp != nil || current.Labels[managedBy] != "hakopod" || current.Labels["hakopod.io/managed-platform-id"] != request.Operation.PlatformID {
		return nil, fmt.Errorf("native Neon TLS namespace changed")
	}
	if err = verifySupabaseClaimedUID("namespace", current, claims); err != nil {
		return nil, fmt.Errorf("native Neon TLS namespace claim changed")
	}
	pod, err := c.platformTLSProbePod(ctx, request.Operation, current, target.component, claims)
	if err != nil {
		return nil, fmt.Errorf("native Neon TLS workload ownership changed")
	}
	if pod.Labels["hakopod.io/revision"] != strconv.FormatInt(request.Operation.Revision, 10) || len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Controller == nil || !*pod.OwnerReferences[0].Controller {
		return nil, fmt.Errorf("native Neon TLS member revision or owner changed")
	}
	image := request.Render.Images[target.container]
	if managedplatform.ValidateImages(map[string]string{target.container: image}, []string{target.container}) != nil {
		return nil, fmt.Errorf("native Neon TLS container image is not pinned")
	}
	ports := 0
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.ContainerPort != int32(target.port) {
				continue
			}
			if container.Name != target.container || container.Image != image || port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
				return nil, fmt.Errorf("native Neon TLS container or port changed")
			}
			ports++
		}
	}
	if ports != 1 {
		return nil, fmt.Errorf("native Neon TLS listener inventory changed")
	}
	return pod, nil
}

type neonNativeTLSDial func(context.Context) (net.Conn, func(), error)

func probeNeonNativeListener(ctx context.Context, dial neonNativeTLSDial, hostname string, roots *x509.CertPool, postgres, requireHBA bool) error {
	connection, closeConnection, err := dial(ctx)
	if err != nil {
		return err
	}
	defer closeConnection()
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
	// The second stream reaches the same UID-bound listener without a TLS
	// router that could reject plaintext before the provider sees it.
	plain, closePlain, err := dial(ctx)
	if err != nil {
		return err
	}
	defer closePlain()
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
		return verifyNeonPostgresTLSRefusal(body, requireHBA)
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

// A password failure or unreachable service cannot qualify the SQL HBA policy.
// Only a bounded PostgreSQL ErrorResponse with its explicit transport refusal
// qualifies; the response text itself is never returned to callers or logs.
func verifyNeonPostgresTLSRefusal(body []byte, requireHBA bool) error {
	fields := map[byte]string{}
	for len(body) > 1 {
		end := bytes.IndexByte(body[1:], 0)
		if end < 0 || body[0] == 0 {
			return fmt.Errorf("PostgreSQL rejection is malformed")
		}
		if _, duplicate := fields[body[0]]; duplicate {
			return fmt.Errorf("PostgreSQL rejection has duplicate fields")
		}
		fields[body[0]] = string(body[1 : 1+end])
		body = body[2+end:]
	}
	if len(body) != 1 || body[0] != 0 {
		return fmt.Errorf("PostgreSQL rejection is incomplete")
	}
	message := strings.ToLower(fields['M'])
	if requireHBA {
		if fields['C'] != "28000" || !strings.Contains(message, "pg_hba.conf rejects connection") || !strings.Contains(message, "no encryption") {
			return fmt.Errorf("PostgreSQL did not explicitly reject plaintext through HBA")
		}
	} else if !strings.Contains(message, "ssl") && !strings.Contains(message, "tls") && !strings.Contains(message, "insecure") && !strings.Contains(message, "encryption") {
		return fmt.Errorf("PostgreSQL rejection did not require TLS")
	}
	return nil
}
