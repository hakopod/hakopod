package backup

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"

	"github.com/pelletier/go-toml/v2"
)

// VitessBackupApproval is operator authority to copy dedicated storage keys to
// one database's native backup jobs. It must never come from a customer request.
// The operator must restrict the keys to this database's bucket or prefix;
// setting a prefix in Hakopod does not restrict the keys at the storage service.
type VitessBackupApproval struct {
	DestinationID        string   `toml:"destination_id" json:"destination_id"`
	Revision             int64    `toml:"revision" json:"revision"`
	Project              string   `toml:"project" json:"project"`
	Environment          string   `toml:"environment" json:"environment"`
	Database             string   `toml:"database" json:"database"`
	DedicatedCredentials bool     `toml:"dedicated_credentials" json:"dedicated_credentials"`
	EndpointCIDRs        []string `toml:"endpoint_cidrs" json:"endpoint_cidrs"`
}

func ValidateVitessBackupApprovals(approvals []VitessBackupApproval) error {
	if len(approvals) > 32 {
		return fmt.Errorf("at most 32 Vitess storage approvals are supported")
	}
	scope := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	seen := map[string]bool{}
	for _, approval := range approvals {
		if !identifierID.MatchString(approval.DestinationID) || approval.Revision < 1 || !approval.DedicatedCredentials || seen[approval.DestinationID] || !scope.MatchString(approval.Project) || !scope.MatchString(approval.Environment) || !scope.MatchString(approval.Database) {
			return fmt.Errorf("Vitess storage approvals require a unique destination revision, exact database scope and dedicated credentials")
		}
		seen[approval.DestinationID] = true
		if len(approval.EndpointCIDRs) < 1 || len(approval.EndpointCIDRs) > 16 {
			return fmt.Errorf("Vitess storage requires 1–16 exact endpoint IPs")
		}
		ips := map[string]bool{}
		for _, raw := range approval.EndpointCIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix != prefix.Masked() || prefix.Bits() != prefix.Addr().BitLen() || !prefix.Addr().IsGlobalUnicast() || prefix.Addr().IsPrivate() || prefix.Addr().Is4In6() || ips[raw] {
				return fmt.Errorf("Vitess storage requires unique public endpoint /32 or /128 addresses")
			}
			ips[raw] = true
		}
	}
	return nil
}

func FindVitessBackupApproval(approvals []VitessBackupApproval, destination string, revision int64, project, environment, name string) (VitessBackupApproval, error) {
	if err := ValidateVitessBackupApprovals(approvals); err != nil {
		return VitessBackupApproval{}, err
	}
	for _, approval := range approvals {
		if approval.DestinationID == destination && approval.Revision == revision && approval.Project == project && approval.Environment == environment && approval.Database == name {
			return approval, nil
		}
	}
	return VitessBackupApproval{}, fmt.Errorf("Vitess native backup storage has not been approved for this database and destination revision")
}

func ReadVitessBackupApprovals(path string) ([]VitessBackupApproval, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open Vitess storage approvals")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("Vitess storage approvals require a regular file of at most 64 KiB that is not writable by group or others")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, fmt.Errorf("cannot read bounded Vitess storage approvals")
	}
	var config struct {
		SchemaVersion int                    `toml:"schema_version"`
		Approvals     []VitessBackupApproval `toml:"approvals"`
	}
	if toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&config) != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("Vitess storage approvals require strict TOML with schema_version = 1")
	}
	if err = ValidateVitessBackupApprovals(config.Approvals); err != nil {
		return nil, err
	}
	return config.Approvals, nil
}
