package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var oracleDatabaseResource = schema.GroupVersionResource{Group: "database.oracle.com", Version: "v4", Resource: "singleinstancedatabases"}

//go:embed database_oracle_start.sh
var oracleStart string

const oracleReadyHook = "#!/bin/bash\nset -eu\numask 077\ntouch /tmp/hakopod/bootstrap-ready\n"

func oracleConfiguration(d database.Resource) map[string]string {
	return map[string]string{"start.sh": oracleStart, "ready.sh": oracleReadyHook, "app-quota-gib": strconv.FormatInt(database.OracleFreeQuotaGiB(d.Spec.StorageGiB), 10)}
}

func oracleRuntimeSupported(s database.Spec) error {
	if s.Oracle == nil || s.Oracle.Edition != "free" || s.Mode != "standalone" {
		return fmt.Errorf("Oracle Enterprise and Data Guard runtime acceptance is not complete")
	}
	return nil
}

// The Free operator profile uses this fixed pod contract. It is constructed
// by Go, never accepted as a user-supplied pod template.
func oracleFreeWorkloadSpec(d database.Resource, resources map[string]any) map[string]any {
	resources["requests"].(map[string]any)["ephemeral-storage"] = "256Mi"
	resources["limits"].(map[string]any)["ephemeral-storage"] = "2Gi"
	security := map[string]any{"runAsNonRoot": true, "runAsUser": int64(54321), "runAsGroup": int64(54321), "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	labels := map[string]any{databaseOwner: d.ID, managedBy: "hakopod"}
	claims := []any{}
	for _, name := range []string{"data", "backup"} {
		claims = append(claims, map[string]any{"metadata": map[string]any{"name": name, "labels": labels}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}})
	}
	return map[string]any{
		"replicas": int64(1), "serviceName": "database", "selector": map[string]any{"matchLabels": labels}, "volumeClaimTemplates": claims,
		"template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{
			"automountServiceAccountToken": false, "terminationGracePeriodSeconds": int64(120),
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(54321), "runAsGroup": int64(54321), "fsGroup": int64(54321), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "oracle", "image": d.Spec.Oracle.ContainerImage(), "imagePullPolicy": "IfNotPresent", "command": []any{"bash", "/etc/hakopod-config/start.sh"}, "resources": resources, "securityContext": security,
				"ports": []any{map[string]any{"name": "tcps", "containerPort": int64(2484)}},
				"volumeMounts": []any{
					map[string]any{"name": "data", "mountPath": "/opt/oracle/oradata"}, map[string]any{"name": "backup", "mountPath": "/opt/oracle/hakopod-backup"},
					map[string]any{"name": "database-tls", "mountPath": "/etc/hakopod-tls", "readOnly": true}, map[string]any{"name": "database-access", "mountPath": "/etc/hakopod-oracle", "readOnly": true},
					map[string]any{"name": "database-app", "mountPath": "/etc/hakopod-app", "readOnly": true}, map[string]any{"name": "database-config", "mountPath": "/etc/hakopod-config", "readOnly": true},
					map[string]any{"name": "database-config", "mountPath": "/opt/oracle/scripts/startup/99-hakopod-ready.sh", "subPath": "ready.sh", "readOnly": true},
					map[string]any{"name": "diagnostics", "mountPath": "/opt/oracle/diag"},
					map[string]any{"name": "temporary", "mountPath": "/tmp"}, map[string]any{"name": "shm", "mountPath": "/dev/shm"}},
				"readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"bash", "-c", "test -f /tmp/hakopod/ready && /opt/oracle/checkDBStatus.sh >/dev/null 2>&1"}}, "timeoutSeconds": int64(10), "periodSeconds": int64(10)},
				"startupProbe":   map[string]any{"exec": map[string]any{"command": []any{"test", "-f", "/tmp/hakopod/ready"}}, "periodSeconds": int64(10), "failureThreshold": int64(210)},
			}},
			"volumes": []any{
				map[string]any{"name": "database-tls", "secret": map[string]any{"secretName": "database-tls", "defaultMode": int64(0440)}},
				map[string]any{"name": "database-access", "secret": map[string]any{"secretName": "database-oracle-access", "defaultMode": int64(0440)}},
				map[string]any{"name": "database-app", "secret": map[string]any{"secretName": "database-credentials", "defaultMode": int64(0440)}},
				map[string]any{"name": "database-config", "configMap": map[string]any{"name": "database-oracle-config", "defaultMode": int64(0550)}},
				map[string]any{"name": "diagnostics", "emptyDir": map[string]any{"sizeLimit": "512Mi"}},
				map[string]any{"name": "temporary", "emptyDir": map[string]any{"sizeLimit": "256Mi"}}, map[string]any{"name": "shm", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "1Gi"}},
			},
		}},
	}
}

func (c *Client) prepareOracleSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if oracleEnterprise(d.Spec) {
		return c.renewOracleEnterpriseSecurity(ctx, d, before)
	}
	if d.Spec.Engine != "oracle" {
		return nil
	}
	if err := oracleRuntimeSupported(d.Spec); err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" || ns.UID == "" {
		return fmt.Errorf("Oracle namespace ownership changed")
	}
	secrets := c.kube.CoreV1().Secrets(ns.Name)
	access, err := secrets.Get(ctx, "database-oracle-access", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		data := map[string][]byte{}
		for _, name := range []string{"admin", "wallet"} {
			value := make([]byte, 32)
			if _, err = rand.Read(value); err != nil {
				return err
			}
			data[name] = []byte(hex.EncodeToString(value))
		}
		access = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-oracle-access"), Immutable: ptr(true), Data: data}
		if err = before(); err != nil {
			return err
		}
		access, err = secrets.Create(ctx, access, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(access, d, ns.UID); err != nil {
		return err
	}
	for _, key := range []string{"admin", "wallet"} {
		if len(access.Data[key]) != 64 {
			return fmt.Errorf("Oracle internal credential is invalid")
		}
		if _, err = hex.DecodeString(string(access.Data[key])); err != nil {
			return fmt.Errorf("Oracle internal credential is invalid")
		}
	}
	meta := databaseIdentityMeta(d, ns.UID, "database-oracle-config")
	desiredConfig := oracleConfiguration(d)
	configs := c.kube.CoreV1().ConfigMaps(ns.Name)
	config, err := configs.Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = configs.Create(ctx, &corev1.ConfigMap{ObjectMeta: meta, Data: desiredConfig}, metav1.CreateOptions{})
	} else if err == nil {
		if !mongodbSupportOwned(config, d, ns.UID) {
			return fmt.Errorf("Oracle startup configuration ownership changed")
		}
		if !reflect.DeepEqual(config.Data, desiredConfig) {
			config.Data = desiredConfig
			if err = before(); err != nil {
				return err
			}
			_, err = configs.Update(ctx, config, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return err
	}
	return c.renewOracleFreeWorkload(ctx, d, before)
}

func oracleIdentityFingerprint(identity *corev1.Secret) string {
	return fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(oracleStart+oracleReadyHook), identity.Data["tls.crt"]...), identity.Data["ca.crt"]...)))
}
