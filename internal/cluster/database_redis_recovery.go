package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const databaseRecoveryHelper = "hakopod.io/database-recovery"

// RestoreRedisDatabase loads verified RDB files in a disposable, network-isolated
// Redis process. The helper owns no credentials or service-account token. Redis
// itself decodes values; only bounded DUMP payloads and absolute expiry cross the
// management connection. The Job deadline and TTL clean up after worker crashes.
func (c *Client) RestoreRedisDatabase(ctx context.Context, d database.Resource, o database.Observation, input io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if d.Spec.Engine != "redis" || d.Recovery == nil || len(d.Recovery.JobID) != 32 {
		return fmt.Errorf("Redis recovery requires an accepted recovery job")
	}
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	helper, cleanup, err := c.redisRecoveryHelper(ctx, d)
	if err != nil {
		return err
	}
	defer cleanup()
	exec := func(command []string, in io.Reader) error { return c.redisRecoveryExec(ctx, d, helper, command, in) }
	files := []string{}
	_, err = database.ReadRedisArchive(input, func(_ database.RedisArchive, _ database.Member, r io.Reader) error {
		name := "shard-" + strconv.Itoa(len(files)) + ".rdb"
		files = append(files, name)
		return exec([]string{"sh", "-c", `set -eu; umask 077; cat > "/work/$1"`, "stage", name}, r)
	})
	if err != nil {
		return err
	}
	for _, name := range files {
		if err = exec([]string{"sh", "-c", `set -eu; ln -sf "$(command -v redis-server)" /work/redis-check-rdb; /work/redis-check-rdb "/work/$1" >/dev/null`, "check", name}, nil); err != nil {
			return fmt.Errorf("Redis rejected a recovered RDB file")
		}
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID {
		return fmt.Errorf("Redis recovery credentials are unavailable")
	}
	connections := map[string]*database.RedisWire{}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for _, member := range o.Members {
		if member.Role != "primary" {
			continue
		}
		// Reuse DatabaseExec's complete namespace, owner, UID and pinned-image checks
		// immediately before opening the private recovery transport.
		if err = c.DatabaseExec(ctx, d, member, []string{"true"}, nil, io.Discard); err != nil {
			return err
		}
		conn, closeForward, err := c.databaseRedisConnection(ctx, DatabaseNamespace(d.ID), member.Name, types.UID(member.UID))
		if err != nil {
			return err
		}
		defer closeForward()
		connections[member.Shard] = conn
		if _, err = conn.Command([]byte("AUTH"), secret.Data["password"]); err != nil {
			return err
		}
	}
	var slots [16384]string
	if d.Spec.Mode == "cluster" {
		raw, err := c.redisCommand(ctx, d, o.Members[0], "CLUSTER NODES")
		if err != nil {
			return err
		}
		nodes, fingerprint, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
		if err != nil || fingerprint != o.TopologyFingerprint {
			return fmt.Errorf("Redis topology changed before recovery")
		}
		for _, node := range nodes {
			if node.Role != "primary" {
				continue
			}
			if connections[node.ID] == nil {
				return fmt.Errorf("Redis primary is not owned by this recovery target")
			}
			for _, span := range node.Slots {
				bounds := strings.Split(span, "-")
				start, _ := strconv.Atoi(bounds[0])
				end := start
				if len(bounds) == 2 {
					end, _ = strconv.Atoi(bounds[1])
				}
				for slot := start; slot <= end; slot++ {
					slots[slot] = node.ID
				}
			}
		}
	}
	// Disconnect application clients before repeating the empty check. Failure
	// leaves this fresh target inaccessible until the operator discards it.
	for _, conn := range connections {
		if _, err = conn.Command([]byte("CLIENT"), []byte("KILL"), []byte("TYPE"), []byte("normal"), []byte("SKIPME"), []byte("yes")); err != nil {
			return err
		}
		for db := 0; db < 16; db++ {
			if d.Spec.Mode == "cluster" && db > 0 {
				break
			}
			if d.Spec.Mode != "cluster" {
				if _, err = conn.Command([]byte("SELECT"), []byte(strconv.Itoa(db))); err != nil {
					return err
				}
			}
			count, err := conn.Command([]byte("DBSIZE"))
			if err != nil || count != int64(0) {
				return fmt.Errorf("Redis recovery target is no longer empty")
			}
		}
	}
	for _, name := range files {
		if err = exec([]string{"sh", "-c", `set -eu; redis-server --bind 127.0.0.1 --port 6379 --dir /work --dbfilename "$1" --save '' --appendonly no --daemonize yes --pidfile /work/redis.pid --logfile /dev/null; i=0; until redis-cli PING >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 120 ]; sleep 1; done`, "load", name}, nil); err != nil {
			return fmt.Errorf("Redis snapshot could not be loaded within the recovery resources")
		}
		source, closeForward, err := c.databaseRedisConnection(ctx, helper.Namespace, helper.Name, helper.UID)
		if err != nil {
			return err
		}
		err = database.CopyRedisSnapshot(ctx, source, func(key []byte, db int) (*database.RedisWire, error) {
			conn := connections[slots[database.RedisKeySlot(key)]]
			if conn == nil {
				return nil, fmt.Errorf("Redis target slot owner is unavailable")
			}
			if d.Spec.Mode != "cluster" {
				if _, err := conn.Command([]byte("SELECT"), []byte(strconv.Itoa(db))); err != nil {
					return nil, err
				}
			}
			return conn, nil
		}, d.Spec.Mode == "cluster")
		_ = source.Close()
		closeForward()
		if err != nil {
			return err
		}
		if err = exec([]string{"sh", "-c", `set -eu; redis-cli SHUTDOWN NOSAVE; rm -- "/work/$1"`, "unload", name}, nil); err != nil {
			return err
		}
	}
	for _, conn := range connections {
		if d.Spec.Replicas > 0 {
			acknowledged, err := conn.Command([]byte("WAIT"), []byte(strconv.Itoa(d.Spec.Replicas)), []byte("10000"))
			if err != nil || acknowledged != int64(d.Spec.Replicas) {
				return fmt.Errorf("Redis replicas have not confirmed the recovered writes")
			}
		}
		if _, err := conn.Command([]byte("SAVE")); err != nil {
			return fmt.Errorf("Redis recovered data could not be persisted")
		}
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("Redis target health changed during recovery")
	}
	d.Status = "ready"
	return c.databaseNetworkPolicy(ctx, d, func() error { return nil })
}

func (c *Client) redisRecoveryHelper(ctx context.Context, d database.Resource) (*corev1.Pod, func(), error) {
	noop := func() {}
	ns := DatabaseNamespace(d.ID)
	if err := c.databaseNetworkPolicy(ctx, d, func() error { return nil }); err != nil {
		return nil, noop, err
	}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "recovery-isolation", Namespace: ns, Labels: databaseLabels(d)}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{databaseRecoveryHelper: "true"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	if _, err := c.kube.NetworkingV1().NetworkPolicies(ns).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		return nil, noop, fmt.Errorf("recovery isolation could not be created")
	}
	name := "recovery-" + d.Recovery.JobID
	labels := databaseLabels(d)
	labels[databaseRecoveryHelper] = "true"
	memory := resource.MustParse(d.Spec.Memory)
	memory.Add(resource.MustParse("128Mi"))
	storage := resource.MustParse(fmt.Sprintf("%dGi", min(int64(64), d.Spec.StorageGiB*int64(d.Spec.Shards))))
	resources := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: memory, corev1.ResourceEphemeralStorage: storage}
	runtimePolicy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, noop, err
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}, Spec: batchv1.JobSpec{BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: ptr(int64(1800)), TTLSecondsAfterFinished: ptr(int32(60)), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr(false), RestartPolicy: corev1.RestartPolicyNever, SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(1000)), RunAsGroup: ptr(int64(1000)), FSGroup: ptr(int64(1000))}, Containers: []corev1.Container{{Name: "recovery", Image: databaseImages["redis:8"], Command: []string{"sh", "-c", "sleep 1800"}, Resources: corev1.ResourceRequirements{Requests: resources, Limits: resources}, SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true), AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}, {Name: "work", MountPath: "/tmp"}}}}, Volumes: []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &storage}}}}}}}}
	if runtimePolicy != nil {
		applyWorkloadPolicy(&WorkloadPolicy{NodeName: runtimePolicy.NodeName, Pool: runtimePolicy.Pool, RuntimeClass: runtimePolicy.RuntimeClass}, &job.Spec.Template.Spec)
	}

	created, err := c.kube.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, noop, fmt.Errorf("recovery helper could not be created")
	}
	cleanup := func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		propagation := metav1.DeletePropagationBackground
		_ = c.kube.BatchV1().Jobs(ns).Delete(cleanupCtx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &created.UID}, PropagationPolicy: &propagation})
	}
	readyCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	for readyCtx.Err() == nil {
		pods, err := c.kube.CoreV1().Pods(ns).List(readyCtx, metav1.ListOptions{LabelSelector: "job-name=" + name, Limit: 2})
		if err == nil && len(pods.Items) == 1 && pods.Continue == "" {
			pod := &pods.Items[0]
			for _, owner := range pod.OwnerReferences {
				if owner.UID == created.UID && pod.Status.Phase == corev1.PodRunning {
					return pod, cleanup, nil
				}
			}
		}
		if err := sleepContext(readyCtx, time.Second); err != nil {
			break
		}
	}
	cleanup()
	return nil, noop, fmt.Errorf("recovery helper did not become ready")
}

func (c *Client) redisRecoveryExec(ctx context.Context, d database.Resource, expected *corev1.Pod, command []string, input io.Reader) error {
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil || pod.UID != expected.UID || pod.DeletionTimestamp != nil || pod.Labels[databaseOwner] != d.ID || pod.Labels[databaseRecoveryHelper] != "true" || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != databaseImages["redis:8"] {
		return fmt.Errorf("recovery helper identity changed")
	}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "recovery", Command: command, Stdin: input != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return fmt.Errorf("recovery transport is unavailable")
	}
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: input, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		return fmt.Errorf("recovery helper command failed")
	}
	return nil
}

// Port forwarding enters the host network namespace and cannot reach runsc's
// userspace network stack. The pinned Redis image supplies nc; executing it
// inside the verified container carries binary RESP over Kubernetes exec.
func (c *Client) databaseRedisConnection(ctx context.Context, namespace, name string, uid types.UID) (*database.RedisWire, func(), error) {
	noop := func() {}
	pod, err := c.kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || pod.UID != uid || pod.DeletionTimestamp != nil {
		return nil, noop, fmt.Errorf("Redis transport target changed")
	}
	container := ""
	for _, candidate := range pod.Spec.Containers {
		if candidate.Image == databaseImages["redis:8"] {
			container = candidate.Name
		}
	}
	if container == "" || c.execConfig == nil || c.restClient() == nil {
		return nil, noop, fmt.Errorf("Redis transport requires its pinned container")
	}
	ctx, cancel := context.WithCancel(ctx)
	u := c.restClient().Post().Resource("pods").Namespace(namespace).Name(name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: container, Command: []string{"nc", "-w", "35", "127.0.0.1", "6379"}, Stdin: true, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		cancel()
		return nil, noop, fmt.Errorf("Redis transport is unavailable")
	}
	// net.Pipe is unbuffered and preserves RedisWire's per-command deadlines.
	// Closing either the request or stream releases blocked reads and writes.
	conn, stream := net.Pipe()
	var once sync.Once
	closeStream := func() {
		once.Do(func() {
			cancel()
			_ = conn.Close()
			_ = stream.Close()
		})
	}
	go func() {
		defer closeStream()
		_ = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stream, Stdout: stream, Stderr: io.Discard})
	}()
	go func() {
		<-ctx.Done()
		closeStream()
	}()
	return database.NewRedisWire(conn), closeStream, nil
}
