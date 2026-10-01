package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func clickhousePublicServiceSelector(d database.Resource) map[string]string {
	return map[string]string{"clickhouse.altinity.com/namespace": DatabaseNamespace(d.ID), "clickhouse.altinity.com/app": "chop", "clickhouse.altinity.com/chi": "database", "clickhouse.altinity.com/ready": "yes"}
}

func clickhousePublicServiceOwned(service *corev1.Service, d database.Resource, uid string, route database.PublicEndpointRoute) bool {
	if service == nil || uid == "" || service.UID == "" || service.DeletionTimestamp != nil || service.Namespace != DatabaseNamespace(d.ID) || service.Name != "database" || service.Spec.PublishNotReadyAddresses || len(service.OwnerReferences) != 1 || !reflect.DeepEqual(service.Spec.Selector, clickhousePublicServiceSelector(d)) {
		return false
	}
	owned := false
	for _, owner := range service.OwnerReferences {
		owned = owned || string(owner.UID) == uid && owner.Name == "database" && owner.Kind == "ClickHouseInstallation" && owner.APIVersion == "clickhouse.altinity.com/v1" && owner.Controller != nil && *owner.Controller && owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion
	}
	if !owned {
		return false
	}
	for _, port := range service.Spec.Ports {
		if port.Name == route.BackendPortName && port.Protocol == corev1.ProtocolTCP && port.Port == int32(route.BackendPort) && port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == int32(route.BackendPort) {
			return true
		}
	}
	return false
}

func (c *Client) clickhousePublicEndpointTargets(ctx context.Context, d database.Resource, route database.PublicEndpointRoute) ([]string, error) {
	if d.Spec.Engine != "clickhouse" || !d.Observation.Fresh(time.Now(), d.Revision) || d.Observation.Status != "ready" || len(d.Observation.Members) != d.Spec.Members() || len(d.Observation.Members) == 0 || len(d.Observation.Members) > database.MaxMembers {
		return nil, fmt.Errorf("ClickHouse public endpoint requires a current verified member inventory")
	}
	object, err := c.clickhouseIdentityController(ctx, d)
	if err != nil {
		return nil, err
	}
	if err = clickhouseClientIdentityConfigured(object, d); err != nil {
		return nil, err
	}
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, route.BackendService, metav1.GetOptions{})
	if err != nil || !clickhousePublicServiceOwned(service, d, string(object.GetUID()), route) {
		return nil, fmt.Errorf("ClickHouse public endpoint Service ownership or routing changed")
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, route)
	if err != nil {
		return nil, err
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, err
	}
	sandbox, err := c.clickhouseRuntime(ctx, d, policy)
	if err != nil {
		return nil, err
	}
	observed := map[string]database.Member{}
	for _, member := range d.Observation.Members {
		if !member.Ready || member.Name == "" || member.UID == "" || observed[member.Name].UID != "" {
			return nil, fmt.Errorf("ClickHouse public member inventory is invalid")
		}
		observed[member.Name] = member
	}
	pods, err := c.kube.CoreV1().Pods(service.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String(), Limit: database.MaxMembers + 1, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) != len(observed) {
		return nil, fmt.Errorf("ClickHouse public Service targets changed or exceeded their bound")
	}
	targets := []string{address}
	for _, pod := range pods.Items {
		member, ok := observed[pod.Name]
		ip, parseErr := netip.ParseAddr(pod.Status.PodIP)
		ready := false
		for _, condition := range pod.Status.Conditions {
			ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
		}
		if !ok || member.UID != string(pod.UID) || member.Node != pod.Spec.NodeName || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !ready || pod.Labels[databaseOwner] != d.ID || !c.databasePodOwned(ctx, pod, object.GetUID()) || !databasePodMatches(pod, d) || !databasePodPolicyMatches(pod, policy) || !clickhousePodRuntimeMatches(pod, sandbox) || parseErr != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			return nil, fmt.Errorf("ClickHouse public member identity or readiness changed")
		}
		targets = append(targets, ip.String())
	}
	return targets, nil
}

func (c *Client) reconcileClickHousePublicNames(ctx context.Context, d database.Resource, before func() error) error {
	object, err := c.clickhouseIdentityController(ctx, d)
	if err != nil {
		return err
	}
	// Migration belongs to ordinary maintenance before review. The publication
	// worker may only change the separate client leaf on an already migrated DB.
	if err = clickhouseClientIdentityConfigured(object, d); err != nil {
		return err
	}
	if err = c.prepareClickHouseClientIdentity(ctx, d, before); err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	names, err := clickhouseClientIdentityNames(d)
	if err != nil {
		return err
	}
	_, err = database.VerifyServerCertificate(certificate, ca, names, time.Now())
	return err
}

const clickhousePublicTCPRelay = `set -eu
exec 3<>/dev/tcp/"$1"/"$2"
exec 4<&0
cat <&3 & reader=$!
cat <&4 >&3 & writer=$!
trap 'kill "$reader" "$writer" 2>/dev/null || true; wait "$reader" "$writer" 2>/dev/null || true' EXIT
wait -n "$reader" "$writer" || true`

func (c *Client) clickhousePublicEndpointStream(ctx context.Context, d database.Resource, member database.Member, address string, port int) (net.Conn, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || port != 8443 && port != 9440 {
		return nil, fmt.Errorf("ClickHouse public probe target is invalid")
	}
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil || d.Spec.Engine != "clickhouse" || container != "clickhouse" {
		return nil, fmt.Errorf("ClickHouse public probe member changed")
	}
	command := []string{"bash", "-c", clickhousePublicTCPRelay, "verify-clickhouse-public-transport", ip.String(), strconv.Itoa(port)}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return nil, fmt.Errorf("ClickHouse public probe transport is unavailable")
	}
	return clickhousePublicProbeStream(ctx, func(step context.Context, stream io.ReadWriter) error {
		return executor.StreamWithContext(step, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}), nil
}

func clickhousePublicProbeStream(ctx context.Context, run func(context.Context, io.ReadWriter) error) net.Conn {
	step, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, stream := net.Pipe()
	var once sync.Once
	// Only successful remote completion represents EOF. Transport failure and
	// cancellation close the caller side so a failed bridge cannot prove that
	// the database refused plaintext.
	finish := func(remoteEOF bool) {
		once.Do(func() {
			if !remoteEOF {
				_ = conn.Close()
			}
			_ = stream.Close()
			cancel()
		})
	}
	go func() { err := run(step, stream); finish(err == nil && step.Err() == nil) }()
	go func() { <-step.Done(); finish(false) }()
	return &mongodbStreamConn{Conn: conn, close: func() { finish(false); _ = conn.Close() }}
}

// The pinned client supports tls-sni-override independently of its numeric
// destination. Credentials and CA bytes enter only through bounded stdin.
const clickhousePublicNativeProbe = `set -eu
umask 077
IFS= read -r password
[[ "$password" =~ ^[a-f0-9]{64}$ ]]
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
secure=true
case "$4" in
 verified) ;;
 wrong_password) password=invalid-public-probe-password ;;
 plaintext) secure=false; password=invalid-public-probe-password ;;
 *) exit 1;;
esac
cat > "$work/client.xml" <<EOF
<config><host>$1</host><port>$2</port><tls-sni-override>$3</tls-sni-override><database>app</database><user>app</user><password>$password</password><secure>$secure</secure><connect_timeout>3</connect_timeout><receive_timeout>3</receive_timeout><send_timeout>3</send_timeout><send_logs_level>none</send_logs_level><openSSL><client><caConfig>$work/ca.crt</caConfig><verificationMode>strict</verificationMode><extendedVerification>true</extendedVerification><loadDefaultCAFile>false</loadDefaultCAFile><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></config>
EOF
if clickhouse-client --config-file="$work/client.xml" --query 'SELECT currentUser(), currentDatabase() FORMAT TSV' > "$work/result" 2> "$work/error"; then
 [ "$4" = verified ] || exit 1
 [ "$(cat "$work/result")" = $'app\tapp' ] || exit 1
 printf 'VERIFIED\n'
else
 case "$4" in
  wrong_password) grep -E 'Code: 516\.' "$work/error" >/dev/null; printf 'AUTHENTICATION_REJECTED\n' ;;
  plaintext) grep -E 'Code: (32|210)\.' "$work/error" >/dev/null; grep -Ei 'Connection reset by peer|Attempt to read after eof|Unexpected EOF|Connection closed' "$work/error" >/dev/null; printf 'PLAINTEXT_REJECTED\n' ;;
  *) exit 1;;
 esac
fi`

func (c *Client) verifyClickHousePublicNative(ctx context.Context, d database.Resource, member database.Member, host, address string, password []byte, trust database.PublicTrust) error {
	input := bytes.Join([][]byte{password, []byte(trust.CertificatePEM)}, []byte{'\n'})
	for _, mode := range []string{"verified", "wrong_password", "plaintext", "verified"} {
		output := &databaseBoundedWriter{limit: 128}
		command := []string{"bash", "-c", clickhousePublicNativeProbe, "verify-clickhouse-public-native", address, "9440", host, mode}
		if err := c.DatabaseExec(ctx, d, member, command, bytes.NewReader(input), output); err != nil {
			return fmt.Errorf("ClickHouse public native authentication or plaintext refusal failed")
		}
		want := map[string]string{"verified": "VERIFIED", "wrong_password": "AUTHENTICATION_REJECTED", "plaintext": "PLAINTEXT_REJECTED"}[mode]
		if strings.TrimSpace(string(output.Bytes())) != want {
			return fmt.Errorf("ClickHouse public native probe returned an invalid result")
		}
	}
	return nil
}

func (c *Client) clickhousePublicHTTPRequest(ctx context.Context, d database.Resource, member database.Member, host, address, password string, secure bool) (int, int, string, error) {
	config, err := c.clickhouseTLSConfig(ctx, d, host, false)
	if err != nil {
		return 0, 0, "", err
	}
	transport := &http.Transport{TLSClientConfig: config, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, DialContext: func(dial context.Context, network, requested string) (net.Conn, error) {
		if dial.Err() != nil || network != "tcp" || requested != net.JoinHostPort(host, "8443") {
			return nil, fmt.Errorf("ClickHouse public HTTPS probe changed destination")
		}
		return c.clickhousePublicEndpointStream(ctx, d, member, address, 8443)
	}}
	defer transport.CloseIdleConnections()
	scheme := "https"
	if !secure {
		scheme = "http"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, scheme+"://"+net.JoinHostPort(host, "8443")+"/?database=app&wait_end_of_query=1&max_result_bytes=128&result_overflow_mode=throw", strings.NewReader("SELECT currentUser(), currentDatabase() FORMAT TSV"))
	if err != nil {
		return 0, 0, "", fmt.Errorf("ClickHouse public HTTPS probe is invalid")
	}
	if secure {
		request.SetBasicAuth("app", password)
	}
	response, err := (&http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return 0, 0, "", err
	}
	defer response.Body.Close()
	output, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(output) > 4096 {
		return 0, 0, "", fmt.Errorf("ClickHouse public HTTPS response exceeded its bound")
	}
	code, _ := strconv.Atoi(response.Header.Get("X-ClickHouse-Exception-Code"))
	return response.StatusCode, code, string(output), nil
}

func (c *Client) verifyClickHousePublicHTTPS(ctx context.Context, d database.Resource, member database.Member, host, address string, password []byte) error {
	verified := func() error {
		status, code, output, err := c.clickhousePublicHTTPRequest(ctx, d, member, host, address, string(password), true)
		if err != nil || status != http.StatusOK || code != 0 || strings.TrimSpace(output) != "app\tapp" {
			return fmt.Errorf("ClickHouse public HTTPS authentication failed")
		}
		return nil
	}
	if err := verified(); err != nil {
		return err
	}
	status, code, _, err := c.clickhousePublicHTTPRequest(ctx, d, member, host, address, "invalid-public-probe-password", true)
	if err != nil || status < 400 || status >= 600 || code != 516 {
		return fmt.Errorf("ClickHouse public HTTPS did not prove authentication refusal")
	}
	_, _, _, err = c.clickhousePublicHTTPRequest(ctx, d, member, host, address, "", false)
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("ClickHouse public HTTPS did not prove plaintext refusal")
	}
	return verified()
}

func (c *Client) verifyClickHousePublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || d.Spec.Engine != "clickhouse" {
		return fmt.Errorf("ClickHouse public route is unavailable")
	}
	if _, err = database.NormalizePublicEndpointNames([]string{endpoint.Allocation.Host}); err != nil {
		return err
	}
	targets, err := c.clickhousePublicEndpointTargets(ctx, d, route)
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) != 64 {
		return fmt.Errorf("ClickHouse public probe credentials are unavailable")
	}
	if decoded, err := hex.DecodeString(string(secret.Data["password"])); err != nil || len(decoded) != 32 {
		return fmt.Errorf("ClickHouse public probe credentials are invalid")
	}
	trust, _, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(2)
	for _, address := range targets {
		group.Go(func() error {
			for _, port := range []int{9440, 8443} {
				config, err := c.clickhouseTLSConfig(step, d, endpoint.Allocation.Host, false)
				if err != nil {
					return err
				}
				wait, cancel := context.WithTimeout(step, 5*time.Second)
				raw, err := c.clickhousePublicEndpointStream(wait, d, d.Observation.Members[0], address, port)
				if err != nil {
					cancel()
					return err
				}
				connection := tls.Client(raw, config)
				err = connection.HandshakeContext(wait)
				_ = connection.Close()
				cancel()
				if err != nil {
					return fmt.Errorf("ClickHouse public hostname or current served certificate verification failed")
				}
			}
			if route.Protocol == "clickhouse_native" {
				return c.verifyClickHousePublicNative(step, d, d.Observation.Members[0], endpoint.Allocation.Host, address, secret.Data["password"], trust)
			}
			return c.verifyClickHousePublicHTTPS(step, d, d.Observation.Members[0], endpoint.Allocation.Host, address, secret.Data["password"])
		})
	}
	return group.Wait()
}
