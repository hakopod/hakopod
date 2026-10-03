package managedplatform

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestRenderNeonEncryptsBrokerAndControllerDatabaseTraffic(t *testing.T) {
	spec := neonCandidateSpec()
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 70, GID: 70}
	}
	manifests, err := RenderNeon(NeonRenderInput{Spec: spec, PlatformID: strings.Repeat("a", 32), Revision: 1, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 70, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
	if err != nil {
		t.Fatal(err)
	}
	var broker, storageController *appsv1.Deployment
	var database *appsv1.StatefulSet
	var databaseConfig *corev1.ConfigMap
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *appsv1.Deployment:
			if value.Name == "neon-broker" {
				broker = value
			}
			if value.Name == "neon-storage-controller" {
				storageController = value
			}
		case *appsv1.StatefulSet:
			if value.Name == "neon-controller-database" {
				database = value
			}
		case *corev1.ConfigMap:
			if value.Name == "neon-controller-database-r1" {
				databaseConfig = value
			}
			if strings.HasPrefix(value.Name, "neon-pageserver-") && !strings.Contains(value.Data["pageserver.toml"], "broker_endpoint='https://neon-broker:50051'") {
				t.Fatal("pageserver broker client is not TLS protected")
			}
		}
	}
	if broker == nil || !slices.Contains(broker.Spec.Template.Spec.Containers[0].Args, "--listen-https-addr=0.0.0.0:50051") || slices.Contains(broker.Spec.Template.Spec.Containers[0].Args, "--listen-addr=0.0.0.0:50051") {
		t.Fatal("broker retained a plaintext listener")
	}
	if database == nil || databaseConfig == nil {
		t.Fatal("controller database TLS configuration is incomplete")
	}
	db := database.Spec.Template.Spec.Containers[0]
	joined := strings.Join(db.Args, " ")
	if !strings.Contains(joined, "chmod 0600 /tmp/controller-database.key") || !strings.Contains(joined, "ssl=on") || !strings.Contains(joined, "unix_socket_directories=/tmp") || !strings.Contains(databaseConfig.Data["pg_hba.conf"], "hostnossl all all 0.0.0.0/0 reject") || !strings.Contains(databaseConfig.Data["pg_hba.conf"], "hostssl storage_controller storage_controller ::/0 scram-sha-256") {
		t.Fatal("controller database does not require TLS while preserving local bootstrap")
	}
	if storageController == nil {
		t.Fatal("storage controller is missing")
	}
	controller := storageController.Spec.Template.Spec.Containers[0]
	environment := map[string]string{}
	for _, value := range controller.Env {
		environment[value.Name] = value.Value
	}
	if environment["DATABASE_URL"] != "" || environment["STORCON_DB_CERT_CHECKS"] != "1" || environment["SSL_CERT_FILE"] != "/var/run/secrets/hakopod/controller-database-password/ca.crt" {
		t.Fatal("storage controller does not require verified database TLS")
	}
	var databaseURLBound bool
	for _, value := range controller.Env {
		if value.Name == "DATABASE_URL" {
			databaseURLBound = value.ValueFrom != nil && value.ValueFrom.SecretKeyRef != nil && value.ValueFrom.SecretKeyRef.Name == NeonControllerCallbackSecretName(1) && value.ValueFrom.SecretKeyRef.Key == "database-url"
		}
	}
	if !databaseURLBound {
		t.Fatal("controller database connection is not bound to the generated credential snapshot")
	}
	for _, volume := range storageController.Spec.Template.Spec.Volumes {
		if volume.Name == "controller-database-password" && (volume.Secret == nil || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "ca.crt") {
			t.Fatal("storage controller received database private key material")
		}
	}
	for _, object := range manifests.Objects {
		set, ok := object.(*appsv1.StatefulSet)
		if ok && (strings.HasPrefix(set.Name, "neon-safekeeper-") || strings.HasPrefix(set.Name, "neon-pageserver-")) {
			for _, volume := range set.Spec.Template.Spec.Volumes {
				if volume.Name == "broker-auth" && (volume.Secret == nil || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "ca.crt") {
					t.Fatal("broker private key was projected into a client")
				}
			}
		}
		if ok && strings.HasPrefix(set.Name, "neon-safekeeper-") && !slices.Contains(set.Spec.Template.Spec.Containers[0].Args, "--broker-endpoint=https://neon-broker:50051") {
			t.Fatal("safekeeper broker client is not TLS protected")
		}
	}
}

func TestRenderNeonMountsOnlyControlPlaneCAIntoProxy(t *testing.T) {
	spec := neonCandidateSpec()
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 70, GID: 70}
	}
	ca := neonTestControlPlaneCAPEM(t)
	input := NeonRenderInput{Spec: spec, PlatformID: strings.Repeat("a", 32), Revision: 1, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 70, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: ca, ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}}
	manifests, err := RenderNeon(input)
	if err != nil {
		t.Fatal(err)
	}
	var config *corev1.ConfigMap
	var proxy *appsv1.Deployment
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *corev1.ConfigMap:
			if value.Name == "neon-proxy-control-plane-ca-r1" {
				config = value
			}
		case *appsv1.Deployment:
			if value.Name == "neon-proxy" {
				proxy = value
			}
		}
	}
	if config == nil || config.Immutable == nil || !*config.Immutable || len(config.Data) != 1 || config.Data["ca.crt"] != ca {
		t.Fatal("proxy control-plane CA ConfigMap is not exact and immutable")
	}
	if proxy == nil {
		t.Fatal("proxy deployment is missing")
	}
	container := proxy.Spec.Template.Spec.Containers[0]
	environment := map[string]string{}
	for _, value := range container.Env {
		environment[value.Name] = value.Value
	}
	if environment["SSL_CERT_FILE"] != "/etc/hakopod-control-plane/ca.crt" {
		t.Fatal("proxy does not use the pinned control-plane CA")
	}
	var found bool
	for _, volume := range proxy.Spec.Template.Spec.Volumes {
		if volume.Name == "control-plane-ca" {
			found = volume.ConfigMap != nil && volume.ConfigMap.Name == config.Name && len(volume.ConfigMap.Items) == 1 && volume.ConfigMap.Items[0].Key == "ca.crt" && volume.ConfigMap.Items[0].Path == "ca.crt"
		}
	}
	if !found {
		t.Fatal("proxy CA volume exposes more than ca.crt or references the wrong revision")
	}
	if strings.Contains(strings.Join(container.Args, " "), ca) || strings.Contains(strings.Join(container.Args, " "), "PRIVATE KEY") {
		t.Fatal("proxy arguments leaked certificate or private-key bodies")
	}
}

func TestRenderNeonRejectsMissingAndInvalidControlPlaneCA(t *testing.T) {
	for name, value := range map[string]string{"missing": "", "invalid": "not a certificate", "private key": "-----BEGIN PRIVATE KEY-----\nZmFrZQ==\n-----END PRIVATE KEY-----\n"} {
		t.Run(name, func(t *testing.T) {
			err := ValidateNeonControlPlaneCABundle(value)
			if err == nil || value != "" && strings.Contains(err.Error(), value) {
				t.Fatalf("unsafe CA bundle result: %v", err)
			}
		})
	}
}
