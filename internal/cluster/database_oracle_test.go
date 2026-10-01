package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestOracleRuntimeSecurityAndPlacement(t *testing.T) {
	d := oracleFixture()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	if object.GetKind() != "StatefulSet" {
		t.Fatal("Oracle must use an owned workload")
	}
	podMap, _, _ := unstructured.NestedMap(object.Object, "spec", "template", "spec")
	var pod corev1.PodSpec
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(podMap, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || len(pod.Containers) != 1 || pod.Containers[0].Image != database.OracleFreeImage || *pod.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("Oracle workload security boundary is incomplete")
	}
	if pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser == 0 {
		t.Fatal("Oracle must not run as root")
	}
	if pod.Containers[0].Resources.Limits.StorageEphemeral().Value() != 2<<30 {
		t.Fatal("Oracle diagnostic and writable-layer storage must be bounded")
	}
	hook := false
	for _, mount := range pod.Containers[0].VolumeMounts {
		if mount.MountPath == "/opt/oracle/scripts/startup/99-hakopod-ready.sh" && mount.SubPath == "ready.sh" && mount.ReadOnly {
			hook = true
		}
	}
	if !hook || oracleConfiguration(d)["app-quota-gib"] != "2" {
		t.Fatal("Oracle must wait for image initialization and reserve system storage")
	}
	applyDatabasePolicy(object, d.Spec, DatabasePolicy{NodeName: "worker", Pool: "free", RuntimeClass: "runsc", StorageClass: "block"})
	applyDatabasePlacement(object, d.Spec, []string{"worker"})
	runtimeName, _, _ := unstructured.NestedString(object.Object, "spec", "template", "spec", "runtimeClassName")
	affinity, _, _ := unstructured.NestedMap(object.Object, "spec", "template", "spec", "affinity")
	if runtimeName != "runsc" || len(affinity) == 0 {
		t.Fatal("Oracle escaped the authorized sandbox or node selection")
	}
	if strings.Contains(oracleStart, "-password pass:") || strings.Contains(oracleStart, "set -x") || !strings.Contains(oracleStart, "SQLNET.INBOUND_CONNECT_TIMEOUT") {
		t.Fatal("Oracle startup must bound transport and keep credentials out of command arguments and tracing")
	}
	if err = (&Client{}).prepareOracleSecurity(context.Background(), database.Resource{Spec: database.Spec{Engine: "postgresql"}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestOracleImportDoesNotRequireCaptureLockPrivileges(t *testing.T) {
	query := oraclePumpSQL("HP_TEST", "HP_TEST.dmp", "IMPORT")
	if strings.Contains(query, "DBMS_LOCK") || strings.Contains(query, "DBMS_FLASHBACK") {
		t.Fatal("schema import must compile with application privileges, without export-only administration packages")
	}
	export := oraclePumpSQL("HP_TEST", "HP_TEST.dmp", "EXPORT")
	if !strings.Contains(export, "DBMS_LOCK.REQUEST") || strings.Count(export, "DBMS_LOCK.RELEASE") != 2 {
		t.Fatal("capture must hold its DDL guard and release it on success or failure")
	}
}

func TestOracleDiagnosticCodesDoNotExposeSQLOrCredentials(t *testing.T) {
	for _, test := range []struct {
		output string
		want   string
	}{
		{"connect app/\"private-password\"\nORA-39083: CREATE USER x IDENTIFIED BY private-password\nORA-39083 duplicate\nPLS-00201: private-sql\nSP2-0640: private-dsn", " (ORA-39083, PLS-00201, SP2-0640)"},
		{"SELECT secret FROM private_table; password=never-print-this", ""},
		{"prefixORA-12345 ORA-123456 ORA-123 ORA-12345suffix", ""},
		{"ORA-31685 SQL and private data HAKOPOD_OBJECT=TABLE HAKOPOD_OBJECT=PRIVATE_PASSWORD HAKOPOD_OBJECT=TABLE", " (ORA-31685, object type TABLE)"},
		{"ORA-00001 ORA-00002 ORA-00003 ORA-00004 ORA-00005 ORA-00006 ORA-00007 ORA-00008 ORA-00009", " (ORA-00001, ORA-00002, ORA-00003, ORA-00004, ORA-00005, ORA-00006, ORA-00007, ORA-00008)"},
	} {
		if got := oracleErrorSuffix(test.output); got != test.want {
			t.Fatalf("unexpected sanitized diagnostics: %q", got)
		}
	}
}
