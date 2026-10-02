package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSupabaseOperatorBindingMatchesLiveClusterAndStorageClass(t *testing.T) {
	images := managedplatform.SupabaseReleaseImages()
	parameters := map[string]string{"skuName": "Premium_LRS"}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	binding := SupabaseOperatorBinding{SchemaVersion: 1, Release: managedplatform.SupabaseReleaseQualificationID, ClusterUID: "cluster-uid", StorageClassName: "encrypted-rwo", StorageClassUID: "storage-uid", StorageProvisioner: "disk.csi.azure.com", StorageParametersSHA256: hex.EncodeToString(digest[:]), ImageInventorySHA256: managedplatform.SupabaseImageInventorySHA256(images), ProviderEvidenceSHA256: strings.Repeat("c", 64), EncryptionAtRest: true, Reviewed: true}
	client := &Client{kube: fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cluster-uid")}}, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "encrypted-rwo", UID: types.UID("storage-uid")}, Provisioner: "disk.csi.azure.com", Parameters: parameters})}
	if err = client.ValidateSupabaseOperatorBinding(context.Background(), binding, images, "encrypted-rwo"); err != nil {
		t.Fatal(err)
	}
	arbitrary := managedplatform.SupabaseReleaseImages()
	arbitrary[managedplatform.SupabaseComponentNames()[0]] = "registry.example.test/unqualified@sha256:" + strings.Repeat("a", 64)
	binding.ImageInventorySHA256 = managedplatform.SupabaseImageInventorySHA256(arbitrary)
	if err = client.ValidateSupabaseOperatorBinding(context.Background(), binding, arbitrary, "encrypted-rwo"); err == nil {
		t.Fatal("self-consistent arbitrary image inventory was accepted")
	}
	binding.ImageInventorySHA256 = managedplatform.SupabaseReleaseImageInventorySHA256
	binding.ClusterUID = "other"
	if err = client.ValidateSupabaseOperatorBinding(context.Background(), binding, images, "encrypted-rwo"); err == nil {
		t.Fatal("changed cluster identity was accepted")
	}
	binding.ClusterUID = "cluster-uid"
	class, err := client.kube.StorageV1().StorageClasses().Get(context.Background(), "encrypted-rwo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	class.Parameters["skuName"] = "StandardSSD_LRS"
	if _, err = client.kube.StorageV1().StorageClasses().Update(context.Background(), class, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = client.ValidateSupabaseOperatorBinding(context.Background(), binding, images, "encrypted-rwo"); err == nil {
		t.Fatal("changed StorageClass parameters were accepted")
	}
}
