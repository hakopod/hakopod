package cluster

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLivePublicTCPSTARTTLS(t *testing.T) {
	if os.Getenv("HAKOPOD_PUBLIC_TCP_TEST") != "1" {
		t.Skip("requires explicit isolated development public TCP acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("public TCP acceptance requires named k3d-hakopod-dev context")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := New(path, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress", PublicTCPPorts: []int32{2587}, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provisionPublicTCPTestPort(t, ctx, c)
	if os.Getenv("HAKOPOD_PUBLIC_TCP_BYO_TEST") == "1" {
		nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 2})
		if err != nil || len(nodes.Items) != 1 || nodes.Continue != "" {
			t.Fatal("BYO acceptance requires one development node", err)
		}
		c.options.DeploymentMode = DeploymentManagedCloud
		c.options.DedicatedPublicTCPNode = nodes.Items[0].Name
		c.options.PublicTCPPorts = nil
		if err := c.ValidatePublicTCPInstallation(ctx); err != nil {
			t.Fatal(err)
		}
		t.Log("Testing dedicated BYO TCP with administrator-provisioned host port and no static port allowlist")
	}
	target := Target{ApplicationID: fmt.Sprintf("smtp-tcp-acceptance-%d", time.Now().UnixNano()), Project: "smtp-acceptance", Environment: "test", OperationID: "tcp-initial", Revision: 1}
	target.Spec, err = spec.Normalize(spec.Application{Name: "smtp", Services: map[string]spec.Service{"mail": {
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 2525, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true,
		Command: []string{"python", "-B", "-c", smtpFixturePython}, PublicTCP: []spec.PublicTCPListener{{Port: 2587, TargetPort: 2525, SourceCIDRs: []string{"0.0.0.0/0"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		cleared := target
		cleared.Previous = &target.Spec
		copy, err := spec.Normalize(target.Spec)
		if err == nil {
			svc := copy.Services["mail"]
			svc.PublicTCP = nil
			copy.Services["mail"] = svc
			cleared.Spec = copy
			if err = c.PreparePublicTCP(cleanup, cleared); err != nil {
				t.Error("remove SMTP listener", err)
			}
		}
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
		claim, e := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(cleanup, publicTCPClaimName(2587), metav1.GetOptions{})
		if e == nil && owned(claim, target) == nil {
			if e = c.kube.CoreV1().ConfigMaps(claim.Namespace).Delete(cleanup, claim.Name, deleteOptions(claim)); e != nil {
				t.Error(e)
			}
		}
	})
	hostname := "smtp.fixture.example.com"
	cert, key := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	certificate, err := c.PutBackendCertificate(ctx, target, "mail", hostname, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["mail"]
	svc.CertificateMounts = []spec.CertificateMount{{Certificate: certificate.Certificate, Hostname: hostname, MountPath: "/smtp-cert"}}
	target.Spec.Services["mail"] = svc
	deploy := func() {
		t.Helper()
		observed, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) })
		if err != nil {
			t.Fatal(err)
		}
		if observed.Status != "healthy" {
			t.Fatalf("successful TCP deployment has unhealthy observation: %+v", observed)
		}
		for name, service := range target.Spec.Services {
			listeners, err := c.ObservePublicTCP(ctx, target, name)
			if err != nil || len(listeners) != len(service.PublicTCP) {
				t.Fatalf("TCP observation mismatch: %+v, %v", listeners, err)
			}
			for _, listener := range listeners {
				if listener.Status != "configured" {
					t.Fatalf("acknowledged TCP route is not configured: %+v", listener)
				}
			}
		}
	}
	deploy()
	cloud, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cloud.ValidatePublicTCPInstallation(ctx); err == nil {
		t.Fatal("managed-cloud startup accepted a live self-hosted listener")
	}
	if _, err = cloud.Deploy(ctx, target, func(Event) { t.Error("managed-cloud deployment reached a mutation stage") }); !errors.Is(err, ErrPublicTCPDisabled) {
		t.Fatalf("managed-cloud accepted live public TCP: %v", err)
	}
	other := target
	other.ApplicationID = "smtp-other-app"
	if err = c.ValidatePublicTCP(ctx, other); err == nil {
		t.Fatal("live reservation allowed a second application")
	}
	private, err := c.kube.CoreV1().Services(Namespace(target.ApplicationID)).Get(ctx, "mail", metav1.GetOptions{})
	if err != nil || private.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatal("backend port stopped being private", err)
	}
	address := publicTCPPortForward(t, ctx, c, path)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("fixture certificate invalid")
	}
	handshake := func() error {
		conn, err := net.DialTimeout("tcp", address, 3*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		client, err := smtp.NewClient(conn, hostname)
		if err != nil {
			return err
		}
		defer client.Close()
		if err = client.StartTLS(&tls.Config{RootCAs: roots, ServerName: hostname, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
		return client.Quit()
	}
	if err = handshake(); err != nil {
		t.Fatal("STARTTLS did not reach backend certificate", err)
	}
	original, err := spec.Normalize(target.Spec)
	if err != nil {
		t.Fatal(err)
	}
	target.Spec.Services["mail"].PublicTCP[0].SourceCIDRs = []string{"192.0.2.0/24"}
	target.Revision++
	target.Previous = &original
	deploy()
	address = publicTCPPortForward(t, ctx, c, path)
	if err = handshake(); err == nil {
		t.Fatal("unlisted source bypassed HAProxy ACL")
	}
	restricted := target.Spec
	target.Spec = original
	target.Previous = &restricted
	target.Revision++
	deploy()
	address = publicTCPPortForward(t, ctx, c, path)
	if err = handshake(); err != nil {
		t.Fatal("rollback did not restore STARTTLS route", err)
	}
	beforeRemoval, err := spec.Normalize(target.Spec)
	if err != nil {
		t.Fatal(err)
	}
	service := target.Spec.Services["mail"]
	service.PublicTCP = nil
	target.Spec.Services["mail"] = service
	target.Previous = &beforeRemoval
	target.Revision++
	deploy()
	if err = handshake(); err == nil {
		t.Fatal("removed listener still accepts SMTP connections")
	}
	if err = cloud.ValidatePublicTCPInstallation(ctx); err != nil {
		t.Fatalf("managed-cloud startup rejected acknowledged listener removal: %v", err)
	}
	t.Log("Managed-cloud refused an active self-hosted listener and accepted the inventory only after acknowledged removal.")
	t.Log("Healthy returned observations and acknowledged listener status verified for initial deployment, source restriction, rollback and listener removal.")
	t.Log("Real HAProxy TCP forwarding, backend STARTTLS certificate/hostname verification, source denial, atomic port conflict and rollback passed. Cloud firewall/NAT and public SMTP delivery remain external acceptance.")
}

const smtpFixturePython = `import socket,ssl,threading
ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain('/smtp-cert/tls.crt','/smtp-cert/tls.key')
def serve(c):
 try:
  c.settimeout(10); c.sendall(b'220 fixture ESMTP\r\n')
  while True:
   line=b''
   while not line.endswith(b'\n'):
    b=c.recv(1)
    if not b: return
    line+=b
    if len(line)>1024: return
   cmd=line.upper()
   if cmd.startswith(b'EHLO'): c.sendall(b'250-fixture\r\n250 STARTTLS\r\n')
   elif cmd.startswith(b'STARTTLS'):
    c.sendall(b'220 Ready\r\n'); c=ctx.wrap_socket(c,server_side=True)
   elif cmd.startswith(b'QUIT'): c.sendall(b'221 Bye\r\n'); return
   else: c.sendall(b'250 OK\r\n')
 except (OSError,ssl.SSLError): pass
 finally: c.close()
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',2525));s.listen(8)
while True:
 c,_=s.accept();threading.Thread(target=serve,args=(c,),daemon=True).start()
`

func provisionPublicTCPTestPort(t *testing.T, ctx context.Context, c *Client) {
	t.Helper()
	api := c.kube.AppsV1().Deployments(c.options.ProxyNamespace)
	dep, err := api.Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil || !c.ownsProxy(dep) {
		t.Fatal("owned ingress unavailable", err)
	}
	idx := -1
	for i, container := range dep.Spec.Template.Spec.Containers {
		if container.Name == "kubernetes-ingress-controller" {
			idx = i
		}
		for _, p := range container.Ports {
			if p.ContainerPort == 2587 || p.HostPort == 2587 {
				t.Fatal("TCP test port already provisioned")
			}
		}
	}
	if idx < 0 {
		t.Fatal("ingress container missing")
	}
	added := corev1.ContainerPort{Name: "hp-tcp-test", ContainerPort: 2587, HostPort: 2587, Protocol: corev1.ProtocolTCP}
	dep.Spec.Template.Spec.Containers[idx].Ports = append(dep.Spec.Template.Spec.Containers[idx].Ports, added)
	if _, err = api.Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		current, e := api.Get(cleanup, c.options.ProxyConfigMap, metav1.GetOptions{})
		if e != nil || !c.ownsProxy(current) {
			t.Error("cannot restore owned ingress", e)
			return
		}
		for i := range current.Spec.Template.Spec.Containers {
			container := &current.Spec.Template.Spec.Containers[i]
			for j, p := range container.Ports {
				if p.Name == added.Name {
					if !reflect.DeepEqual(p, added) {
						t.Error("operator changed test port; preserving it")
						return
					}
					container.Ports = append(container.Ports[:j], container.Ports[j+1:]...)
					break
				}
			}
		}
		if _, e = api.Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
		}
		if e = waitPublicTCPTestIngress(cleanup, c); e != nil {
			t.Error(e)
		}
	})
	svcAPI := c.kube.CoreV1().Services(c.options.ProxyNamespace)
	svc, err := svcAPI.Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil || !c.ownsProxy(svc) {
		t.Fatal(err)
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == 2587 {
			t.Fatal("TCP test Service port already used")
		}
	}
	svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: added.Name, Port: 2587, TargetPort: intstr.FromInt32(2587), Protocol: corev1.ProtocolTCP})
	if _, err = svcAPI.Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		current, e := svcAPI.Get(cleanup, c.options.ProxyConfigMap, metav1.GetOptions{})
		if e != nil || !c.ownsProxy(current) {
			t.Error(e)
			return
		}
		for i, p := range current.Spec.Ports {
			if p.Name == added.Name {
				if p.Port != 2587 || p.TargetPort != intstr.FromInt32(2587) {
					t.Error("operator changed test service port; preserving it")
					return
				}
				current.Spec.Ports = append(current.Spec.Ports[:i], current.Spec.Ports[i+1:]...)
				break
			}
		}
		if _, e = svcAPI.Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
		}
	})
	if err = waitPublicTCPTestIngress(ctx, c); err != nil {
		t.Fatal(err)
	}
}
func waitPublicTCPTestIngress(ctx context.Context, c *Client) error {
	for {
		d, err := c.kube.AppsV1().Deployments(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == *d.Spec.Replicas && d.Status.ReadyReplicas == *d.Spec.Replicas && d.Status.Replicas == *d.Spec.Replicas {
			return nil
		}
		if err = sleepContext(ctx, time.Second); err != nil {
			return err
		}
	}
}
func publicTCPPortForward(t *testing.T, ctx context.Context, c *Client, path string) string {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Running", LabelSelector: "app.kubernetes.io/name=kubernetes-ingress,app.kubernetes.io/instance=" + c.options.ProxyRelease})
	if err != nil || len(pods.Items) != 1 {
		t.Fatal("expected one stable development ingress pod", err)
	}
	forward, stop := context.WithCancel(ctx)
	command := exec.CommandContext(forward, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", c.options.ProxyNamespace, "port-forward", "pod/"+pods.Items[0].Name, ":2587", "--address=127.0.0.1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("port forwarding did not start")
	}
	line := scanner.Text()
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(line, "Forwarding from ") {
		t.Fatal("unexpected port-forward response")
	}
	address := fields[2]
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return address
}
