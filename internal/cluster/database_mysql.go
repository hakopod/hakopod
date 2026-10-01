package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const databaseRouterLabel = "hakopod.io/database-router"
const mysqlOperatorImage = "container-registry.oracle.com/mysql/community-operator:26.7.0-2.3.0@sha256:01ecaa57bf952850ff9ffd7caeb231a9c3335764fdb12488f132ea4605d8f364"
const mysqlRouterImage = "container-registry.oracle.com/mysql/community-router:8.4.10@sha256:d704471c2bb78fa833790dc197959c004d09af5aa18f5f153ff40e7268f54043"

func mysqlResources(cpu, memory string) map[string]any {
	return map[string]any{"requests": map[string]any{"cpu": cpu, "memory": memory}, "limits": map[string]any{"cpu": cpu, "memory": memory}}
}

// The MySQL controller needs its pods and credentials while dissolving the
// replication group. Deleting the namespace first races that finalization.
func (c *Client) deleteMySQLController(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	api := c.dynamic.Resource(mysqlDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return false, fmt.Errorf("MySQL controller object ownership changed")
	}
	if object.GetDeletionTimestamp() == nil {
		uid := object.GetUID()
		if err = before(); err != nil {
			return false, err
		}
		err = api.Delete(ctx, object.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	return false, err
}

func mysqlDatabaseSpec(d database.Resource, resources map[string]any) map[string]any {
	server := databaseImages["mysql:8.4"]
	// Override every image the operator generates, including initialization.
	// Supplying a private bootstrap password suppresses the image's random-root
	// password logging. Application credentials are created separately.
	pod := map[string]any{
		"nodeSelector": map[string]any{corev1.LabelArchStable: "amd64"},
		"volumes":      []any{map[string]any{"name": "rundir", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Mi"}}},
		"containers": []any{
			map[string]any{"name": "mysql", "image": server, "resources": resources},
			map[string]any{"name": "sidecar", "image": mysqlOperatorImage, "resources": mysqlResources(database.MySQLSidecarCPU, database.MySQLSidecarMemory)},
		},
		"initContainers": []any{
			map[string]any{"name": "initconf", "image": mysqlOperatorImage, "resources": mysqlResources(database.MySQLSidecarCPU, database.MySQLSidecarMemory)},
			map[string]any{"name": "initmysql", "image": server, "resources": resources, "env": []any{
				map[string]any{"name": "MYSQL_RANDOM_ROOT_PASSWORD", "value": ""},
				map[string]any{"name": "MYSQL_ROOT_PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "database-admin", "key": "rootPassword"}}},
			}},
		},
	}
	return map[string]any{
		"secretName": "database-admin", "version": "8.4.12", "instances": int64(d.Spec.Members()),
		"datadirPermissions": map[string]any{"setRightsUsingInitContainer": false, "fsGroupChangePolicy": "OnRootMismatch"},
		"tlsUseSelfSigned":   false, "tlsCASecretName": "database-tls", "tlsSecretName": "database-tls",
		"podLabels": map[string]any{databaseOwner: d.ID}, "podSpec": pod,
		// gVisor needs a shared in-sandbox mount for cross-container Unix
		// sockets. Disk bind mounts expose the files but not the socket endpoint.
		"podAnnotations":             map[string]any{"dev.gvisor.spec.mount.rundir.share": "pod", "dev.gvisor.spec.mount.rundir.type": "tmpfs", "dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216"},
		"datadirVolumeClaimTemplate": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}},
		"mycnf":                      "[mysqld]\nrequire_secure_transport=ON\ntls_version=TLSv1.2,TLSv1.3\ngeneral_log=OFF\nslow_query_log=OFF\nlocal_infile=OFF\nmax_connections=200\ninnodb_buffer_pool_size=268435456\nlog_bin_trust_function_creators=ON\ngroup_replication_consistency=BEFORE_ON_PRIMARY_FAILOVER\n",
		"router": map[string]any{
			"instances": int64(d.Spec.RouterInstances()), "version": "8.4.10", "tlsSecretName": "database-tls",
			"podLabels":        map[string]any{databaseRouterLabel: "true", databaseOwner: d.ID},
			"bootstrapOptions": []any{"--ssl-mode=VERIFY_IDENTITY", "--client-ssl-mode=REQUIRED", "--server-ssl-mode=REQUIRED", "--conf-set-option=routing:bootstrap_rw_split.server_ssl_mode=REQUIRED", "--conf-set-option=DEFAULT.max_total_connections=200", "--conf-set-option=logger.level=ERROR", "--conf-set-option=routing:bootstrap_ro.routing_strategy=round-robin"},
			"podSpec":          map[string]any{"automountServiceAccountToken": false, "nodeSelector": map[string]any{corev1.LabelArchStable: "amd64"}, "containers": []any{map[string]any{"name": "router", "image": mysqlRouterImage, "resources": mysqlResources(database.MySQLRouterCPU, database.MySQLRouterMemory)}}},
		},
	}
}

func safeMySQLController(pod corev1.PodTemplateSpec) bool {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 {
		return false
	}
	c := pod.Spec.Containers[0]
	if c.Name != "mysql-operator" || c.Image != mysqlOperatorImage {
		return false
	}
	debug, credentials := false, false
	for _, e := range c.Env {
		if e.Name == "MYSQL_OPERATOR_DEBUG" {
			debug = e.Value == "0" && e.ValueFrom == nil
		}
		if e.Name == "MYSQLSH_CREDENTIAL_STORE_SAVE_PASSWORDS" {
			credentials = e.Value == "never" && e.ValueFrom == nil
		}
	}
	return debug && credentials && !c.Resources.Limits.Cpu().IsZero() && !c.Resources.Limits.Memory().IsZero()
}

func (c *Client) prepareMySQLSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "mysql" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" || ns.UID == "" {
		return fmt.Errorf("MySQL namespace ownership changed")
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	secret, err := api.Get(ctx, "database-admin", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		password := make([]byte, 32)
		if _, err = rand.Read(password); err != nil {
			return err
		}
		secret = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-admin"), Immutable: ptr(true), Data: map[string][]byte{"rootUser": []byte("root"), "rootHost": []byte("%"), "rootPassword": []byte(hex.EncodeToString(password))}}
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
		return err
	}
	if secret.Immutable == nil || !*secret.Immutable || string(secret.Data["rootUser"]) != "root" || string(secret.Data["rootHost"]) != "%" || len(secret.Data["rootPassword"]) != 64 {
		return fmt.Errorf("MySQL administrative credential identity changed")
	}
	return nil
}

func mysqlPrimary(object *unstructured.Unstructured) string {
	// The operator status is only used to locate a candidate. Native replication
	// queries must independently verify its role before reporting readiness.
	value, _, _ := unstructured.NestedString(object.Object, "status", "cluster", "status")
	if value != "ONLINE" {
		return ""
	}
	return "database-0"
}

func (c *Client) prepareMySQLAccount(ctx context.Context, d database.Resource, password []byte, before func() error) error {
	if d.Spec.Engine != "mysql" {
		return nil
	}
	object, err := c.dynamic.Resource(mysqlDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if mysqlPrimary(object) == "" {
		return nil
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: "component=mysqld", Limit: 8})
	if err != nil || pods.Continue != "" || len(pods.Items) > 7 {
		return fmt.Errorf("MySQL account member inventory is unavailable")
	}
	for _, pod := range pods.Items {
		member := database.Member{Name: pod.Name, UID: string(pod.UID)}
		out := &databaseBoundedWriter{limit: 256}
		if err = c.DatabaseExec(ctx, d, member, mysqlLocalCommand("SELECT @@super_read_only"), nil, out); err != nil {
			continue
		}
		if strings.TrimSpace(out.String()) != "0" {
			continue
		}
		if bytes.ContainsAny(password, "\x00\r\n") {
			return fmt.Errorf("MySQL application credential format is invalid")
		}
		// This statement travels only through stdin. The account owns app.*, with
		// no global privileges, administrative credential or grant option.
		sql := "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES'; CREATE DATABASE IF NOT EXISTS app; CREATE USER IF NOT EXISTS 'app'@'%' IDENTIFIED BY '" + strings.ReplaceAll(string(password), "'", "''") + "' REQUIRE SSL WITH MAX_USER_CONNECTIONS 100; GRANT ALL PRIVILEGES ON app.* TO 'app'@'%';"
		if err = before(); err != nil {
			return err
		}
		return c.DatabaseExec(ctx, d, member, mysqlLocalCommand(""), strings.NewReader(sql), io.Discard)
	}
	return nil
}

func mysqlLocalCommand(query string) []string {
	command := []string{"mysql", "--no-defaults", "--protocol=SOCKET", "--socket=/var/run/mysqld/mysql.sock", "--user=localroot", "--batch", "--raw", "--skip-column-names", "--connect-timeout=3"}
	if query != "" {
		command = append(command, "--execute", query)
	}
	return command
}

func mysqlPodImagesMatch(pod corev1.Pod, d database.Resource) bool {
	for key, value := range map[string]string{"dev.gvisor.spec.mount.rundir.share": "pod", "dev.gvisor.spec.mount.rundir.type": "tmpfs", "dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216"} {
		if pod.Annotations[key] != value {
			return false
		}
	}
	sharedSocket := false
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "rundir" && volume.EmptyDir != nil && volume.EmptyDir.Medium == corev1.StorageMediumMemory && volume.EmptyDir.SizeLimit != nil && volume.EmptyDir.SizeLimit.Cmp(resource.MustParse("16Mi")) == 0 {
			sharedSocket = true
		}
	}
	if !sharedSocket {
		return false
	}
	expected := map[string]string{"mysql": databaseImages["mysql:8.4"], "sidecar": mysqlOperatorImage, "initmysql": databaseImages["mysql:8.4"], "initconf": mysqlOperatorImage}
	if len(pod.Spec.Containers) != 2 || len(pod.Spec.InitContainers) != 2 {
		return false
	}
	for _, container := range append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...) {
		if container.Image != expected[container.Name] {
			return false
		}
		cpu, memory := d.Spec.CPU, d.Spec.Memory
		if container.Name == "sidecar" || container.Name == "initconf" {
			cpu, memory = database.MySQLSidecarCPU, database.MySQLSidecarMemory
		}
		for k, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory} {
			req, limit := container.Resources.Requests[k], container.Resources.Limits[k]
			want := resource.MustParse(value)
			if req.Cmp(want) != 0 || limit.Cmp(want) != 0 {
				return false
			}
		}
	}
	return true
}
