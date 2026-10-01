package cluster

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const maxRecoveryTarEntries = 100000

func sanitizeRecoveryTar(input io.Reader, expected int64) (*os.File, error) {
	if expected < 1 || expected > platformbackup.MaxArchiveBytes {
		return nil, platformbackup.ErrInvalid
	}
	file, err := os.CreateTemp("", "hakopod-supabase-safe-tar-*")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*os.File, error) { _ = file.Close(); _ = os.Remove(file.Name()); return nil, e }
	_ = file.Chmod(0600)
	reader := tar.NewReader(io.LimitReader(input, expected+1))
	writer := tar.NewWriter(file)
	seen := map[string]struct{}{}
	var entries int
	var bytes int64
	for {
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(platformbackup.ErrInvalid)
		}
		entries++
		if entries > maxRecoveryTarEntries {
			return fail(platformbackup.ErrInvalid)
		}
		name := path.Clean(header.Name)
		if name == "." || name == "/" || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fail(platformbackup.ErrInvalid)
		}
		if _, ok := seen[name]; ok {
			return fail(platformbackup.ErrInvalid)
		}
		seen[name] = struct{}{}
		clean := &tar.Header{Name: name, Mode: header.Mode & 0777, ModTime: header.ModTime, Typeflag: header.Typeflag, Size: header.Size}
		switch header.Typeflag {
		case tar.TypeDir:
			clean.Size = 0
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || bytes+header.Size > expected {
				return fail(platformbackup.ErrInvalid)
			}
			bytes += header.Size
		default:
			return fail(platformbackup.ErrInvalid)
		}
		if e = writer.WriteHeader(clean); e != nil {
			return fail(e)
		}
		if clean.Size > 0 {
			if _, e = io.CopyN(writer, reader, clean.Size); e != nil {
				return fail(platformbackup.ErrInvalid)
			}
		}
	}
	if err = writer.Close(); err != nil {
		return fail(err)
	}
	if err = file.Sync(); err != nil {
		return fail(err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return file, nil
}

type SupabaseRecoveryRuntime struct {
	Cluster *Client
	Store   *store.Store
}

var supabaseRecoveryCryptographicSecretKeys = []string{"anon-key", "jwt-secret", "jwt-signing-keys", "jwt-verification-keys", "pg-meta-crypto-key", "pooler-api-jwt-secret", "publishable-key", "realtime-db-encryption-key", "secret-key-base", "service-role-key", "secret-key", "vault-encryption-key"}

func compatibleSupabaseRecoveryConfig(a, b *managedplatform.SupabaseConfig) bool {
	if a == nil || b == nil {
		return false
	}
	smtp := a.SMTPSecret == nil && b.SMTPSecret == nil || a.SMTPSecret != nil && b.SMTPSecret != nil && *a.SMTPSecret == *b.SMTPSecret
	return smtp && a.DatabaseName == b.DatabaseName && a.JWTExpirySeconds == b.JWTExpirySeconds && a.RESTMaxRows == b.RESTMaxRows && a.StorageFileLimitBytes == b.StorageFileLimitBytes && a.PoolSize == b.PoolSize && a.PoolMaxClients == b.PoolMaxClients && a.EmailSignup == b.EmailSignup && a.AnonymousSignup == b.AnonymousSignup
}

func (r *SupabaseRecoveryRuntime) contract(ctx context.Context, id string, revision int64) (store.ManagedPlatform, managedplatform.Plan, error) {
	if r == nil || r.Cluster == nil || r.Store == nil {
		return store.ManagedPlatform{}, managedplatform.Plan{}, fmt.Errorf("Supabase recovery runtime is unavailable")
	}
	return r.Store.ManagedPlatformRecoveryContract(ctx, id, revision)
}
func (r *SupabaseRecoveryRuntime) fence(ctx context.Context) error {
	op, ok := platformbackup.RecoveryOperationFromContext(ctx)
	if !ok {
		return fmt.Errorf("Supabase recovery mutation is missing its operation lease")
	}
	if platformbackup.RecoveryCleanupFromContext(ctx) {
		return r.Store.FencePlatformRecoveryCleanup(ctx, op)
	}
	cancelled, err := r.Store.HeartbeatPlatformRecovery(ctx, op)
	if err != nil {
		return err
	}
	if cancelled {
		return fmt.Errorf("Supabase recovery operation was cancelled")
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) ResolveSource(ctx context.Context, op platformbackup.Operation) (platformbackup.Manifest, error) {
	item, plan, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return platformbackup.Manifest{}, err
	}
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return platformbackup.Manifest{}, err
	}
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/managed-platform-id"] != item.ID || claims["namespace."+plan.Namespace].ResourceID != string(ns.UID) {
		return platformbackup.Manifest{}, fmt.Errorf("source Supabase namespace ownership is invalid")
	}
	m := platformbackup.Manifest{SchemaVersion: platformbackup.SchemaVersion, Format: platformbackup.Format, PlatformID: item.ID, PlatformRevision: item.Revision, PlatformSpec: json.RawMessage(store.JSON(item.Spec)), Release: item.Spec.Version, Images: map[string]string{}, SourceNamespace: ns.Name, SourceNamespaceUID: string(ns.UID), Consistency: "public and component writes blocked; sessions drained; namespace and PVC UIDs reobserved before publication", DestinationID: op.DestinationID}
	for _, component := range plan.Components {
		m.Images[component.Name] = component.Image
	}
	destination, err := r.Store.BackupDestination(ctx, op.DestinationID)
	if err != nil {
		return m, err
	}
	if destination.ID != op.DestinationID || destination.Revision != op.DestinationRevision {
		return m, fmt.Errorf("backup destination revision changed")
	}
	m.EncryptionRecipient = destination.EncryptionRecipient
	for _, key := range managedplatform.SupabaseRequiredStorageKeys() {
		name := "supabase-" + key
		pvc, err := r.Cluster.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err != nil || pvc.Labels["hakopod.io/managed-platform-id"] != item.ID || len(pvc.OwnerReferences) != 1 || pvc.OwnerReferences[0].UID != ns.UID || claims["pvc."+name].ResourceID != string(pvc.UID) {
			return m, fmt.Errorf("source Supabase PVC ownership is invalid")
		}
		m.PVCs = append(m.PVCs, platformbackup.Claim{Component: key, Kind: "pvc", Name: pvc.Name, UID: string(pvc.UID)})
	}
	return m, nil
}

func (r *SupabaseRecoveryRuntime) ResolveEmptyTarget(ctx context.Context, op platformbackup.Operation, source platformbackup.Manifest) error {
	target, plan, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	var sourceSpec managedplatform.Spec
	if json.Unmarshal(source.PlatformSpec, &sourceSpec) != nil || sourceSpec.Validate() != nil {
		return fmt.Errorf("artifact Supabase specification is invalid")
	}
	if target.ID == source.PlatformID || target.Spec.Version != source.Release || !compatibleSupabaseRecoveryConfig(target.Spec.Supabase, sourceSpec.Supabase) {
		return fmt.Errorf("restore requires a separate Supabase target with a compatible runtime configuration")
	}
	for key, size := range sourceSpec.Storage {
		if target.Spec.Storage[key] < size {
			return fmt.Errorf("target Supabase storage is smaller than the source")
		}
	}
	if err = validateSupabaseRecoverySecrets(target.Spec, sourceSpec); err != nil {
		return err
	}
	for name, image := range source.Images {
		found := false
		for _, component := range plan.Components {
			if component.Name == name && component.Image == image {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("target Supabase image inventory differs from the artifact")
		}
	}
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, target.ID, target.Revision)
	if err != nil {
		return err
	}
	namespace, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if err != nil || claims["namespace."+plan.Namespace].ResourceID != string(namespace.UID) || namespace.Labels["hakopod.io/managed-platform-id"] != target.ID || namespace.Labels["hakopod.io/platform-kind"] != "supabase" {
		return fmt.Errorf("target Supabase namespace ownership is invalid")
	}
	for _, key := range managedplatform.SupabaseRequiredStorageKeys() {
		pvc, e := r.Cluster.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, "supabase-"+key, metav1.GetOptions{})
		if e != nil || pvc.Labels["hakopod.io/managed-platform-id"] != target.ID || len(pvc.OwnerReferences) != 1 || pvc.OwnerReferences[0].UID != namespace.UID || claims["pvc."+pvc.Name].ResourceID != string(pvc.UID) {
			return fmt.Errorf("target Supabase PVC ownership is invalid")
		}
	}
	for _, component := range []string{"api-gateway", "auth", "edge-runtime", "image-proxy", "pooler", "postgres-meta", "realtime", "rest"} {
		if err = r.scaleOne(ctx, target, plan.Namespace, component, 0); err != nil {
			return err
		}
	}
	pod, err := r.pod(ctx, plan.Namespace, target.ID, "database", "")
	if err != nil {
		return err
	}
	var out strings.Builder
	query := `DO $$ DECLARE r record; occupied boolean; BEGIN IF (SELECT count(*) FROM auth.users)+(SELECT count(*) FROM storage.objects)+(SELECT count(*) FROM storage.buckets)>0 THEN RAISE EXCEPTION 'target contains Supabase data'; END IF; FOR r IN SELECT schemaname,tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'schema_migrations' LOOP EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.%I LIMIT 1)',r.schemaname,r.tablename) INTO occupied; IF occupied THEN RAISE EXCEPTION 'target contains application data'; END IF; END LOOP; END $$; SELECT 0;`
	if err = r.exec(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", target.Spec.Supabase.DatabaseName, "-c", query}, nil, &out); err != nil || strings.TrimSpace(out.String()) != "0" {
		return fmt.Errorf("recovery requires a separate empty Supabase target")
	}
	for _, component := range []struct{ name, path string }{{"storage", "/var/lib/storage"}, {"studio", "/app/snippets"}} {
		p, e := r.pod(ctx, plan.Namespace, target.ID, component.name, "")
		if e != nil {
			return e
		}
		out.Reset()
		if e = r.exec(ctx, p, component.name, []string{"sh", "-c", "find \"$1\" -mindepth 1 -print -quit", "empty-check", component.path}, nil, &out); e != nil || strings.TrimSpace(out.String()) != "" {
			return fmt.Errorf("recovery target contains existing %s data", component.name)
		}
		if e = r.scaleOne(ctx, target, plan.Namespace, component.name, 0); e != nil {
			return e
		}
	}
	return nil
}

func validateSupabaseRecoverySecrets(target, source managedplatform.Spec) error {
	for _, key := range supabaseRecoveryCryptographicSecretKeys {
		if target.Secrets[key] != source.Secrets[key] {
			return fmt.Errorf("target Supabase cryptographic secret revisions differ from the artifact source")
		}
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) PauseWrites(ctx context.Context, op platformbackup.Operation) error {
	item, plan, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	pod, err := r.pod(ctx, plan.Namespace, item.ID, "database", "")
	if err != nil {
		return err
	}
	var out strings.Builder
	if err = r.exec(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "-c", `SHOW default_transaction_read_only`}, nil, &out); err != nil {
		return err
	}
	value := strings.TrimSpace(out.String())
	if value != "on" && value != "off" {
		return fmt.Errorf("Supabase database read-only state is invalid")
	}
	if _, err = r.Store.SavePlatformRecoveryDatabaseState(ctx, op, item.ID, value == "on"); err != nil {
		return err
	}
	return r.scaleClients(ctx, item, plan.Namespace, 0)
}
func (r *SupabaseRecoveryRuntime) ResumeSource(ctx context.Context, op platformbackup.Operation) error {
	item, plan, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	prior, err := r.Store.PlatformRecoveryDatabaseState(ctx, op, item.ID)
	if err == nil {
		pod, podErr := r.pod(ctx, plan.Namespace, item.ID, "database", "")
		if podErr != nil {
			return podErr
		}
		setting := "off"
		if prior {
			setting = "on"
		}
		if err = r.exec(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "-c", "ALTER SYSTEM SET default_transaction_read_only=" + setting}, nil, io.Discard); err == nil {
			err = r.exec(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "-c", `SELECT pg_reload_conf()`}, nil, io.Discard)
		}
		if err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return r.scaleClients(ctx, item, plan.Namespace, -1)
}
func recoveryTransitionToken(operationID, name string, generation int64, target int32) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", operationID, name, generation, target)))
	return hex.EncodeToString(sum[:])
}
func (r *SupabaseRecoveryRuntime) transitionDeployment(ctx context.Context, item store.ManagedPlatform, deployment *appsv1.Deployment, target int32, claimedGeneration int64) (int32, error) {
	op, ok := platformbackup.RecoveryOperationFromContext(ctx)
	if !ok {
		return 0, fmt.Errorf("Supabase recovery mutation is missing its operation lease")
	}
	if err := r.fence(ctx); err != nil {
		return 0, err
	}
	current := valueOrOne(deployment.Spec.Replicas)
	uid := string(deployment.UID)
	if token := deployment.Annotations["hakopod.io/recovery-transition"]; token != "" {
		if err := r.Store.ReconcilePlatformRecoveryDeployment(ctx, op, deployment.Name, uid, token, deployment.Generation, current); err != nil {
			journalGeneration, generationErr := r.Store.PlatformRecoveryDeploymentGeneration(ctx, op, deployment.Name, claimedGeneration)
			if generationErr != nil || journalGeneration != deployment.Generation {
				return 0, err
			}
		}
	}
	journalGeneration, err := r.Store.PlatformRecoveryDeploymentGeneration(ctx, op, deployment.Name, claimedGeneration)
	if err != nil {
		return 0, err
	}
	if current == target && journalGeneration == deployment.Generation {
		if prior, priorErr := r.Store.PlatformRecoveryPriorReplicas(ctx, op, item.ID, deployment.Name, uid); priorErr == nil {
			return prior, nil
		}
	}
	token := recoveryTransitionToken(op.ID, deployment.Name, deployment.Generation, target)
	_, prior, err := r.Store.PreparePlatformRecoveryDeployment(ctx, op, item.ID, deployment.Name, uid, deployment.Generation, current, target, claimedGeneration, token)
	if err != nil {
		return 0, err
	}
	deployment = deployment.DeepCopy()
	deployment.Spec.Replicas = &target
	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{}
	}
	deployment.Annotations["hakopod.io/recovery-transition"] = token
	updated, err := r.Cluster.kube.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return 0, err
	}
	if err = r.Store.CompletePlatformRecoveryDeployment(ctx, op, deployment.Name, uid, deployment.Generation, updated.Generation); err != nil {
		return 0, err
	}
	return prior, nil
}

func (r *SupabaseRecoveryRuntime) scaleClients(ctx context.Context, item store.ManagedPlatform, namespace string, replicas int32) error {
	claims, claimErr := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if claimErr != nil {
		return claimErr
	}
	items, err := r.Cluster.kube.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/platform-kind=supabase"})
	if err != nil {
		return err
	}
	if len(items.Items) != 10 {
		return fmt.Errorf("Supabase client deployment inventory changed")
	}
	expected := 0
	for i := range items.Items {
		deployment := items.Items[i].DeepCopy()
		claim := claims["deployment."+deployment.Name]
		if deployment.Labels["hakopod.io/managed-platform-id"] != item.ID || claim.ResourceID != string(deployment.UID) {
			return fmt.Errorf("Supabase deployment ownership changed")
		}
		target := replicas
		if target < 0 {
			target, err = r.Store.PlatformRecoveryPriorReplicas(ctx, mustRecoveryOperation(ctx), item.ID, deployment.Name, string(deployment.UID))
			if errors.Is(err, pgx.ErrNoRows) {
				expected += int(valueOrOne(deployment.Spec.Replicas))
				continue
			}
			if err != nil {
				return err
			}
		}
		if _, err = r.transitionDeployment(ctx, item, deployment, target, claim.ImmutableGeneration); err != nil {
			return err
		}
		expected += int(target)
	}
	return r.waitClientPods(ctx, namespace, item.ID, expected)
}

func mustRecoveryOperation(ctx context.Context) platformbackup.Operation {
	op, _ := platformbackup.RecoveryOperationFromContext(ctx)
	return op
}
func valueOrOne(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}
func (r *SupabaseRecoveryRuntime) waitClientPods(ctx context.Context, namespace, platformID string, expected int) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + platformID})
		if err != nil {
			return err
		}
		ready, total := 0, 0
		for _, pod := range pods.Items {
			if pod.Labels["app.kubernetes.io/component"] != "database" {
				total++
				if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && len(pod.Status.ContainerStatuses) == 1 && pod.Status.ContainerStatuses[0].Ready {
					ready++
				}
			}
		}
		if expected == 0 && total == 0 || expected > 0 && ready == expected && total == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Supabase write-path scaling timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (r *SupabaseRecoveryRuntime) scaleOne(ctx context.Context, item store.ManagedPlatform, namespace, component string, replicas int32) error {
	claims, claimErr := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if claimErr != nil {
		return claimErr
	}
	name := "supabase-" + component
	deployment, err := r.Cluster.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	claim := claims["deployment."+deployment.Name]
	if deployment.Labels["hakopod.io/managed-platform-id"] != item.ID || claim.ResourceID != string(deployment.UID) {
		return fmt.Errorf("Supabase deployment ownership changed")
	}
	if _, err = r.transitionDeployment(ctx, item, deployment, replicas, claim.ImmutableGeneration); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, e := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + item.ID + ",app.kubernetes.io/component=" + component})
		if e != nil {
			return e
		}
		ready := 0
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && len(pod.Status.ContainerStatuses) == 1 && pod.Status.ContainerStatuses[0].Ready {
				ready++
			}
		}
		if replicas == 0 && len(pods.Items) == 0 || replicas == 1 && ready == 1 && len(pods.Items) == 1 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Supabase %s scaling timed out", component)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (r *SupabaseRecoveryRuntime) DrainWrites(ctx context.Context, op platformbackup.Operation) error {
	item, plan, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	pod, err := r.pod(ctx, plan.Namespace, item.ID, "database", "")
	if err != nil {
		return err
	}
	for _, query := range []string{`ALTER SYSTEM SET default_transaction_read_only=on`, `SELECT pg_reload_conf()`, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`} {
		if err = r.exec(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "-c", query}, nil, io.Discard); err != nil {
			return err
		}
	}
	return nil
}

const supabaseDatabaseKeyDirectory = "/var/lib/postgresql/pgsodium-volume/keyring"

func supabaseDatabaseKeyCaptureCommand() []string {
	return []string{"tar", "-C", supabaseDatabaseKeyDirectory, "-cf", "-", "pgsodium_root.key"}
}

func sanitizeSupabaseDatabaseKeyTar(input io.Reader, expected int64) (*os.File, error) {
	if expected < 1 || expected > platformbackup.MaxArchiveBytes {
		return nil, platformbackup.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(input, expected+1))
	if err != nil || int64(len(data)) != expected {
		return nil, platformbackup.ErrInvalid
	}
	reader := tar.NewReader(bytes.NewReader(data))
	header, err := reader.Next()
	if err != nil || header.Name != "pgsodium_root.key" || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size != 64 || header.Mode&0777 != 0600 {
		return nil, platformbackup.ErrInvalid
	}
	key := make([]byte, 64)
	if _, err = io.ReadFull(reader, key); err != nil {
		return nil, platformbackup.ErrInvalid
	}
	for _, value := range key {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return nil, platformbackup.ErrInvalid
		}
	}
	if _, err = reader.Next(); err != io.EOF {
		return nil, platformbackup.ErrInvalid
	}
	file, err := os.CreateTemp("", "hakopod-supabase-key-*")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*os.File, error) { _ = file.Close(); _ = os.Remove(file.Name()); return nil, e }
	if err = file.Chmod(0600); err != nil {
		return fail(err)
	}
	if _, err = file.Write(key); err != nil {
		return fail(err)
	}
	if err = file.Sync(); err != nil {
		return fail(err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return file, nil
}

func supabaseDatabaseKeyRestoreCommand() []string {
	return []string{"sh", "-ceu", `directory="$1"
key="$directory/pgsodium_root.key"
temporary="$directory/.pgsodium_root.key.restore.$$"
umask 077
trap 'rm -f "$temporary"' EXIT HUP INT TERM
cat > "$temporary"
test -f "$temporary" && test ! -L "$temporary"
test "$(stat -c %u "$temporary")" = "$(id -u)"
test "$(stat -c %g "$temporary")" = "$(id -g)"
test "$(stat -c %a "$temporary")" = 600
test "$(wc -c < "$temporary" | tr -d ' ')" = 64
LC_ALL=C grep -Eq '^[0-9a-f]{64}$' "$temporary"
mv -f "$temporary" "$key"
trap - EXIT HUP INT TERM`, "restore-pgsodium-key", supabaseDatabaseKeyDirectory}
}

func (r *SupabaseRecoveryRuntime) Capture(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) ([]platformbackup.CapturedPart, map[string]string, error) {
	item, plan, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return nil, nil, err
	}
	commands := []struct {
		name, component string
		command         []string
	}{{"database-encryption.tar", "database", supabaseDatabaseKeyCaptureCommand()}, {"database-roles.sql", "database", []string{"pg_dumpall", "--roles-only", "--no-role-passwords", "-U", "postgres"}}, {"database.dump", "database", []string{"pg_dump", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "--format=custom", "--compress=0"}}, {"edge-functions.tar", "edge-runtime", []string{"tar", "-C", "/home/deno/functions", "-cf", "-", "."}}, {"storage-objects.tar", "storage", []string{"tar", "-C", "/var/lib/storage", "-cf", "-", "."}}, {"studio-snippets.tar", "studio", []string{"tar", "-C", "/app/snippets", "-cf", "-", "."}}}
	parts := make([]platformbackup.CapturedPart, 0, len(commands))
	paths := []string{}
	remaining := platformbackup.MaxArchiveBytes
	fail := func(e error) ([]platformbackup.CapturedPart, map[string]string, error) {
		for _, path := range paths {
			_ = os.Remove(path)
		}
		return nil, nil, e
	}
	for _, capture := range commands {
		scaled := capture.component != "database"
		if scaled {
			if e := r.scaleOne(ctx, item, plan.Namespace, capture.component, 1); e != nil {
				return fail(e)
			}
		}
		pod, e := r.pod(ctx, plan.Namespace, item.ID, capture.component, m.Images[capture.component])
		if e != nil {
			if scaled {
				_ = r.scaleOne(context.WithoutCancel(ctx), item, plan.Namespace, capture.component, 0)
			}
			return fail(e)
		}
		file, e := os.CreateTemp("", "hakopod-supabase-recovery-*")
		if e != nil {
			return fail(e)
		}
		path := file.Name()
		paths = append(paths, path)
		_ = file.Chmod(0600)
		hash := sha256.New()
		limited := &recoveryLimitWriter{Writer: io.MultiWriter(file, hash), Remaining: remaining}
		e = r.exec(ctx, pod, capture.component, capture.command, nil, limited)
		closeErr := file.Close()
		if scaled {
			scaleErr := r.scaleOne(context.WithoutCancel(ctx), item, plan.Namespace, capture.component, 0)
			if e == nil {
				e = scaleErr
			}
		}
		if e != nil || closeErr != nil || limited.Written < 1 {
			return fail(fmt.Errorf("Supabase %s capture failed", capture.name))
		}
		remaining -= limited.Written
		pathCopy := path
		parts = append(parts, platformbackup.CapturedPart{Part: platformbackup.Part{Name: capture.name, Bytes: limited.Written, SHA256: hex.EncodeToString(hash.Sum(nil))}, Open: func() (io.ReadCloser, error) {
			f, e := os.Open(pathCopy)
			if e != nil {
				return nil, e
			}
			return &removeReadCloser{File: f, path: pathCopy}, nil
		}, Cleanup: func() error {
			err := os.Remove(pathCopy)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}})
	}
	evidence, e := r.captureEvidence(ctx, item, plan, m.Images)
	if e != nil {
		return fail(e)
	}
	return parts, evidence, nil
}

func (r *SupabaseRecoveryRuntime) captureEvidence(ctx context.Context, item store.ManagedPlatform, plan managedplatform.Plan, images map[string]string) (map[string]string, error) {
	result := map[string]string{}
	database, err := r.pod(ctx, plan.Namespace, item.ID, "database", images["database"])
	if err != nil {
		return nil, err
	}
	queries := map[string]string{"database_roles": `SELECT jsonb_build_object('roles',COALESCE((SELECT jsonb_agg(jsonb_build_array(rolname,rolsuper,rolinherit,rolcreaterole,rolcreatedb,rolcanlogin,rolreplication,rolbypassrls,rolconnlimit,COALESCE(rolvaliduntil::text,'')) ORDER BY rolname) FROM pg_roles),'[]'::jsonb),'memberships',COALESCE((SELECT jsonb_agg(jsonb_build_array(roleid::regrole::text,member::regrole::text,admin_option) ORDER BY roleid::regrole::text,member::regrole::text) FROM pg_auth_members),'[]'::jsonb))::text`, "auth_metadata": `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id)::text,'[]') FROM auth.users t`, "storage_metadata": `SELECT jsonb_build_object('buckets',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM storage.buckets t),'[]'::jsonb),'objects',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY id,name) FROM storage.objects t),'[]'::jsonb))::text`, "database_security": `SELECT jsonb_build_object('tables',COALESCE((SELECT jsonb_agg(jsonb_build_array(n.nspname,c.relname,r.rolname,c.relrowsecurity,c.relforcerowsecurity,COALESCE(c.relacl::text,'')) ORDER BY n.nspname,c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.oid=c.relowner WHERE c.relkind IN ('r','p','v','m','S') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname !~ '^pg_toast'),'[]'::jsonb),'policies',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY schemaname,tablename,policyname) FROM pg_policies p),'[]'::jsonb),'schema_acl',COALESCE((SELECT jsonb_agg(jsonb_build_array(n.nspname,r.rolname,COALESCE(n.nspacl::text,'')) ORDER BY n.nspname) FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname !~ '^pg_toast'),'[]'::jsonb))::text`}
	for key, query := range queries {
		digest, e := r.commandDigest(ctx, database, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", item.Spec.Supabase.DatabaseName, "-c", query})
		if e != nil {
			return nil, e
		}
		result[key] = digest
	}
	trees := []struct{ key, component, path string }{{"storage_bytes", "storage", "/var/lib/storage"}, {"edge_functions", "edge-runtime", "/home/deno/functions"}, {"studio_snippets", "studio", "/app/snippets"}}
	for _, tree := range trees {
		if err = r.scaleOne(ctx, item, plan.Namespace, tree.component, 1); err != nil {
			return nil, err
		}
		pod, e := r.pod(ctx, plan.Namespace, item.ID, tree.component, images[tree.component])
		if e == nil {
			result[tree.key], e = r.commandDigest(ctx, pod, tree.component, []string{"sh", "-c", `cd "$1"; find . -type f -exec sha256sum {} \; | LC_ALL=C sort`, "digest", tree.path})
		}
		scaleErr := r.scaleOne(context.WithoutCancel(ctx), item, plan.Namespace, tree.component, 0)
		if e != nil {
			return nil, e
		}
		if scaleErr != nil {
			return nil, scaleErr
		}
	}
	return result, nil
}

func (r *SupabaseRecoveryRuntime) commandDigest(ctx context.Context, pod *corev1.Pod, container string, command []string) (string, error) {
	hash := sha256.New()
	limited := &recoveryLimitWriter{Writer: hash, Remaining: 8 << 20}
	if err := r.exec(ctx, pod, container, command, nil, limited); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *SupabaseRecoveryRuntime) ReobserveSourceClaims(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, m.SourceNamespace, metav1.GetOptions{})
	if err != nil || string(ns.UID) != m.SourceNamespaceUID || claims["namespace."+m.SourceNamespace].ResourceID != string(ns.UID) {
		return fmt.Errorf("source namespace UID changed")
	}
	for _, claim := range m.PVCs {
		pvc, e := r.Cluster.kube.CoreV1().PersistentVolumeClaims(m.SourceNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
		if e != nil || string(pvc.UID) != claim.UID || claims["pvc."+claim.Name].ResourceID != claim.UID {
			return fmt.Errorf("source PVC UID changed")
		}
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) verifyTargetPVC(ctx context.Context, target store.ManagedPlatform, plan managedplatform.Plan, key string) error {
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, target.ID, target.Revision)
	if err != nil {
		return err
	}
	namespace, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if err != nil || claims["namespace."+plan.Namespace].ResourceID != string(namespace.UID) {
		return fmt.Errorf("target namespace ownership changed")
	}
	name := "supabase-" + key
	pvc, err := r.Cluster.kube.CoreV1().PersistentVolumeClaims(plan.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || claims["pvc."+name].ResourceID != string(pvc.UID) || len(pvc.OwnerReferences) != 1 || pvc.OwnerReferences[0].UID != namespace.UID {
		return fmt.Errorf("target PVC ownership changed")
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) RestorePart(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest, part platformbackup.Part, input io.Reader) error {
	target, plan, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	component, path, storageKey, command := "database", "", "database", []string{}
	switch part.Name {
	case "database-encryption.tar":
		path, storageKey = supabaseDatabaseKeyDirectory, "database-encryption"
	case "database-roles.sql":
		command = nil
	case "database.dump":
		command = []string{"pg_restore", "-U", "postgres", "-d", target.Spec.Supabase.DatabaseName, "--clean", "--if-exists", "--exit-on-error", "--single-transaction"}
	case "edge-functions.tar":
		component, path, storageKey = "edge-runtime", "/home/deno/functions", "edge-functions"
	case "storage-objects.tar":
		component, path, storageKey = "storage", "/var/lib/storage", "objects"
	case "studio-snippets.tar":
		component, path, storageKey = "studio", "/app/snippets", "studio-snippets"
	default:
		return platformbackup.ErrInvalid
	}
	if err = r.verifyTargetPVC(ctx, target, plan, storageKey); err != nil {
		return err
	}
	if component != "database" {
		if err = r.scaleOne(ctx, target, plan.Namespace, component, 1); err != nil {
			return err
		}
		defer func() { _ = r.scaleOne(context.WithoutCancel(ctx), target, plan.Namespace, component, 0) }()
	}
	pod, err := r.pod(ctx, plan.Namespace, target.ID, component, m.Images[component])
	if err != nil {
		return err
	}
	if part.Name == "database-roles.sql" {
		actual, e := r.commandDigest(ctx, pod, "database", []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", target.Spec.Supabase.DatabaseName, "-c", `SELECT jsonb_build_object('roles',COALESCE((SELECT jsonb_agg(jsonb_build_array(rolname,rolsuper,rolinherit,rolcreaterole,rolcreatedb,rolcanlogin,rolreplication,rolbypassrls,rolconnlimit,COALESCE(rolvaliduntil::text,'')) ORDER BY rolname) FROM pg_roles),'[]'::jsonb),'memberships',COALESCE((SELECT jsonb_agg(jsonb_build_array(roleid::regrole::text,member::regrole::text,admin_option) ORDER BY roleid::regrole::text,member::regrole::text) FROM pg_auth_members),'[]'::jsonb))::text`})
		if e != nil {
			return e
		}
		if actual != m.Verification["database_roles"] {
			return fmt.Errorf("target Supabase role inventory differs from the artifact")
		}
		return nil
	}
	restoreInput := input
	var safe *os.File
	if part.Name == "database-encryption.tar" {
		safe, err = sanitizeSupabaseDatabaseKeyTar(input, part.Bytes)
		if err != nil {
			return err
		}
		defer func() { name := safe.Name(); _ = safe.Close(); _ = os.Remove(name) }()
		restoreInput = safe
		command = supabaseDatabaseKeyRestoreCommand()
	} else if path != "" {
		safe, err = sanitizeRecoveryTar(input, part.Bytes)
		if err != nil {
			return err
		}
		defer func() { name := safe.Name(); _ = safe.Close(); _ = os.Remove(name) }()
		restoreInput = safe
		command = []string{"sh", "-c", `set -eu; root="$1"; find "$root" -mindepth 1 -delete; tar -xpf - -C "$root" --no-same-owner --no-same-permissions`, "restore", path}
	}
	if err = r.exec(ctx, pod, component, command, restoreInput, io.Discard); err != nil {
		return err
	}
	if part.Name == "database-encryption.tar" {
		oldUID := pod.UID
		if err = r.fence(ctx); err != nil {
			return err
		}
		if err = r.Cluster.kube.CoreV1().Pods(plan.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &oldUID}}); err != nil {
			return err
		}
		deadline := time.Now().Add(2 * time.Minute)
		for {
			next, e := r.pod(ctx, plan.Namespace, target.ID, "database", m.Images["database"])
			if e == nil && next.UID != oldUID {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("database did not restart after encryption claim restore")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) VerifyRestoredContent(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	return r.verifyTarget(ctx, op, m, false)
}
func (r *SupabaseRecoveryRuntime) VerifyRestoredRuntime(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	return r.verifyTarget(ctx, op, m, true)
}
func (r *SupabaseRecoveryRuntime) verifyTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest, runtime bool) error {
	target, plan, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	evidence, err := r.captureEvidence(ctx, target, plan, m.Images)
	if err != nil {
		return err
	}
	for key, expected := range m.Verification {
		if evidence[key] != expected {
			return fmt.Errorf("restored Supabase %s content differs from the artifact", strings.ReplaceAll(key, "_", " "))
		}
	}
	if runtime {
		pod, err := r.pod(ctx, plan.Namespace, target.ID, "database", m.Images["database"])
		if err != nil {
			return err
		}
		var out strings.Builder
		if err = r.exec(ctx, pod, "database", []string{"pg_isready", "-U", "postgres", "-d", target.Spec.Supabase.DatabaseName}, nil, &out); err != nil {
			return fmt.Errorf("restored Supabase database is not ready")
		}
		for _, component := range plan.Components {
			if component.Name == "database" || component.Name == "api-gateway" {
				continue
			}
			if err = r.scaleOne(ctx, target, plan.Namespace, component.Name, 1); err != nil {
				return fmt.Errorf("restored Supabase %s is not ready: %w", component.Name, err)
			}
		}
		gateway, err := r.Cluster.kube.AppsV1().Deployments(plan.Namespace).Get(ctx, "supabase-api-gateway", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if gateway.Spec.Replicas == nil || *gateway.Spec.Replicas != 0 || gateway.Status.ReadyReplicas != 0 {
			return fmt.Errorf("restored Supabase public gateway is not isolated")
		}
		if err = r.scaleClients(ctx, target, plan.Namespace, 0); err != nil {
			return err
		}
	}
	return nil
}

func (r *SupabaseRecoveryRuntime) pod(ctx context.Context, namespace, platformID, component, image string) (*corev1.Pod, error) {
	selector := labels.Set{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platformID, "app.kubernetes.io/component": component}.AsSelector().String()
	pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil || len(pods.Items) != 1 {
		return nil, fmt.Errorf("owned Supabase %s pod is unavailable", component)
	}
	pod := pods.Items[0].DeepCopy()
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/managed-platform-id"] != platformID {
		return nil, fmt.Errorf("owned Supabase namespace identity changed")
	}
	namespaceClaim, err := r.Store.ManagedPlatformRecoveryCurrentClaim(ctx, platformID, "namespace."+namespace)
	if err != nil || namespaceClaim.ResourceID != string(ns.UID) {
		return nil, fmt.Errorf("owned Supabase namespace claim changed")
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Controller == nil || !*pod.OwnerReferences[0].Controller {
		return nil, fmt.Errorf("owned Supabase %s pod controller changed", component)
	}
	if component == "database" {
		if pod.OwnerReferences[0].Kind != "StatefulSet" {
			return nil, fmt.Errorf("owned Supabase database controller changed")
		}
		stateful, loadErr := r.Cluster.kube.AppsV1().StatefulSets(namespace).Get(ctx, pod.OwnerReferences[0].Name, metav1.GetOptions{})
		if loadErr != nil || stateful.UID != pod.OwnerReferences[0].UID || len(stateful.OwnerReferences) != 1 || stateful.OwnerReferences[0].UID != ns.UID {
			return nil, fmt.Errorf("owned Supabase database controller changed")
		}
		claim, loadErr := r.Store.ManagedPlatformRecoveryCurrentClaim(ctx, platformID, "statefulset."+stateful.Name)
		if loadErr != nil || claim.ResourceID != string(stateful.UID) || claim.ImmutableGeneration != stateful.Generation {
			return nil, fmt.Errorf("owned Supabase database claim changed")
		}
	} else {
		if pod.OwnerReferences[0].Kind != "ReplicaSet" {
			return nil, fmt.Errorf("owned Supabase %s pod controller changed", component)
		}
		rs, loadErr := r.Cluster.kube.AppsV1().ReplicaSets(namespace).Get(ctx, pod.OwnerReferences[0].Name, metav1.GetOptions{})
		if loadErr != nil || rs.UID != pod.OwnerReferences[0].UID || len(rs.OwnerReferences) != 1 || rs.OwnerReferences[0].Controller == nil || !*rs.OwnerReferences[0].Controller || rs.OwnerReferences[0].Kind != "Deployment" {
			return nil, fmt.Errorf("owned Supabase %s replica controller changed", component)
		}
		deployment, loadErr := r.Cluster.kube.AppsV1().Deployments(namespace).Get(ctx, rs.OwnerReferences[0].Name, metav1.GetOptions{})
		if loadErr != nil || deployment.UID != rs.OwnerReferences[0].UID || len(deployment.OwnerReferences) != 1 || deployment.OwnerReferences[0].UID != ns.UID {
			return nil, fmt.Errorf("owned Supabase %s deployment controller changed", component)
		}
		claim, loadErr := r.Store.ManagedPlatformRecoveryCurrentClaim(ctx, platformID, "deployment."+deployment.Name)
		if loadErr != nil || claim.ResourceID != string(deployment.UID) {
			return nil, fmt.Errorf("owned Supabase %s deployment claim changed", component)
		}
		op, ok := platformbackup.RecoveryOperationFromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("Supabase recovery pod check is missing its operation lease")
		}
		expectedGeneration, loadErr := r.Store.PlatformRecoveryDeploymentGeneration(ctx, op, deployment.Name, claim.ImmutableGeneration)
		if loadErr != nil || deployment.Generation != expectedGeneration {
			return nil, fmt.Errorf("owned Supabase %s deployment generation changed", component)
		}
	}
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || len(pod.Spec.Containers) != 1 || len(pod.Status.ContainerStatuses) != 1 || !pod.Status.ContainerStatuses[0].Ready || pod.Spec.Containers[0].Name != component || image != "" && pod.Spec.Containers[0].Image != image {
		return nil, fmt.Errorf("owned Supabase %s pod identity changed", component)
	}
	if image != "" {
		digest := image[strings.LastIndex(image, "@sha256:")+1:]
		if !strings.HasSuffix(pod.Status.ContainerStatuses[0].ImageID, digest) {
			return nil, fmt.Errorf("owned Supabase %s runtime image changed", component)
		}
	}
	return pod, nil
}
func (r *SupabaseRecoveryRuntime) exec(ctx context.Context, pod *corev1.Pod, container string, command []string, stdin io.Reader, stdout io.Writer) error {
	if r.Cluster.execConfig == nil {
		return fmt.Errorf("Supabase recovery execution transport is unavailable")
	}
	if len(pod.Spec.Containers) != 1 {
		return fmt.Errorf("owned Supabase pod identity changed before execution")
	}
	platformID := pod.Labels["hakopod.io/managed-platform-id"]
	component := pod.Labels["app.kubernetes.io/component"]
	current, err := r.pod(ctx, pod.Namespace, platformID, component, pod.Spec.Containers[0].Image)
	if err != nil || current.Name != pod.Name || current.UID != pod.UID || current.ResourceVersion != pod.ResourceVersion || current.Spec.Containers[0].Name != container {
		return fmt.Errorf("owned Supabase pod identity changed before execution")
	}
	if err = r.fence(ctx); err != nil {
		return err
	}
	request := r.Cluster.restClient().Post().Resource("pods").Namespace(current.Namespace).Name(current.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: stdin != nil, Stdout: stdout != nil, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(r.Cluster.execConfig, http.MethodPost, request)
	if err != nil {
		return err
	}
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: io.Discard}); err != nil {
		return fmt.Errorf("Supabase recovery command failed")
	}
	return nil
}

type recoveryLimitWriter struct {
	Writer             io.Writer
	Remaining, Written int64
}

func (w *recoveryLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, fmt.Errorf("recovery part exceeds bound")
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	w.Written += int64(n)
	return n, err
}

type removeReadCloser struct {
	*os.File
	path string
}

func (r *removeReadCloser) Close() error {
	err := r.File.Close()
	removeErr := os.Remove(r.path)
	if err != nil {
		return err
	}
	return removeErr
}
