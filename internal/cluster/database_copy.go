package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Database copy streams data from a database reachable over the internet
// (Railway TCP proxy, Neon, Upstash) into a new, empty managed database. The
// copy runs as a Job in the target database's namespace, so it reaches the
// database through the existing same-namespace rule and reads the target
// password from database-credentials without copying it. The pod carries the
// recovery-helper label, which keeps it out of the database's own member
// lists, placement and network policy; its own policy allows egress only to
// DNS, the database pods and the one validated public source address.

const (
	databaseCopyLabel     = "hakopod.io/database-copy"
	databaseCopyApp       = "hakopod.io/migration-application"
	databaseCopyDeadline  = 6 * 60 * 60
	databaseCopyRetention = 60 * 60
)

// databaseCopyLookup resolves the source host. Tests replace it.
var databaseCopyLookup = net.DefaultResolver.LookupNetIP

// CopyRequest names the target managed database and the source to copy from.
// The engine is the target's: Database.Spec.Engine (postgresql or redis).
type CopyRequest struct {
	// ApplicationID is the application being migrated; it is recorded on the Job.
	ApplicationID string
	// Database and Observation are the target, which must be ready and empty
	// (check with DatabaseEmpty first).
	Database    database.Resource
	Observation database.Observation
	// SourceURL is a postgres(ql):// or redis(s):// URL with credentials. It is
	// stored only in the copy's Secret, which is deleted with the Job.
	SourceURL string
	// SourceVersion is the source PostgreSQL major version, e.g. "16", when known.
	SourceVersion string
}

// CopyID identifies one copy: "<database id>/<job name>".
type CopyID string

type CopyStatus struct {
	Phase string `json:"phase"` // pending, running, succeeded, failed
	// Keys is the number of Redis keys copied, once a Redis copy succeeds.
	Keys int64 `json:"keys,omitempty"`
	// Error is a short summary with credentials removed.
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type copySource struct {
	host     string
	addr     netip.Addr
	port     int
	user     string
	password string
	tls      bool
	url      string // PostgreSQL only: the rebuilt URL with allowed parameters
}

// libpq parameters kept from the source URL. host, hostaddr, port, service and
// file paths are dropped so the URL cannot redirect the copy elsewhere.
var copyPostgresParams = map[string]bool{"sslmode": true, "sslnegotiation": true, "channel_binding": true, "connect_timeout": true, "application_name": true}

// parseCopySource validates the URL and resolves its host. Every resolved
// address must be public; the first is pinned in the pod's /etc/hosts and is
// the only address the network policy allows.
func parseCopySource(ctx context.Context, engine, raw string) (copySource, error) {
	var s copySource
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return s, fmt.Errorf("source database URL is invalid")
	}
	switch {
	case engine == "postgresql" && (u.Scheme == "postgres" || u.Scheme == "postgresql"):
	case engine == "redis" && u.Scheme == "redis":
	case engine == "redis" && u.Scheme == "rediss":
		s.tls = true
	default:
		return s, fmt.Errorf("source database URL scheme does not match the %s target", engine)
	}
	s.host = u.Hostname()
	if s.host == "" || strings.ContainsAny(s.host, ",/") {
		return s, fmt.Errorf("source database URL must name one host")
	}
	s.port = 5432
	if engine == "redis" {
		s.port = 6379
	}
	if p := u.Port(); p != "" {
		if s.port, err = strconv.Atoi(p); err != nil || s.port < 1 || s.port > 65535 {
			return s, fmt.Errorf("source database port is invalid")
		}
	}
	s.user = u.User.Username()
	s.password, _ = u.User.Password()
	addrs := []netip.Addr{}
	if ip, err := netip.ParseAddr(s.host); err == nil {
		addrs = append(addrs, ip)
	} else {
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		addrs, err = databaseCopyLookup(lookup, "ip", s.host)
		cancel()
		if err != nil || len(addrs) == 0 {
			return s, fmt.Errorf("source database host %s did not resolve", s.host)
		}
	}
	for _, ip := range addrs {
		if !backup.PublicIP(ip) {
			return s, fmt.Errorf("source database host %s must resolve only to public addresses", s.host)
		}
	}
	s.addr = addrs[0].Unmap()
	if engine == "postgresql" {
		query := url.Values{}
		for key, values := range u.Query() {
			if copyPostgresParams[key] {
				query[key] = values
			}
		}
		u.RawQuery = query.Encode()
		u.Fragment = ""
		s.url = u.String()
	}
	return s, nil
}

// copyPostgresImage picks the client tools. pg_dump must be at least the source
// server's major, and a dump restores only into the same or a newer server, so
// the target's major (the newer of the two) is the only correct choice. The
// image is the pinned CloudNativePG operand image the target already runs.
func copyPostgresImage(sourceVersion, targetVersion string) (string, error) {
	image, ok := databaseImages["postgresql:"+targetVersion]
	if !ok {
		return "", fmt.Errorf("PostgreSQL %s client tools are not available", targetVersion)
	}
	if sourceVersion == "" {
		return image, nil
	}
	major, _, _ := strings.Cut(sourceVersion, ".")
	source, err := strconv.Atoi(major)
	target, _ := strconv.Atoi(targetVersion)
	if err != nil || source < 1 {
		return "", fmt.Errorf("source PostgreSQL version %q is invalid", sourceVersion)
	}
	if source > target {
		return "", fmt.Errorf("source PostgreSQL %d is newer than the target PostgreSQL %d; create the database with version %d or later", source, target, source)
	}
	return image, nil
}

// The dump is a consistent snapshot; restoring it in one transaction leaves the
// target empty if anything fails, so the copy can simply be started again.
const copyPostgresScript = `set -euo pipefail
umask 077
trap 'rc=$?; [ "$rc" = 0 ] || { tail -n 5 /tmp/dump.err; tail -n 5 /tmp/restore.err; } 2>/dev/null | sed -E "s#://[^@/ ]*@#://***@#g; s#(password=)[^ ]*#\1***#g" | tail -c 2000 >/dev/termination-log' EXIT
tls=()
if [ -n "${TARGET_CA:-}" ]; then
  printf '%s' "$TARGET_CA" >/tmp/ca.crt
  tls=(PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/ca.crt)
fi
pg_dump --dbname="$SOURCE_URL" --format=custom --compress=0 --no-owner --no-privileges --no-subscriptions 2>/tmp/dump.err |
  env PGPASSWORD="$TARGET_PASSWORD" "${tls[@]}" pg_restore --host="$TARGET_HOST" --port="$TARGET_PORT" --username="$TARGET_USER" --dbname="$TARGET_DATABASE" --no-password --no-owner --no-privileges --exit-on-error --single-transaction 2>/tmp/restore.err
`

// Redis copies key by key with DUMP and RESTORE, which every source supports:
// Upstash has no SYNC, so redis-cli --rdb cannot snapshot it. redis-cli's
// quoted output (--no-raw) and quoted input round-trip binary keys and values
// exactly. One EVAL reads 32 keys with their remaining TTL per round trip; the
// target is in-cluster, so one RESTORE per round trip is cheap. REPLACE makes
// keys SCAN returns twice harmless; the caller checks the target is empty.
const copyRedisScript = `set -euo pipefail
umask 077
trap 'rc=$?; [ "$rc" = 0 ] || tail -n 10 /tmp/copy.err 2>/dev/null | tail -c 2000 >/dev/termination-log' EXIT
: >/tmp/copy.err
src=(-h "$SOURCE_HOST" -p "$SOURCE_PORT")
[ -z "${SOURCE_USER:-}" ] || src+=(--user "$SOURCE_USER")
[ "$SOURCE_TLS" != true ] || src+=(--tls --sni "$SOURCE_HOST" --cacert /etc/ssl/certs/ca-certificates.crt)
dst=(-h "$TARGET_HOST" -p "$TARGET_PORT" --user default)
if [ -n "${TARGET_CA:-}" ]; then
  printf '%s' "$TARGET_CA" >/tmp/ca.crt
  dst+=(--tls --sni "$TARGET_HOST" --cacert /tmp/ca.crt)
fi
[ "$TARGET_CLUSTER" != true ] || dst+=(-c)
source_cli() { REDISCLI_AUTH="$SOURCE_PASSWORD" redis-cli "${src[@]}" "$@" 2>>/tmp/copy.err; }
target_cli() { REDISCLI_AUTH="$TARGET_PASSWORD" redis-cli "${dst[@]}" "$@" 2>>/tmp/copy.err; }
read_script="local r={} for _,k in ipairs(KEYS) do local d=redis.call('DUMP',k) local t=redis.call('PTTL',k) if d and t~=-2 and t~=0 then r[#r+1]=k r[#r+1]=d r[#r+1]=t end end return r"
[ "$(source_cli PING)" = PONG ] || { echo "source: could not connect or authenticate" >>/tmp/copy.err; exit 1; }
[ "$(target_cli PING)" = PONG ] || { echo "target: could not connect or authenticate" >>/tmp/copy.err; exit 1; }
# Database 0 always; others when INFO lists them (Upstash has only 0).
databases=$({ echo 0; source_cli INFO keyspace | sed -n 's/^db\([0-9][0-9]*\):keys=.*/\1/p' || true; } | sort -un)
total=0
for db in $databases; do
  if [ "$db" != 0 ] && [ "$TARGET_CLUSTER" = true ]; then
    echo "the source uses Redis database $db, but a Redis Cluster target has only database 0" >>/tmp/copy.err
    exit 1
  fi
  copied=$(source_cli -n "$db" --no-raw --scan --count 1000 |
    awk -v script="$read_script" '
      function flush(  i, line) { line = "EVAL \"" script "\" " n; for (i = 1; i <= n; i++) line = line " " keys[i]; print line; n = 0 }
      { keys[++n] = $0; if (n == 32) flush() }
      END { if (n) flush() }' |
    source_cli -n "$db" --no-raw |
    awk '
      function bad() { print "source: " substr($0, 1, 200) >>"/tmp/copy.err"; exit 1 }
      /^\([0-9.]+s\)$/ || $0 == "(empty array)" { next }
      match($0, /^ *[0-9]+\) /) {
        i = substr($0, 1, RLENGTH); gsub(/[ )]/, "", i); v = substr($0, RLENGTH + 1); p = (i - 1) % 3
        if (p == 0) key = v
        else if (p == 1) payload = v
        else {
          if (v !~ /^\(integer\) -?[0-9]+$/) bad()
          ttl = substr(v, 11) + 0; if (ttl < 0) ttl = 0
          print "RESTORE " key " " ttl " " payload " REPLACE"
        }
        next
      }
      { bad() }' |
    target_cli -n "$db" --no-raw |
    awk '
      /^\([0-9.]+s\)$/ || /^-> Redirected to slot / { next }
      $0 == "OK" { n++; next }
      { print "target: " substr($0, 1, 200) >>"/tmp/copy.err"; exit 1 }
      END { print n + 0 }')
  total=$((total + copied))
done
echo "keys=$total" >/dev/termination-log
`

// StartDatabaseCopy validates the source, then creates the copy's Job
// suspended, its Secret and NetworkPolicy (both owned by the Job, so they go
// with it), and only then lets the Job run.
func (c *Client) StartDatabaseCopy(ctx context.Context, req CopyRequest) (CopyID, error) {
	d, o := req.Database, req.Observation
	engine := d.Spec.Engine
	if engine != "postgresql" && engine != "redis" {
		return "", fmt.Errorf("data copy supports PostgreSQL and Redis, not %s", engine)
	}
	if o.Status != "ready" || o.Revision != d.Revision {
		return "", fmt.Errorf("the target database must be ready before copying data into it")
	}
	if validation.IsDNS1123Label(DatabaseNamespace(d.ID)) != nil {
		return "", fmt.Errorf("target database identity is invalid")
	}
	target := database.Endpoint{}
	for _, e := range o.Endpoints {
		if e.Purpose == "read_write" || e.Purpose == "cluster" && target.Host == "" {
			target = e
		}
	}
	if target.Host == "" {
		return "", fmt.Errorf("target database endpoint is unavailable")
	}
	source, err := parseCopySource(ctx, engine, req.SourceURL)
	if err != nil {
		return "", err
	}
	image := databaseImages["redis:8"]
	uid := int64(1000)
	script := copyRedisScript
	if engine == "postgresql" {
		if image, err = copyPostgresImage(req.SourceVersion, d.Spec.Version); err != nil {
			return "", err
		}
		uid, script = 26, copyPostgresScript // the postgres user in CloudNativePG images
	}
	ca := ""
	if d.Spec.TLSRequired() {
		trust, err := c.DatabaseTrust(ctx, d)
		if err != nil {
			return "", err
		}
		ca = trust.CertificatePEM
	}
	runtimePolicy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return "", err
	}

	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	name := "copy-" + hex.EncodeToString(suffix)
	ns := DatabaseNamespace(d.ID)
	labels := databaseLabels(d)
	labels[databaseRecoveryHelper] = "true"
	labels[databaseCopyLabel] = name

	secretEnv := func(key string) corev1.EnvVar {
		return corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}}}
	}
	env := []corev1.EnvVar{
		{Name: "TARGET_HOST", Value: target.Host},
		{Name: "TARGET_PORT", Value: strconv.Itoa(target.Port)},
		{Name: "TARGET_CA", Value: ca},
		{Name: "TARGET_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "database-credentials"}, Key: "password"}}},
	}
	secret := map[string]string{}
	if engine == "postgresql" {
		secret["SOURCE_URL"] = source.url
		env = append(env, secretEnv("SOURCE_URL"), corev1.EnvVar{Name: "TARGET_USER", Value: d.Spec.CredentialUsername()}, corev1.EnvVar{Name: "TARGET_DATABASE", Value: d.Spec.LogicalDatabase()})
	} else {
		secret["SOURCE_PASSWORD"] = source.password
		env = append(env, secretEnv("SOURCE_PASSWORD"),
			corev1.EnvVar{Name: "SOURCE_HOST", Value: source.host},
			corev1.EnvVar{Name: "SOURCE_PORT", Value: strconv.Itoa(source.port)},
			corev1.EnvVar{Name: "SOURCE_USER", Value: source.user},
			corev1.EnvVar{Name: "SOURCE_TLS", Value: strconv.FormatBool(source.tls)},
			corev1.EnvVar{Name: "TARGET_CLUSTER", Value: strconv.FormatBool(d.Spec.Mode == "cluster")})
	}

	tmp := resource.MustParse("1Gi")
	resources := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceEphemeralStorage: tmp}
	requests := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("256Mi")}
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: ptr(false),
		EnableServiceLinks:           ptr(false),
		RestartPolicy:                corev1.RestartPolicyNever,
		SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(uid), RunAsGroup: ptr(uid), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{
			Name: "copy", Image: image, Command: []string{"bash", "-c", script}, Env: env,
			Resources:                corev1.ResourceRequirements{Requests: requests, Limits: resources},
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext:          &corev1.SecurityContext{RunAsNonRoot: ptr(true), AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:             []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}},
		Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmp}}}},
	}
	if source.host != source.addr.String() {
		// Pin the name to the validated address: the pod resolves it again,
		// and DNS must not hand it a different (or private) one.
		pod.HostAliases = []corev1.HostAlias{{IP: source.addr.String(), Hostnames: []string{source.host}}}
	}
	if runtimePolicy != nil {
		applyWorkloadPolicy(&WorkloadPolicy{NodeName: runtimePolicy.NodeName, Pool: runtimePolicy.Pool, RuntimeClass: runtimePolicy.RuntimeClass}, &pod)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: map[string]string{databaseCopyApp: req.ApplicationID}},
		Spec: batchv1.JobSpec{
			Suspend: ptr(true), BackoffLimit: ptr(int32(0)), Completions: ptr(int32(1)), Parallelism: ptr(int32(1)),
			ActiveDeadlineSeconds: ptr(int64(databaseCopyDeadline)), TTLSecondsAfterFinished: ptr(int32(databaseCopyRetention)),
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: pod},
		},
	}
	jobs := c.kube.BatchV1().Jobs(ns)
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("database copy job could not be created: %w", err)
	}
	fail := func(err error) (CopyID, error) {
		c.deleteCopyJob(created)
		return "", err
	}
	owner := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: created.UID, Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	meta := metav1.ObjectMeta{Name: name, Namespace: ns, Labels: databaseLabels(d), OwnerReferences: owner}
	meta.Labels[databaseCopyLabel] = name
	if _, err = c.kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeOpaque, StringData: secret}, metav1.CreateOptions{}); err != nil {
		return fail(fmt.Errorf("database copy secret could not be created: %w", err))
	}
	if _, err = c.kube.NetworkingV1().NetworkPolicies(ns).Create(ctx, copyNetworkPolicy(meta, engine, source), metav1.CreateOptions{}); err != nil {
		return fail(fmt.Errorf("database copy network policy could not be created: %w", err))
	}
	if _, err = jobs.Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"suspend":false}}`), metav1.PatchOptions{}); err != nil {
		return fail(fmt.Errorf("database copy job could not be started: %w", err))
	}
	return CopyID(d.ID + "/" + name), nil
}

func copyNetworkPolicy(meta metav1.ObjectMeta, engine string, source copySource) *networkingv1.NetworkPolicy {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	dns := intstr.FromInt(53)
	targetPort := intstr.FromInt(5432)
	if engine == "redis" {
		targetPort = intstr.FromInt(6379)
	}
	sourcePort := intstr.FromInt(source.port)
	bits := 32
	if source.addr.Is6() {
		bits = 128
	}
	members := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: databaseRecoveryHelper, Operator: metav1.LabelSelectorOpDoesNotExist}}}
	return &networkingv1.NetworkPolicy{ObjectMeta: meta, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{databaseCopyLabel: meta.Name}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &dns, Protocol: &udp}, {Port: &dns, Protocol: &tcp}}},
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: members}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &targetPort, Protocol: &tcp}}},
			{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(source.addr, bits).String()}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &sourcePort, Protocol: &tcp}}},
		},
	}}
}

func (c *Client) copyJob(ctx context.Context, id CopyID) (*batchv1.Job, error) {
	databaseID, name, ok := strings.Cut(string(id), "/")
	if !ok || !strings.HasPrefix(name, "copy-") || validation.IsDNS1123Label(DatabaseNamespace(databaseID)) != nil || validation.IsDNS1123Label(name) != nil {
		return nil, fmt.Errorf("database copy id is invalid")
	}
	job, err := c.kube.BatchV1().Jobs(DatabaseNamespace(databaseID)).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if job.Labels[databaseCopyLabel] != name || job.Labels[databaseOwner] != databaseID {
		return nil, fmt.Errorf("database copy job is not owned by this database")
	}
	return job, nil
}

// DatabaseCopyStatus maps the Job's state. A Job removed after its retention
// period returns a NotFound error.
func (c *Client) DatabaseCopyStatus(ctx context.Context, id CopyID) (CopyStatus, error) {
	job, err := c.copyJob(ctx, id)
	if err != nil {
		return CopyStatus{}, err
	}
	status := CopyStatus{Phase: "pending"}
	if job.Status.StartTime != nil {
		status.StartedAt = &job.Status.StartTime.Time
	}
	switch jobState(job) {
	case "completed":
		status.Phase = "succeeded"
		if job.Status.CompletionTime != nil {
			status.FinishedAt = &job.Status.CompletionTime.Time
		}
		message, _ := c.copyTerminationMessage(ctx, job)
		if keys, ok := strings.CutPrefix(strings.TrimSpace(message), "keys="); ok {
			status.Keys, _ = strconv.ParseInt(keys, 10, 64)
		}
	case "failed":
		status.Phase = "failed"
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				status.FinishedAt = &condition.LastTransitionTime.Time
				if condition.Reason == "DeadlineExceeded" {
					status.Error = "the copy did not finish within 6 hours"
				}
			}
		}
		if status.Error == "" {
			message, secrets := c.copyTerminationMessage(ctx, job)
			status.Error = scrubCopyError(message, secrets...)
		}
		if status.Error == "" {
			status.Error = "the copy failed; no error output was recorded"
		}
	default:
		if job.Status.Ready != nil && *job.Status.Ready > 0 {
			status.Phase = "running"
		}
	}
	return status, nil
}

// copyTerminationMessage returns the copy container's termination message and
// the secret values to scrub from it.
func (c *Client) copyTerminationMessage(ctx context.Context, job *batchv1.Job) (string, []string) {
	secrets := []string{}
	if s, err := c.kube.CoreV1().Secrets(job.Namespace).Get(ctx, job.Name, metav1.GetOptions{}); err == nil {
		for _, v := range s.Data {
			secrets = append(secrets, string(v))
			if u, err := url.Parse(string(v)); err == nil {
				if password, ok := u.User.Password(); ok {
					secrets = append(secrets, password)
				}
			}
		}
	}
	pods, err := c.kube.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: databaseCopyLabel + "=" + job.Name, Limit: 2})
	if err != nil {
		return "", secrets
	}
	for _, pod := range pods.Items {
		for _, s := range pod.Status.ContainerStatuses {
			if s.Name == "copy" && s.State.Terminated != nil {
				return s.State.Terminated.Message, secrets
			}
		}
	}
	return "", secrets
}

var (
	copyURLCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^@/\s]*@`)
	copyPasswordParam  = regexp.MustCompile(`(?i)(password\s*[=:]\s*)\S+`)
)

// scrubCopyError keeps the last lines of tool output, without credentials.
func scrubCopyError(message string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 4 {
			message = strings.ReplaceAll(message, s, "***")
		}
	}
	message = copyURLCredentials.ReplaceAllString(message, "${1}***@")
	message = copyPasswordParam.ReplaceAllString(message, "${1}***")
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	message = strings.TrimSpace(strings.Join(lines, "\n"))
	if len(message) > 1000 {
		message = message[len(message)-1000:]
	}
	return message
}

// CancelDatabaseCopy stops a copy and removes its Job, pod, Secret and network
// policy. Cancelling a finished or removed copy succeeds. A cancelled
// PostgreSQL copy leaves the target empty (one transaction); a cancelled Redis
// copy may leave keys behind, so the target must be recreated before retrying.
func (c *Client) CancelDatabaseCopy(ctx context.Context, id CopyID) error {
	job, err := c.copyJob(ctx, id)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.deleteCopyJob(job)
}

// CleanupDatabaseCopies removes the target database's finished copies. Their
// Jobs also expire an hour after finishing, taking the source credentials.
func (c *Client) CleanupDatabaseCopies(ctx context.Context, databaseID string) error {
	if validation.IsDNS1123Label(DatabaseNamespace(databaseID)) != nil {
		return fmt.Errorf("database id is invalid")
	}
	jobs, err := c.kube.BatchV1().Jobs(DatabaseNamespace(databaseID)).List(ctx, metav1.ListOptions{LabelSelector: databaseCopyLabel + "," + databaseOwner + "=" + databaseID, Limit: 100})
	if err != nil {
		return err
	}
	for i := range jobs.Items {
		if state := jobState(&jobs.Items[i]); state == "completed" || state == "failed" {
			if err := c.deleteCopyJob(&jobs.Items[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteCopyJob deletes the Job in the foreground; garbage collection removes
// its pod, and the Secret and policy it owns.
func (c *Client) deleteCopyJob(job *batchv1.Job) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := c.kube.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &job.UID}, PropagationPolicy: ptr(metav1.DeletePropagationForeground)})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
