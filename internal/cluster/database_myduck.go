package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Release admission requires native qualification and an independent pull of
// this exact hardened image. An empty pin never falls back to upstream.
const myduckServerImage = "ghcr.io/hakopod/managed-myduck@sha256:ad324a97360dea53f9e32cb367666b8fefa6f52377000c084a63c1712ca79873"
const myduckConfigPath = "/etc/hakopod-config/managed.json"
const myduckIdentityAnnotation = "hakopod.io/myduck-identity"
const myduckMemberLabel = "hakopod.io/myduck-member"

var myduckDatabaseResource = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}

func myduckRuntimeSupported(s database.Spec) error {
	if err := s.ValidateMyDuck(); err != nil {
		return err
	}
	if !database.MyDuckRuntimeQualified || myduckServerImage == "" {
		return fmt.Errorf("DuckDB (MyDuck) is unavailable pending hardened runtime qualification")
	}
	return nil
}

func myduckConfiguration(d database.Resource) map[string]string {
	memory := resource.MustParse(d.Spec.Memory)
	cpu := resource.MustParse(d.Spec.CPU)
	config := map[string]any{
		"schema_version": 1, "database": "app", "data_dir": "/var/lib/myduck",
		"password_file": "/etc/hakopod-app/password",
		"tls_cert_file": "/etc/hakopod-tls/tls.crt", "tls_key_file": "/etc/hakopod-tls/tls.key",
		"server_name": "database." + DatabaseNamespace(d.ID) + ".svc",
		"mysql_port":  3306, "postgres_port": 5432,
		"memory_limit_bytes": memory.Value() * 7 / 10,
		"threads":            max(int64(1), (cpu.MilliValue()+999)/1000),
		"temp_directory":     "/var/lib/myduck/tmp", "temp_limit_bytes": d.Spec.StorageGiB << 28,
		"max_connections": 64, "statement_timeout_seconds": 60,
	}
	raw, _ := json.Marshal(config)
	return map[string]string{"managed.json": string(raw) + "\n"}
}

func myduckDatabaseSpec(d database.Resource, resources map[string]any) map[string]any {
	labels := map[string]any{databaseOwner: d.ID, managedBy: "hakopod", myduckMemberLabel: "true"}
	resources["requests"].(map[string]any)["ephemeral-storage"] = "64Mi"
	resources["limits"].(map[string]any)["ephemeral-storage"] = "256Mi"
	config := myduckConfiguration(d)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(config["managed.json"])))
	security := map[string]any{"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	return map[string]any{
		"replicas": int64(1), "serviceName": "database", "selector": map[string]any{"matchLabels": labels},
		"updateStrategy":       map[string]any{"type": "RollingUpdate"},
		"volumeClaimTemplates": []any{map[string]any{"metadata": map[string]any{"name": "data", "labels": labels}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}}},
		"template": map[string]any{"metadata": map[string]any{"labels": labels, "annotations": map[string]any{"hakopod.io/myduck-config": fingerprint}}, "spec": map[string]any{
			"automountServiceAccountToken": false, "enableServiceLinks": false, "terminationGracePeriodSeconds": int64(120),
			"nodeSelector":    map[string]any{corev1.LabelArchStable: "amd64"},
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000), "fsGroup": int64(1000), "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{
				"name": "myduck", "image": myduckServerImage, "imagePullPolicy": "IfNotPresent",
				"command": []any{"/usr/local/bin/myduckserver", "--managed-config=" + myduckConfigPath}, "resources": resources, "securityContext": security,
				"ports": []any{map[string]any{"name": "mysql", "containerPort": int64(3306)}, map[string]any{"name": "postgresql", "containerPort": int64(5432)}},
				"volumeMounts": []any{
					map[string]any{"name": "data", "mountPath": "/var/lib/myduck"}, map[string]any{"name": "temporary", "mountPath": "/tmp"},
					map[string]any{"name": "database-tls", "mountPath": "/etc/hakopod-tls", "readOnly": true},
					map[string]any{"name": "database-app", "mountPath": "/etc/hakopod-app", "readOnly": true},
					map[string]any{"name": "database-config", "mountPath": "/etc/hakopod-config", "readOnly": true},
				},
				"readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"/usr/local/bin/myduckserver", "--managed-check=" + myduckConfigPath}}, "timeoutSeconds": int64(10), "periodSeconds": int64(15)},
				"startupProbe":   map[string]any{"exec": map[string]any{"command": []any{"/usr/local/bin/myduckserver", "--managed-check=" + myduckConfigPath}}, "timeoutSeconds": int64(10), "periodSeconds": int64(10), "failureThreshold": int64(60)},
			}},
			"volumes": []any{
				map[string]any{"name": "database-tls", "secret": map[string]any{"secretName": "database-tls", "defaultMode": int64(0440)}},
				map[string]any{"name": "database-app", "secret": map[string]any{"secretName": "database-credentials", "defaultMode": int64(0440)}},
				map[string]any{"name": "database-config", "configMap": map[string]any{"name": "database-myduck-config", "defaultMode": int64(0440)}},
				map[string]any{"name": "temporary", "emptyDir": map[string]any{"sizeLimit": "128Mi"}},
			},
		}},
	}
}

func (c *Client) prepareMyDuckSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "duckdb" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MyDuck namespace ownership changed")
	}
	meta := databaseIdentityMeta(d, ns.UID, "database-myduck-config")
	configs := c.kube.CoreV1().ConfigMaps(ns.Name)
	config, err := configs.Get(ctx, meta.Name, metav1.GetOptions{})
	desired := myduckConfiguration(d)
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = configs.Create(ctx, &corev1.ConfigMap{ObjectMeta: meta, Data: desired}, metav1.CreateOptions{})
	} else if err == nil {
		if !mongodbSupportOwned(config, d, ns.UID) || config.DeletionTimestamp != nil {
			return fmt.Errorf("MyDuck configuration ownership changed")
		}
		if !reflect.DeepEqual(config.Data, desired) || len(config.BinaryData) != 0 {
			config.Data, config.BinaryData = desired, nil
			if err = before(); err != nil {
				return err
			}
			_, err = configs.Update(ctx, config, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return err
	}
	meta.Name = "database"
	service := &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{databaseOwner: d.ID, managedBy: "hakopod", myduckMemberLabel: "true"}, Ports: []corev1.ServicePort{
		{Name: "mysql", Port: 3306, TargetPort: intstr.FromInt(3306), Protocol: corev1.ProtocolTCP},
		{Name: "postgresql", Port: 5432, TargetPort: intstr.FromInt(5432), Protocol: corev1.ProtocolTCP},
	}}}
	services := c.kube.CoreV1().Services(ns.Name)
	old, err := services.Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = services.Create(ctx, service, metav1.CreateOptions{})
	} else if err == nil && (!mongodbSupportOwned(old, d, ns.UID) || old.DeletionTimestamp != nil || old.Spec.Type != corev1.ServiceTypeClusterIP || old.Spec.PublishNotReadyAddresses || old.Spec.ExternalName != "" || len(old.Spec.ExternalIPs) != 0 || !reflect.DeepEqual(old.Spec.Selector, service.Spec.Selector) || !reflect.DeepEqual(old.Spec.Ports, service.Spec.Ports)) {
		return fmt.Errorf("MyDuck private endpoint ownership or routing changed")
	}
	if err != nil {
		return err
	}
	identity, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	sets := c.kube.AppsV1().StatefulSets(ns.Name)
	set, err := sets.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if set.UID == "" || set.DeletionTimestamp != nil || set.Labels[databaseOwner] != d.ID || set.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MyDuck workload ownership changed")
	}
	if set.Annotations[myduckColdAnnotation] != "" {
		return fmt.Errorf("MyDuck cold-storage cleanup must finish before identity maintenance")
	}
	fingerprint := myduckIdentityFingerprint(identity)
	if set.Spec.Template.Annotations[myduckIdentityAnnotation] == fingerprint {
		return nil
	}
	object, err := c.databaseObject(ctx, d)
	if err != nil {
		return err
	}
	var replacement struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &replacement); err != nil {
		return err
	}
	set.Spec.Template = replacement.Spec.Template
	if set.Spec.Template.Annotations == nil {
		set.Spec.Template.Annotations = map[string]string{}
	}
	set.Spec.Template.Annotations[myduckIdentityAnnotation] = fingerprint
	if err = before(); err != nil {
		return err
	}
	_, err = sets.Update(ctx, set, metav1.UpdateOptions{})
	return err
}

func myduckIdentityFingerprint(secret *corev1.Secret) string {
	h := sha256.New()
	for _, key := range []string{"ca.crt", "tls.crt", "tls.key"} {
		_, _ = h.Write(secret.Data[key])
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func myduckPodMatches(p corev1.Pod, d database.Resource) bool {
	if d.Spec.Engine != "duckdb" || p.Labels[databaseOwner] != d.ID || p.Labels[managedBy] != "hakopod" || p.Labels[myduckMemberLabel] != "true" || myduckServerImage == "" || len(p.Spec.Containers) != 1 || len(p.Spec.InitContainers) != 0 || len(p.Spec.EphemeralContainers) != 0 || p.Spec.HostNetwork || p.Spec.HostPID || p.Spec.HostIPC || p.Spec.ShareProcessNamespace != nil && *p.Spec.ShareProcessNamespace || p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken || p.Spec.EnableServiceLinks == nil || *p.Spec.EnableServiceLinks {
		return false
	}
	security := p.Spec.SecurityContext
	if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.RunAsUser == nil || *security.RunAsUser != 1000 || security.RunAsGroup == nil || *security.RunAsGroup != 1000 || security.FSGroup == nil || *security.FSGroup != 1000 || security.FSGroupChangePolicy == nil || *security.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch || security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || security.SeccompProfile.LocalhostProfile != nil || len(security.SupplementalGroups) != 0 || len(security.Sysctls) != 0 || security.SELinuxOptions != nil || security.WindowsOptions != nil {
		return false
	}
	container := p.Spec.Containers[0]
	if container.Name != "myduck" || container.Image != myduckServerImage || len(container.Args) != 0 || len(container.Env) != 0 || len(container.EnvFrom) != 0 || !reflect.DeepEqual(container.Command, []string{"/usr/local/bin/myduckserver", "--managed-config=" + myduckConfigPath}) {
		return false
	}
	cs := container.SecurityContext
	if cs == nil || cs.Privileged != nil && *cs.Privileged || cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation || cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem || cs.RunAsNonRoot == nil || !*cs.RunAsNonRoot || cs.RunAsUser == nil || *cs.RunAsUser != 1000 || cs.RunAsGroup == nil || *cs.RunAsGroup != 1000 || cs.Capabilities == nil || len(cs.Capabilities.Add) != 0 || !reflect.DeepEqual(cs.Capabilities.Drop, []corev1.Capability{"ALL"}) || cs.ProcMount != nil && *cs.ProcMount != corev1.DefaultProcMount || cs.SELinuxOptions != nil || cs.WindowsOptions != nil || cs.SeccompProfile != nil && (cs.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || cs.SeccompProfile.LocalhostProfile != nil) {
		return false
	}
	if len(container.Ports) != 2 {
		return false
	}
	ports := map[string]int32{"mysql": 3306, "postgresql": 5432}
	for _, port := range container.Ports {
		if ports[port.Name] != port.ContainerPort || port.Protocol != corev1.ProtocolTCP || port.HostPort != 0 || port.HostIP != "" {
			return false
		}
		delete(ports, port.Name)
	}
	if len(ports) != 0 {
		return false
	}
	mounts := map[string]corev1.VolumeMount{
		"data": {Name: "data", MountPath: "/var/lib/myduck"}, "temporary": {Name: "temporary", MountPath: "/tmp"},
		"database-tls":    {Name: "database-tls", MountPath: "/etc/hakopod-tls", ReadOnly: true},
		"database-app":    {Name: "database-app", MountPath: "/etc/hakopod-app", ReadOnly: true},
		"database-config": {Name: "database-config", MountPath: "/etc/hakopod-config", ReadOnly: true},
	}
	if len(container.VolumeMounts) != len(mounts) || len(container.VolumeDevices) != 0 {
		return false
	}
	for _, mount := range container.VolumeMounts {
		expected, ok := mounts[mount.Name]
		if !ok || !reflect.DeepEqual(mount, expected) {
			return false
		}
		delete(mounts, mount.Name)
	}
	if len(p.Spec.Volumes) != 5 {
		return false
	}
	seen := map[string]bool{}
	for _, volume := range p.Spec.Volumes {
		if seen[volume.Name] {
			return false
		}
		seen[volume.Name] = true
		switch volume.Name {
		case "data":
			if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName != "data-database-0" || volume.PersistentVolumeClaim.ReadOnly {
				return false
			}
		case "temporary":
			if volume.EmptyDir == nil || volume.EmptyDir.Medium != "" || volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Cmp(resource.MustParse("128Mi")) != 0 {
				return false
			}
		case "database-tls", "database-app":
			name := "database-tls"
			if volume.Name == "database-app" {
				name = "database-credentials"
			}
			if volume.Secret == nil || volume.Secret.SecretName != name || len(volume.Secret.Items) != 0 || volume.Secret.Optional != nil && *volume.Secret.Optional || volume.Secret.DefaultMode == nil || *volume.Secret.DefaultMode != 0440 {
				return false
			}
		case "database-config":
			if volume.ConfigMap == nil || volume.ConfigMap.Name != "database-myduck-config" || len(volume.ConfigMap.Items) != 0 || volume.ConfigMap.Optional != nil && *volume.ConfigMap.Optional || volume.ConfigMap.DefaultMode == nil || *volume.ConfigMap.DefaultMode != 0440 {
				return false
			}
		default:
			return false
		}
	}
	for _, probe := range []*corev1.Probe{container.ReadinessProbe, container.StartupProbe} {
		if probe == nil || probe.Exec == nil || !reflect.DeepEqual(probe.Exec.Command, []string{"/usr/local/bin/myduckserver", "--managed-check=" + myduckConfigPath}) || probe.HTTPGet != nil || probe.TCPSocket != nil || probe.GRPC != nil || probe.TimeoutSeconds != 10 {
			return false
		}
	}
	return true
}

func (c *Client) observeMyDuckDatabase(ctx context.Context, d database.Resource, o *database.Observation) error {
	if len(o.Members) != 1 || !o.Members[0].Ready {
		return fmt.Errorf("MyDuck requires one ready persistent instance")
	}
	o.Members[0].Role, o.Primary = "primary", o.Members[0].Name
	host := "database." + DatabaseNamespace(d.ID) + ".svc"
	o.Endpoints = []database.Endpoint{{Purpose: "mysql", Host: host, Port: 3306}, {Purpose: "postgresql", Host: host, Port: 5432}}
	return nil
}

func (c *Client) verifyMyDuckTLS(ctx context.Context, d database.Resource, o *database.Observation) error {
	if len(o.Members) != 1 {
		return fmt.Errorf("MyDuck TLS requires one current member")
	}
	step, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output := &databaseBoundedWriter{limit: 4096}
	if err := c.DatabaseExec(step, d, o.Members[0], []string{"/usr/local/bin/myduckserver", "--managed-check=" + myduckConfigPath}, nil, output); err != nil {
		return fmt.Errorf("MyDuck did not pass both authenticated TLS protocol checks")
	}
	// A Secret projection may still contain the previous certificate. Verify
	// the served leaf against the current control-plane identity as well.
	password, identity, err := c.myduckClientIdentity(step, d)
	if err != nil {
		return err
	}
	mysql, err := c.myduckMySQLClient(step, d, o.Members[0], password, identity)
	if err != nil {
		return err
	}
	err = mysql.PingContext(step)
	_ = mysql.Close()
	if err != nil {
		return fmt.Errorf("MyDuck MySQL endpoint has not loaded its current identity")
	}
	postgres, err := c.myduckPostgresClient(step, d, o.Members[0], password, identity)
	if err != nil {
		return fmt.Errorf("MyDuck PostgreSQL endpoint has not loaded its current identity")
	}
	_ = postgres.Close(step)
	return nil
}
