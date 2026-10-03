package cluster

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNeonAuthenticationSnapshotKeepsUserConfigurationAndNoSigner(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("old")}
	}
	compute := request.SecretSnapshots["compute-auth-r1"]
	compute["config.json"] = []byte(`{"spec":{"storage_auth_token":"old","mode":"Replica","cluster":{"settings":[]}},"compute_ctl_config":{}}`)
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err != nil {
		t.Fatal(err)
	}
	if string(compute["token"]) != "old" || !bytes.Contains(compute["config.json"], []byte(`"old"`)) {
		t.Fatal("source secret map mutated")
	}
	if string(request.SecretSnapshots["compute-auth-r1"]["token"]) != "old" {
		t.Fatal("compute control credential changed")
	}
	if !bytes.Contains(request.SecretSnapshots["compute-auth-r1"]["config.json"], []byte(`"Replica"`)) {
		t.Fatal("compute mode changed")
	}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth"} {
		data := request.SecretSnapshots[logical+"-r1"]
		if len(data["public-key.pem"]) == 0 || bytes.Equal(data["token"], []byte("old")) {
			t.Fatal("native credentials were not replaced")
		}
		for name, value := range data {
			if strings.Contains(name, "private") || bytes.Contains(value, []byte("PRIVATE KEY")) {
				t.Fatal("signing key entered snapshot")
			}
		}
	}
}

func TestNeonAuthenticationSnapshotRefusesSharedReferencesWithoutMutation(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{
		"controller-auth": {Name: "shared", Revision: 1}, "pageserver-auth": {Name: "shared", Revision: 1},
		"compute-auth": {Name: "shared", Revision: 1},
	}}}, SecretSnapshots: map[string]map[string][]byte{"shared-r1": {"token": []byte("unchanged")}}}
	request.SecretSnapshots["shared-r1"]["config.json"] = []byte(`{"spec":{"cluster":{"settings":[]}},"compute_ctl_config":{}}`)
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err == nil || !strings.Contains(err.Error(), "separate secret references") {
		t.Fatal("shared secret reference accepted")
	}
	if !reflect.DeepEqual(request.SecretSnapshots, map[string]map[string][]byte{"shared-r1": {"token": []byte("unchanged"), "config.json": []byte(`{"spec":{"cluster":{"settings":[]}},"compute_ctl_config":{}}`)}}) {
		t.Fatal("rejected request changed snapshots")
	}
}

func TestNeonComputeTemplateRejectsMalformedClusterBeforeAcceptance(t *testing.T) {
	for _, raw := range []string{`{"spec":{}}`, `{"spec":{"cluster":null}}`, `{"spec":{"cluster":{"settings":"invalid"}}}`, `{"spec":{"cluster":{"settings":[1]}}}`, `{"spec":{"cluster":{"settings":[{}]}}}`, `{"spec":{"cluster":{}}}`, `{"spec":{"cluster":{}},"compute_ctl_config":[]}`, strings.Repeat("x", 65537)} {
		if ValidateNeonComputeTemplate([]byte(raw)) == nil {
			t.Fatal("malformed compute template accepted")
		}
	}
	if err := ValidateNeonComputeTemplate([]byte(`{"spec":{"cluster":{"settings":[{"name":"shared_buffers","value":"128MB","vartype":"string"}]}},"compute_ctl_config":{}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestNeonAuthenticationSnapshotFailureIsAtomic(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("unchanged")}
	}
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err == nil {
		t.Fatal("missing compute spec accepted")
	}
	for _, snapshot := range request.SecretSnapshots {
		if len(snapshot) != 1 || string(snapshot["token"]) != "unchanged" {
			t.Fatal("failed preparation changed original snapshots")
		}
	}
}

func TestNeonDeleteAuthenticationDoesNotRequireComputeTemplate(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("unchanged")}
	}
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "delete"); err != nil {
		t.Fatal(err)
	}
	if string(request.SecretSnapshots["controller-auth-r1"]["token"]) == "unchanged" {
		t.Fatal("delete snapshot lacks native storage authority")
	}
	if !reflect.DeepEqual(request.SecretSnapshots["compute-auth-r1"], map[string][]byte{"token": []byte("unchanged")}) {
		t.Fatal("delete snapshot changed compute configuration")
	}
}
