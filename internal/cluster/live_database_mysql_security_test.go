package cluster

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testMySQLCertificateRefusal(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, password []byte) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Unrelated development issuer"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, issuer, issuer, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	wrongCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	// A successful native connection was already observed with the same account.
	// These failures must be certificate errors, not an unreachable server.
	script := `set -eu
IFS= read -r MYSQL_PWD; export MYSQL_PWD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
if mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --execute='SELECT 1' > /dev/null 2> "$work/error"; then exit 1; fi
grep -E 'ERROR 2026.*(SSL|TLS)' "$work/error" >/dev/null
`
	for _, target := range []struct {
		name, host, ca string
		port           int
	}{
		{"untrusted issuer", o.Endpoints[0].Host, wrongCA, o.Endpoints[0].Port},
		{"hostname mismatch", "127.0.0.1", trust.CertificatePEM, 3306},
	} {
		input := bytes.Join([][]byte{password, []byte(target.ca)}, []byte{'\n'})
		if err := c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", script, "mysql-certificate-refusal", target.host, strconv.Itoa(target.port)}, bytes.NewReader(input), io.Discard); err != nil {
			t.Fatalf("MySQL did not prove rejection of %s", target.name)
		}
	}
	t.Log("MySQL native client rejected an unrelated issuer and an incorrect endpoint hostname")
}

func testMySQLCredentialLogs(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" || namespace.CreationTimestamp.IsZero() {
		t.Fatal("MySQL log audit fixture namespace identity is unavailable")
	}
	// The shared operator may have months of unrelated logs. Inspect the whole
	// fixture lifetime from its namespace creation, including every bootstrap
	// and fault, without silently truncating that interval.
	since := namespace.CreationTimestamp
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 40})
	if err != nil || secrets.Continue != "" {
		t.Fatal("MySQL log audit credential inventory unavailable")
	}
	var private [][]byte
	for _, secret := range secrets.Items {
		for key, value := range secret.Data {
			if len(value) >= 16 && (strings.Contains(strings.ToLower(key), "password") || strings.HasSuffix(key, ".key")) {
				private = append(private, value)
			}
		}
	}
	if len(private) < 4 {
		t.Fatal("MySQL log audit did not load expected private material")
	}
	audit := func(namespace, selector string) {
		pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 20})
		if err != nil || pods.Continue != "" || len(pods.Items) == 0 {
			t.Fatal("MySQL log audit pod inventory unavailable")
		}
		for _, pod := range pods.Items {
			if mysqlAuditPodEndedBefore(pod, since) {
				continue
			}
			for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
				status, found := mysqlAuditContainerStatus(pod, container.Name)
				if !found {
					t.Fatalf("MySQL log audit container history is unavailable for %s/%s", pod.Name, container.Name)
				}
				streams, err := mysqlAuditContainerStreams(status, since)
				if err != nil {
					t.Fatalf("MySQL log audit history is incomplete for %s/%s: %s", pod.Name, container.Name, err)
				}
				for _, previous := range streams {
					limit := int64(4 << 20)
					data, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, LimitBytes: &limit, SinceTime: &since, Previous: previous}).DoRaw(ctx)
					if err != nil {
						t.Fatalf("MySQL log audit failed for %s/%s: Kubernetes reason %s", pod.Name, container.Name, apierrors.ReasonForError(err))
					}
					if len(data) >= int(limit) {
						t.Fatalf("MySQL log audit exceeded its complete-log bound for %s/%s", pod.Name, container.Name)
					}
					for _, value := range private {
						if bytes.Contains(data, value) {
							t.Fatalf("MySQL private material appeared in %s/%s logs", pod.Name, container.Name)
						}
					}
				}
				latest, err := c.kube.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
				if err != nil || latest.UID != pod.UID {
					t.Fatal("MySQL log audit pod identity changed during inspection")
				}
				latestStatus, found := mysqlAuditContainerStatus(*latest, container.Name)
				if !found || !reflect.DeepEqual(status, latestStatus) {
					t.Fatal("MySQL log audit container restarted or changed during inspection")
				}
			}
		}
	}
	audit(ns, "")
	audit("mysql-operator", "name=mysql-operator")
	latestNamespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil || latestNamespace.UID != namespace.UID || !latestNamespace.CreationTimestamp.Equal(&since) {
		t.Fatal("MySQL log audit fixture namespace changed during inspection")
	}
	t.Log("MySQL member, initialization, Router and controller logs contain none of this fixture's credential or private-key values")
}

func mysqlAuditPodEndedBefore(pod corev1.Pod, since metav1.Time) bool {
	if pod.Status.Phase != corev1.PodFailed || pod.Status.Reason != "Evicted" {
		return false
	}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if status.State.Running != nil {
			return false
		}
		if terminated := status.State.Terminated; terminated != nil && !terminated.FinishedAt.IsZero() && !terminated.FinishedAt.Before(&since) {
			return false
		}
	}
	sandboxStopped := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReadyToStartContainers && condition.Status == corev1.ConditionFalse && !condition.LastTransitionTime.IsZero() && condition.LastTransitionTime.Before(&since) {
			sandboxStopped = true
		}
	}
	if !sandboxStopped {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.DisruptionTarget && condition.Status == corev1.ConditionTrue && condition.Reason == "TerminationByKubelet" && !condition.LastTransitionTime.IsZero() && condition.LastTransitionTime.Before(&since) {
			return true
		}
	}
	return false
}

func mysqlAuditContainerStatus(pod corev1.Pod, name string) (corev1.ContainerStatus, bool) {
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if status.Name == name {
			return status, true
		}
	}
	return corev1.ContainerStatus{}, false
}

func mysqlFixtureOperatorProcesses(t *testing.T, ctx context.Context, c *Client) map[string]int32 {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods("mysql-operator").List(ctx, metav1.ListOptions{LabelSelector: "name=mysql-operator", Limit: 20})
	if err != nil || pods.Continue != "" {
		t.Fatal("MySQL operator process inventory is unavailable")
	}
	processes := make(map[string]int32)
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}
		status, found := mysqlAuditContainerStatus(pod, "mysql-operator")
		if pod.UID == "" || pod.DeletionTimestamp != nil || !found || status.State.Running == nil || status.State.Running.StartedAt.IsZero() || !status.Ready {
			t.Fatal("MySQL operator process is not stable and ready")
		}
		processes[string(pod.UID)] = status.RestartCount
	}
	if len(processes) == 0 {
		t.Fatal("MySQL operator has no ready process")
	}
	return processes
}

// Kubernetes keeps only the current and most recently terminated container's
// logs. Reject a fixture interval that also includes an older lost process.
func mysqlAuditContainerStreams(status corev1.ContainerStatus, since metav1.Time) ([]bool, error) {
	var streams []bool
	var currentStart metav1.Time
	switch {
	case status.State.Running != nil:
		currentStart = status.State.Running.StartedAt
		if currentStart.IsZero() {
			return nil, fmt.Errorf("running process timestamp is unavailable")
		}
		streams = append(streams, false)
	case status.State.Terminated != nil:
		terminated := status.State.Terminated
		if terminated.FinishedAt.IsZero() || terminated.StartedAt.IsZero() {
			return nil, fmt.Errorf("termination timestamps are unavailable")
		}
		if terminated.FinishedAt.Before(&since) {
			return nil, nil
		}
		currentStart = terminated.StartedAt
		streams = append(streams, false)
	case status.State.Waiting != nil:
		if status.RestartCount == 0 && status.LastTerminationState.Terminated == nil {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("container process state is unavailable")
	}
	if status.RestartCount == 0 {
		return streams, nil
	}
	if !currentStart.IsZero() && !currentStart.After(since.Time) {
		return streams, nil
	}
	previous := status.LastTerminationState.Terminated
	if previous == nil || previous.FinishedAt.IsZero() || previous.StartedAt.IsZero() {
		return nil, fmt.Errorf("previous process timestamps are unavailable")
	}
	if previous.FinishedAt.Before(&since) {
		return streams, nil
	}
	if status.RestartCount > 1 && previous.StartedAt.After(since.Time) {
		return nil, fmt.Errorf("more than one terminated process overlaps the fixture")
	}
	return append(streams, true), nil
}

func TestMySQLCredentialLogAuditRetainsOverlappingProcesses(t *testing.T) {
	since := metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	before, after := metav1.NewTime(since.Add(-time.Hour)), metav1.NewTime(since.Add(time.Hour))
	running := func(start metav1.Time, restarts int32, previous *corev1.ContainerStateTerminated) corev1.ContainerStatus {
		return corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: start}}, RestartCount: restarts, LastTerminationState: corev1.ContainerState{Terminated: previous}}
	}
	for _, tc := range []struct {
		name    string
		status  corev1.ContainerStatus
		streams []bool
		invalid bool
	}{
		{"stable operator with old restarts", running(before, 2, nil), []bool{false}, false},
		{"fresh process", running(after, 0, nil), []bool{false}, false},
		{"one fixture restart", running(after, 1, &corev1.ContainerStateTerminated{StartedAt: before, FinishedAt: after}), []bool{false, true}, false},
		{"multiple fixture restarts lost history", running(after, 2, &corev1.ContainerStateTerminated{StartedAt: after, FinishedAt: after}), nil, true},
		{"multiple older restarts with complete fixture coverage", running(after, 2, &corev1.ContainerStateTerminated{StartedAt: before, FinishedAt: after}), []bool{false, true}, false},
		{"old terminated process", corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: before, FinishedAt: before}}, RestartCount: 2}, nil, false},
		{"unavailable restart history", running(after, 1, nil), nil, true},
		{"unknown termination", corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "ContainerStatusUnknown"}}}, nil, true},
		{"unstarted container", corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams, err := mysqlAuditContainerStreams(tc.status, since)
			if (err != nil) != tc.invalid || !reflect.DeepEqual(streams, tc.streams) {
				t.Fatal("credential audit lost fixture logs or accepted unavailable history")
			}
		})
	}
}

func TestMySQLCredentialLogAuditExcludesOnlyProvenOldEvictions(t *testing.T) {
	since := metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	before, after := metav1.NewTime(since.Add(-time.Hour)), metav1.NewTime(since.Add(time.Hour))
	old := corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "ContainerStatusUnknown"}}}}, Conditions: []corev1.PodCondition{
		{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "TerminationByKubelet", LastTransitionTime: before},
		{Type: corev1.PodReadyToStartContainers, Status: corev1.ConditionFalse, LastTransitionTime: before},
	}}}
	if !mysqlAuditPodEndedBefore(old, since) {
		t.Fatal("proven pre-fixture eviction was included")
	}
	for _, change := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Status.Conditions[0].LastTransitionTime = after },
		func(p *corev1.Pod) { p.Status.Conditions[1].LastTransitionTime = after },
		func(p *corev1.Pod) { p.Status.Conditions[1].Status = corev1.ConditionTrue },
		func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Terminated.FinishedAt = after },
		func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning },
		func(p *corev1.Pod) { p.Status.Reason = "Error" },
	} {
		changed := old.DeepCopy()
		change(changed)
		if mysqlAuditPodEndedBefore(*changed, since) {
			t.Fatal("overlapping or unproven eviction was excluded")
		}
	}
}

func testMySQLQuorumLoss(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	if len(o.Members) != 3 {
		t.Fatal("MySQL quorum-loss fixture needs exactly three voting members")
	}
	primary, err := mysqlObservedPrimary(o)
	if err != nil {
		t.Fatal(err)
	}
	// Container PID 1 ignores SIGSTOP sent inside its own PID namespace. Use
	// the development runtime boundary and verify the actual PAUSED state.
	helper := os.Getenv("HAKOPOD_MYSQL_FAULT_HELPER")
	if helper == "" {
		t.Fatal("set HAKOPOD_MYSQL_FAULT_HELPER to the owned development quorum helper")
	}
	fault := func(ctx context.Context, action string, member database.Member) error {
		command := exec.CommandContext(ctx, "python3", helper, action, d.ID, member.Name, member.UID)
		return command.Run()
	}
	var stopped []database.Member
	resume := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, server := range stopped {
			if err := fault(cleanup, "resume", server); err != nil {
				t.Error("MySQL fixture server could not resume", server.Name)
			}
		}
		stopped = nil
	}
	defer resume()
	for _, member := range o.Members {
		if member.Role != "replica" {
			continue
		}
		stopped = append(stopped, member)
		if err := fault(ctx, "pause", member); err != nil {
			t.Fatal("MySQL fixture server pause failed")
		}
	}
	if len(stopped) != 2 {
		t.Fatal("MySQL fixture has no replica majority")
	}
	// The primary must still answer a local query, but cannot acknowledge a
	// commit without a majority. Timeout means an unknown outcome, not rollback.
	if err := c.DatabaseExec(ctx, d, primary, mysqlLocalCommand("SELECT 1"), nil, io.Discard); err != nil {
		t.Fatal("MySQL primary itself became unreachable")
	}
	script := `set -eu
work=$(mktemp); trap 'rm -f "$work"' EXIT
set +e
timeout 12 mysql --no-defaults --protocol=SOCKET --socket=/var/run/mysqld/mysql.sock --user=localroot --database=app --execute='INSERT INTO scale_check VALUES(99,0x01)' > /dev/null 2> "$work"
status=$?
set -e
printf 'MySQL quorum probe exit status: %s\n' "$status"
if [ "$status" = 124 ]; then exit 0; fi
if [ "$status" != 0 ] && grep -E 'ERROR (1290|3100|3098)' "$work" >/dev/null; then exit 0; fi
exit 1`
	output := &databaseBoundedWriter{limit: 256}
	if err := c.DatabaseExec(ctx, d, primary, []string{"sh", "-c", script}, nil, output); err != nil {
		t.Fatal("MySQL primary did not prove refusal of an acknowledged commit without quorum", output.String())
	}
	resume()
	t.Log("MySQL primary stayed responsive but could not acknowledge a write while both other voting servers were paused")
}
