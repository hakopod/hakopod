package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reuse this observation's owned tablet inventory and read its claims once.
// Each tablet was checked immediately before exec; the final inventory check
// rejects replacements or changed volume references before health is published.
func (c *Client) verifyVitessStorage(ctx context.Context, d database.Resource, members []database.Member, inventory *vitessObservationInventory) error {
	if inventory == nil || d.Spec.Engine != "vitess" || inventory.database.ID != d.ID || inventory.database.Revision != d.Revision || len(members) == 0 || len(members) != d.Spec.Members() || len(members) != len(inventory.tablets) || len(members) > database.MaxMembers {
		return fmt.Errorf("Vitess storage member inventory is unavailable")
	}
	required := make(map[string]bool, len(members))
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		pod, _, err := inventory.tablet(member)
		if err != nil || seen[member.Name] {
			return fmt.Errorf("Vitess storage member changed")
		}
		seen[member.Name] = true
		volumes := 0
		for _, volume := range pod.Spec.Volumes {
			claim := volume.PersistentVolumeClaim
			if claim == nil {
				continue
			}
			if claim.ClaimName == "" || claim.ReadOnly || required[claim.ClaimName] {
				return fmt.Errorf("Vitess tablet storage reference is invalid")
			}
			required[claim.ClaimName] = true
			volumes++
		}
		if volumes != 1 {
			return fmt.Errorf("Vitess tablet requires one persistent data volume")
		}
	}
	ns := DatabaseNamespace(d.ID)
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database,planetscale.com/component=vttablet", Limit: database.MaxMembers + 1})
	if err != nil || claims.Continue != "" || len(claims.Items) > database.MaxMembers || len(claims.Items) != len(required) {
		return fmt.Errorf("Vitess storage inventory is incomplete or exceeds its bound")
	}
	expectedSize := resource.MustParse(fmt.Sprintf("%dGi", d.Spec.StorageGiB))
	for _, claim := range claims.Items {
		if !required[claim.Name] || claim.Namespace != ns || claim.UID == "" || claim.DeletionTimestamp != nil || claim.Labels["planetscale.com/cluster"] != "database" || claim.Labels["planetscale.com/component"] != "vttablet" || claim.Status.Phase != corev1.ClaimBound || len(claim.Status.Conditions) != 0 || len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
			return fmt.Errorf("Vitess tablet storage is not ready")
		}
		size := claim.Status.Capacity[corev1.ResourceStorage]
		if size.Cmp(expectedSize) < 0 {
			return fmt.Errorf("Vitess tablet storage has not reached its requested size")
		}
		delete(required, claim.Name)
	}
	if len(required) != 0 {
		return fmt.Errorf("Vitess tablet storage is missing")
	}
	return nil
}
