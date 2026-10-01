package api

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
)

func (s *Server) resolveVitessBackup(approvals []backup.VitessBackupApproval) cluster.VitessBackupResolver {
	return func(ctx context.Context, d database.Resource) (cluster.VitessBackupStorage, error) {
		if d.Spec.Vitess == nil || s.Backups == nil {
			return cluster.VitessBackupStorage{}, fmt.Errorf("Vitess storage is unavailable")
		}
		ref := d.Spec.Vitess
		approval, err := backup.FindVitessBackupApproval(approvals, ref.BackupDestinationID, ref.BackupDestinationRevision, d.Project, d.Environment, d.Spec.Name)
		if err != nil {
			return cluster.VitessBackupStorage{}, err
		}
		destination, err := s.Store.BackupDestination(ctx, ref.BackupDestinationID)
		if err != nil || destination.Revision != ref.BackupDestinationRevision || destination.Project != d.Project || destination.Environment != d.Environment {
			return cluster.VitessBackupStorage{}, fmt.Errorf("Vitess storage scope or revision changed")
		}
		credentials, err := backup.OpenCredentials(s.Backups.CredentialKey, destination)
		if err != nil {
			return cluster.VitessBackupStorage{}, fmt.Errorf("Vitess storage credentials could not be opened")
		}
		// Archive recovery keys never enter the namespace or native operator.
		credentials.EncryptionIdentity = ""
		storage := cluster.VitessBackupStorage{DatabaseID: d.ID, Dedicated: true, Destination: destination, Credentials: credentials}
		storage.Destination.EncryptedCredentials = nil
		for _, raw := range approval.EndpointCIDRs {
			storage.ApprovedEndpointCIDRs = append(storage.ApprovedEndpointCIDRs, netip.MustParsePrefix(raw))
		}
		return storage, storage.Validate(d)
	}
}
