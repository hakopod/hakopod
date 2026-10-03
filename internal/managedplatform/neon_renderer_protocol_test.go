package managedplatform

import (
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

func TestNeonStorageConfigurationUsesAuthenticatedUpstreamProtocols(t *testing.T) {
	spec := neonCandidateSpec()
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 1000, GID: 1000}
	}
	platformID := strings.Repeat("a", 32)
	manifests, err := RenderNeon(NeonRenderInput{Spec: spec, PlatformID: platformID, Revision: 1, NamespaceUID: "namespace-uid", Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 1000, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
	if err != nil {
		t.Fatal(err)
	}
	pageservers, callbacks := 0, 0
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *corev1.ConfigMap:
			if !strings.HasPrefix(value.Name, "neon-pageserver-") {
				continue
			}
			pageservers++
			var config struct {
				HTTPAuth string `toml:"http_auth_type"`
				PGAuth   string `toml:"pg_auth_type"`
				TLS      bool   `toml:"enable_tls_page_service_api"`
				Upcall   string `toml:"control_plane_api"`
				Postgres string `toml:"pg_distrib_dir"`
				Remote   struct {
					Prefix string `toml:"prefix_in_bucket"`
				} `toml:"remote_storage"`
			}
			if err := toml.Unmarshal([]byte(value.Data["pageserver.toml"]), &config); err != nil {
				t.Fatal(err)
			}
			if config.HTTPAuth != "NeonJWT" || config.PGAuth != "NeonJWT" || !config.TLS {
				t.Fatal("pageserver inherited upstream trust or plaintext defaults")
			}
			if config.Upcall != "https://neon-storage-controller:6699/upcall/v1/" || config.Postgres != "/usr/local" {
				t.Fatal("pageserver cannot find its controller upcall or installed PostgreSQL")
			}
			if config.Remote.Prefix != spec.Neon.ObjectStoragePrefix+"/pageserver" {
				t.Fatal("pageserver replacement cannot see the same remote layers")
			}
		case *appsv1.Deployment:
			if value.Name != "neon-storage-controller" {
				continue
			}
			container := value.Spec.Template.Spec.Containers[0]
			if !slices.Contains(container.Args, "--control-plane-url=https://control.example.test/api/v1/internal/neon/storage-controller/"+platformID+"/1") {
				t.Fatal("controller callback is not scoped to the platform revision")
			}
			for _, env := range container.Env {
				if env.Name == "CONTROL_PLANE_JWT_TOKEN" {
					if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name != NeonControllerCallbackSecretName(1) || env.ValueFrom.SecretKeyRef.Key != "token" {
						t.Fatal("controller callback credential is not a generated secret reference")
					}
					callbacks++
				}
			}
		case *appsv1.StatefulSet:
			container := value.Spec.Template.Spec.Containers[0]
			if strings.HasPrefix(value.Name, "neon-safekeeper-") || strings.HasPrefix(value.Name, "neon-pageserver-") || strings.HasPrefix(value.Name, "neon-compute-") {
				found := false
				for _, env := range container.Env {
					if env.Name == "NEON_STORAGE_CA_FILE" && env.Value == "/var/run/secrets/hakopod/safekeeper-auth/ca.crt" {
						found = true
					}
				}
				if !found {
					t.Fatal("storage client is missing its explicit safekeeper CA")
				}
			}
			if strings.HasPrefix(value.Name, "neon-safekeeper-") && (!slices.Contains(container.Args, "--enable-tls-wal-service-api") || !slices.Contains(container.Args, "--use-https-safekeeper-api")) {
				t.Fatal("safekeeper data or peer connection can use plaintext")
			}
			if strings.HasPrefix(value.Name, "neon-pageserver-") {
				if !strings.Contains(strings.Join(container.Args, " "), "/controller-auth/upcall-token") {
					t.Fatal("pageserver upcall uses another service's authentication token")
				}
				found := false
				for _, env := range container.Env {
					if env.Name == "NEON_AUTH_TOKEN" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == neonSecretName(spec.Secrets["safekeeper-auth"]) {
						found = true
					}
				}
				if !found {
					t.Fatal("pageserver WAL receiver has no safekeeper credential")
				}
			}
		case *networkingv1.NetworkPolicy:
			if value.Name != "neon-storage-controller-internal" {
				continue
			}
			found := false
			for _, rule := range value.Spec.Egress {
				if len(rule.To) == 1 && rule.To[0].NamespaceSelector != nil && rule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "hakopod-system" {
					found = rule.To[0].PodSelector != nil && rule.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] == "hakopod-server" && len(rule.Ports) == 1 && rule.Ports[0].Port != nil && rule.Ports[0].Port.IntVal == 443
				}
			}
			if !found {
				t.Fatal("controller cannot deliver HTTPS notifications to the owned control plane")
			}
		}
	}
	if pageservers != spec.Neon.Pageservers || callbacks != 1 {
		t.Fatal("missing storage configuration or controller callback")
	}
}
