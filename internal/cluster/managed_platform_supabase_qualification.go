package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hakopod/hakopod/internal/managedplatform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SupabaseOperatorBinding struct {
	SchemaVersion           int    `toml:"schema_version"`
	Release                 string `toml:"release"`
	ClusterUID              string `toml:"cluster_uid"`
	StorageClassName        string `toml:"storage_class_name"`
	StorageClassUID         string `toml:"storage_class_uid"`
	StorageProvisioner      string `toml:"storage_provisioner"`
	StorageParametersSHA256 string `toml:"storage_parameters_sha256"`
	ImageInventorySHA256    string `toml:"image_inventory_sha256"`
	ProviderEvidenceSHA256  string `toml:"provider_evidence_sha256"`
	EncryptionAtRest        bool   `toml:"encryption_at_rest"`
	Reviewed                bool   `toml:"reviewed"`
}

func (b SupabaseOperatorBinding) Validate(images map[string]string, storageClass string) error {
	if b.SchemaVersion != 1 || b.Release != managedplatform.SupabaseReleaseQualificationID || !b.Reviewed || !b.EncryptionAtRest {
		return fmt.Errorf("Supabase operator qualification is incomplete")
	}
	if b.ClusterUID == "" || b.StorageClassUID == "" || b.StorageProvisioner == "" || b.StorageClassName == "" || b.StorageClassName != storageClass {
		return fmt.Errorf("Supabase operator qualification identity is incomplete")
	}
	for _, value := range []string{b.StorageParametersSHA256, b.ImageInventorySHA256, b.ProviderEvidenceSHA256} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("Supabase operator qualification digest is invalid")
		}
	}
	if b.ImageInventorySHA256 != managedplatform.SupabaseImageInventorySHA256(images) {
		return fmt.Errorf("Supabase operator qualification image inventory changed")
	}
	if !managedplatform.SupabaseReleaseImagesMatch(images) || b.ImageInventorySHA256 != managedplatform.SupabaseReleaseImageInventorySHA256 {
		return fmt.Errorf("Supabase operator qualification does not match the compiled release image inventory")
	}
	return nil
}

func (c *Client) ValidateSupabaseOperatorBinding(ctx context.Context, binding SupabaseOperatorBinding, images map[string]string, storageClass string) error {
	if c == nil || c.kube == nil {
		return fmt.Errorf("Supabase operator qualification requires a Kubernetes client")
	}
	if err := binding.Validate(images, storageClass); err != nil {
		return err
	}
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || string(namespace.UID) != binding.ClusterUID {
		return fmt.Errorf("Supabase operator qualification cluster identity changed")
	}
	class, err := c.kube.StorageV1().StorageClasses().Get(ctx, binding.StorageClassName, metav1.GetOptions{})
	if err != nil || string(class.UID) != binding.StorageClassUID || class.Provisioner != binding.StorageProvisioner {
		return fmt.Errorf("Supabase operator qualification StorageClass identity changed")
	}
	encoded, err := json.Marshal(class.Parameters)
	if err != nil {
		return fmt.Errorf("Supabase operator qualification StorageClass parameters are invalid")
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != binding.StorageParametersSHA256 {
		return fmt.Errorf("Supabase operator qualification StorageClass parameters changed")
	}
	return nil
}
