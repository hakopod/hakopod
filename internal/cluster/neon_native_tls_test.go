//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func nativeNeonTestDial(address string) neonNativeTLSDial {
	return func(ctx context.Context) (net.Conn, func(), error) {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, func() {}, err
		}
		return conn, func() { _ = conn.Close() }, nil
	}
}

func TestNativeNeonTLSRequiresTrustedHostnameAndPlaintextRefusal(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	address := strings.TrimPrefix(server.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probeNeonNativeListener(ctx, nativeNeonTestDial(address), "example.com", roots, false, false); err != nil {
		t.Fatalf("verified HTTPS listener failed: %v", err)
	}
	if err := probeNeonNativeListener(ctx, nativeNeonTestDial(address), "invalid-native-probe.invalid", roots, false, false); err == nil {
		t.Fatal("probe admitted the wrong hostname")
	}
	if err := probeNeonNativeListener(ctx, nativeNeonTestDial(address), "example.com", x509.NewCertPool(), false, false); err == nil {
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
	if err := probeNeonNativeListener(ctx, nativeNeonTestDial(listener.Addr().String()), "example.com", roots, false, false); err == nil {
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
			err = probeNeonNativeListener(ctx, nativeNeonTestDial(listener.Addr().String()), "example.com", roots, true, false)
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

func TestNativeNeonTLSRechecksTransportForPlaintextAndClosesStreams(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, replaced := range []bool{false, true} {
		calls, closed := 0, 0
		dial := func(ctx context.Context) (net.Conn, func(), error) {
			calls++
			if replaced && calls == 2 {
				return nil, func() {}, errors.New("member was replaced")
			}
			conn, closeConn, err := nativeNeonTestDial(strings.TrimPrefix(server.URL, "https://"))(ctx)
			return conn, func() { closed++; closeConn() }, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := probeNeonNativeListener(ctx, dial, "example.com", roots, false, false)
		cancel()
		if (err != nil) != replaced || calls != 2 || !replaced && closed != 2 || replaced && closed != 1 {
			t.Fatalf("transport identity/cleanup was not enforced: replaced=%t calls=%d closed=%d error=%v", replaced, calls, closed, err)
		}
	}
}

func TestNativeNeonTLSListenerInventoryIncludesExactComputeContainers(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{Spec: managedplatform.Spec{Neon: &managedplatform.NeonConfig{Pageservers: 2, Safekeepers: 3, ComputeReplicas: 2}}}}
	listeners := neonNativeTLSListeners(request, "owned")
	if len(listeners) != 13 {
		t.Fatalf("incomplete native listener inventory: %d", len(listeners))
	}
	seen := map[string]bool{}
	for _, listener := range listeners {
		key := listener.host + ":" + strconv.Itoa(listener.port)
		if seen[key] || !strings.HasSuffix(listener.host, ".owned.svc") {
			t.Fatal("duplicate or foreign listener")
		}
		seen[key] = true
		if listener.role == "compute" && (listener.container != "compute-tls" || listener.port != 3081 || listener.postgres) {
			t.Fatal("compute control probe missed the TLS container")
		}
		if listener.role == "compute-sql" && (listener.container != "compute" || listener.port != 55433 || !listener.postgres) {
			t.Fatal("compute SQL probe missed the PostgreSQL container")
		}
	}
}

func TestNativeNeonTLSPodRejectsChangedNamespaceWorkloadAndListener(t *testing.T) {
	platformID := strings.Repeat("4", 32)
	controller := true
	image := "example.test/compute@sha256:" + strings.Repeat("a", 64)
	for name, mutate := range map[string]func(*corev1.Namespace, *appsv1.StatefulSet, *corev1.Pod){
		"accepted":            func(*corev1.Namespace, *appsv1.StatefulSet, *corev1.Pod) {},
		"namespace":           func(ns *corev1.Namespace, _ *appsv1.StatefulSet, _ *corev1.Pod) { ns.UID = "replacement" },
		"workload UID":        func(_ *corev1.Namespace, workload *appsv1.StatefulSet, _ *corev1.Pod) { workload.UID = "replacement" },
		"workload generation": func(_ *corev1.Namespace, workload *appsv1.StatefulSet, _ *corev1.Pod) { workload.Generation++ },
		"pod revision": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.Labels["hakopod.io/revision"] = "2"
		},
		"pod owner": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.OwnerReferences[0].UID = "replacement"
		},
		"pod deleting": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			value := metav1.Now()
			pod.DeletionTimestamp = &value
		},
		"container": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.Spec.Containers[0].Name = "foreign"
		},
		"image": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.Spec.Containers[0].Image = "example.test/compute:latest"
		},
		"port": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.Spec.Containers[0].Ports[0].ContainerPort = 3081
		},
		"ambiguous port": func(_ *corev1.Namespace, _ *appsv1.StatefulSet, pod *corev1.Pod) {
			pod.Spec.Containers = append(pod.Spec.Containers, pod.Spec.Containers[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			labels := map[string]string{managedBy: "hakopod", "hakopod.io/managed-platform-id": platformID, "app.kubernetes.io/component": "compute-0", "hakopod.io/revision": "1"}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "managed-platform-" + platformID, UID: "namespace", Labels: labels}}
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "neon-compute-0", Namespace: ns.Name, UID: "workload", Generation: 1, Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "neon-compute-0-0", Namespace: ns.Name, UID: "pod", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: workload.Name, UID: workload.UID, Controller: &controller}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "compute", Image: image, Ports: []corev1.ContainerPort{{Name: "postgres", ContainerPort: 55433}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			claims := map[string]store.PlatformResourceClaim{
				supabaseClaimKey("namespace", ns.Name):         {ResourceID: string(ns.UID), ImmutableGeneration: platformRuntimeGeneration("namespace", ns)},
				supabaseClaimKey("statefulset", workload.Name): {ResourceID: string(workload.UID), ImmutableGeneration: platformRuntimeGeneration("statefulset", workload)},
			}
			expected := ns.DeepCopy()
			mutate(ns, workload, pod)
			client := &Client{kube: fake.NewSimpleClientset([]runtime.Object{ns, workload, pod}...)}
			request := NeonRuntimeRequest{Operation: store.ManagedPlatformOperation{PlatformID: platformID, Revision: 1}, Render: managedplatform.NeonRenderInput{Images: map[string]string{"compute": image}}}
			_, err := client.neonNativeTLSProbePod(context.Background(), request, expected, claims, neonNativeTLSListener{component: "compute-0", container: "compute", port: 55433})
			if (err == nil) != (name == "accepted") {
				t.Fatalf("unexpected owned transport admission: %v", err)
			}
		})
	}
}

func TestNativeNeonComputeSQLRequiresHBARejectionBeforeAuthentication(t *testing.T) {
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{"SFATAL\x00C28000\x00Mpg_hba.conf rejects connection for host \"10.1.2.3\", user \"native_tls_probe\", database \"postgres\", no encryption\x00\x00", true},
		{"SFATAL\x00C28P01\x00Mpassword authentication failed\x00\x00", false},
		{"SFATAL\x00C28000\x00MTLS connection required\x00\x00", false},
		{"SFATAL\x00C08006\x00Mpg_hba.conf rejects connection, no encryption\x00\x00", false},
		{"SFATAL\x00C28000\x00C28P01\x00Mpg_hba.conf rejects connection, no encryption\x00\x00", false},
		{"SFATAL\x00C28000\x00Mpg_hba.conf rejects connection, no encryption\x00", false},
	} {
		if err := verifyNeonPostgresTLSRefusal([]byte(test.body), true); (err == nil) != test.valid {
			t.Fatal("compute SQL plaintext evidence was misclassified")
		}
	}
}
