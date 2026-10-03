package managedplatform

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestNeonRecoveryStorageRenderingPreservesAcceptedSpec(t *testing.T) {
	spec := neonCandidateSpec()
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 1000, GID: 1000}
	}
	input := NeonRenderInput{Spec: spec, PreviousSpec: &spec, PlatformID: strings.Repeat("a", 32), Revision: 2, NamespaceUID: "namespace-uid", Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 1000, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}}
	acceptedPrefix := spec.Neon.ObjectStoragePrefix
	recoveryPrefix := acceptedPrefix + "/recovery/" + strings.Repeat("b", 32)
	for _, prefix := range []string{"", recoveryPrefix, recoveryPrefix + "/"} {
		input.RecoveryStoragePrefix = prefix
		manifests, err := RenderNeon(input)
		if err != nil {
			t.Fatal(err)
		}
		wanted := strings.TrimSuffix(prefix, "/")
		if wanted == "" {
			wanted = acceptedPrefix
		}
		pageservers, safekeepers := 0, 0
		for _, object := range manifests.Objects {
			switch value := object.(type) {
			case *corev1.ConfigMap:
				if !strings.HasPrefix(value.Name, "neon-pageserver-") {
					continue
				}
				var config struct {
					Remote struct {
						Prefix string `toml:"prefix_in_bucket"`
					} `toml:"remote_storage"`
				}
				if err := toml.Unmarshal([]byte(value.Data["pageserver.toml"]), &config); err != nil {
					t.Fatal(err)
				}
				if config.Remote.Prefix != wanted+"/pageserver" {
					t.Fatal("pageserver uses a different restore prefix")
				}
				pageservers++
			case *appsv1.StatefulSet:
				if !strings.HasPrefix(value.Name, "neon-safekeeper-") {
					continue
				}
				found := false
				for _, arg := range value.Spec.Template.Spec.Containers[0].Args {
					if !strings.HasPrefix(arg, "--remote-storage=") {
						continue
					}
					var remote struct {
						Storage struct {
							Prefix string `toml:"prefix_in_bucket"`
						} `toml:"storage"`
					}
					if err := toml.Unmarshal([]byte("storage="+strings.TrimPrefix(arg, "--remote-storage=")), &remote); err != nil {
						t.Fatal(err)
					}
					if remote.Storage.Prefix != "/"+wanted+"/"+strings.TrimPrefix(value.Name, "neon-") {
						t.Fatal("safekeeper uses a different restore prefix")
					}
					found = true
				}
				if !found {
					t.Fatal("safekeeper has no remote storage")
				}
				safekeepers++
			}
		}
		if pageservers != spec.Neon.Pageservers || safekeepers != spec.Neon.Safekeepers {
			t.Fatal("restore did not bind every storage writer")
		}
		if spec.Neon.ObjectStoragePrefix != acceptedPrefix || !reflect.DeepEqual(input.Spec, *input.PreviousSpec) {
			t.Fatal("restore rendering mutated the accepted specification")
		}
	}
	for _, invalid := range []string{
		"other/recovery/" + strings.Repeat("b", 32),
		acceptedPrefix + "-other/recovery/" + strings.Repeat("b", 32),
		acceptedPrefix + "/recovery/../outside",
		acceptedPrefix + "/recovery/" + strings.Repeat("b", 31),
		acceptedPrefix + "/recovery/" + strings.Repeat("B", 32),
		recoveryPrefix + "//",
		recoveryPrefix + "/nested",
	} {
		input.RecoveryStoragePrefix = invalid
		if _, err := RenderNeon(input); err == nil {
			t.Fatal("restore accepted a foreign or malformed storage prefix")
		}
	}
	changed := *spec.Neon
	changed.ObjectStoragePrefix = "changed"
	input.Spec.Neon = &changed
	input.RecoveryStoragePrefix = "changed/recovery/" + strings.Repeat("b", 32)
	if _, err := RenderNeon(input); err == nil {
		t.Fatal("recovery prefix bypassed immutable storage configuration")
	}
}
