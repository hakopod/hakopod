package store

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
)

func TestNeonRenderedInventoryHasExactClaimAuthority(t *testing.T) {
	_, _, item, plan := managedPlatformFixture(t)
	images := map[string]string{}
	identities := map[string]managedplatform.NeonRuntimeIdentity{}
	for _, name := range managedplatform.NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = managedplatform.NeonRuntimeIdentity{UID: 10001, GID: 10001}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	input := managedplatform.NeonRenderInput{Spec: item.Spec, PlatformID: item.ID, Revision: 1, NamespaceUID: "contract-fixture", Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 20000, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}}
	for _, topology := range [][2]int{{2, 1}, {8, 6}} {
		input.Spec.Neon.Pageservers = topology[0]
		input.Spec.Neon.ComputeReplicas = topology[1]
		input.Spec.Placement.NodeNames = nil
		for i := 0; i < max(3, topology[0]); i++ {
			input.Spec.Placement.NodeNames = append(input.Spec.Placement.NodeNames, fmt.Sprintf("node-%d", i))
		}
		plan, err = managedplatform.PlanNeon(input.Spec, images)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := managedplatform.RenderNeon(input)
		if err != nil {
			t.Fatal(err)
		}
		op := ManagedPlatformOperation{PlatformID: item.ID, Revision: 1, Spec: input.Spec, Plan: plan}
		for _, object := range manifest.Objects {
			accessor, err := meta.Accessor(object)
			if err != nil {
				t.Fatal(err)
			}
			kind := ""
			switch object.(type) {
			case *corev1.Service:
				kind = "service"
			case *corev1.ConfigMap:
				kind = "configmap"
			case *corev1.PersistentVolumeClaim:
				kind = "pvc"
			case *appsv1.StatefulSet:
				kind = "statefulset"
			case *appsv1.Deployment:
				kind = "deployment"
			case *networkingv1.NetworkPolicy:
				kind = "networkpolicy"
			default:
				t.Fatalf("unreviewed rendered kind %T", object)
			}
			if !managedPlatformClaimComponentAllowed(op, kind+"."+accessor.GetName(), "runtime_component") {
				t.Fatalf("rendered resource has no durable claim authority: %s.%s", kind, accessor.GetName())
			}
		}
		for _, name := range manifest.RequiredSecrets {
			if !managedPlatformClaimComponentAllowed(op, "secret."+name, "runtime_component") {
				t.Fatalf("rendered secret has no durable claim authority: %s", name)
			}
		}
		for _, name := range []string{"secret.neon-controller-callback-r2", "secret.neon-controller-callback-r01", "configmap.neon-controller-database-r2"} {
			if managedPlatformClaimComponentAllowed(op, name, "runtime_component") {
				t.Fatal("another revision acquired claim authority")
			}
		}
	}
}
