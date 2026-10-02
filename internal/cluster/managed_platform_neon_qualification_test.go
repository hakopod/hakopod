package cluster

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNeonOperatorBindingFailsClosedBeforeReleaseQualification(t *testing.T) {
	images := map[string]string{}
	for _, name := range managedplatform.NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	binding := NeonOperatorBinding{
		SchemaVersion: 1, Release: managedplatform.NeonReleaseQualificationID,
		ClusterUID: "cluster", StorageClassName: "encrypted", StorageClassUID: "storage",
		StorageProvisioner: "disk.csi.example", StorageParametersSHA256: strings.Repeat("b", 64),
		ImageInventorySHA256: managedplatform.NeonImageInventorySHA256(images), ProviderEvidenceSHA256: strings.Repeat("c", 64),
		SourceArchiveSHA256: managedplatform.NeonReleaseSourceArchiveSHA256, PostgresCommit: managedplatform.NeonReleasePostgresCommit,
		ConsumerPatchID: managedplatform.NeonReleaseConsumerPatchID, EncryptionAtRest: true, Reviewed: true,
	}
	if err := binding.Validate(images, "encrypted"); err == nil || !strings.Contains(err.Error(), "compiled release image inventory") {
		t.Fatalf("unqualified image inventory was accepted: %v", err)
	}
	binding.SourceArchiveSHA256 = strings.Repeat("d", 64)
	if err := binding.Validate(images, "encrypted"); err == nil || !strings.Contains(err.Error(), "source identity") {
		t.Fatalf("changed source identity was accepted: %v", err)
	}
}
