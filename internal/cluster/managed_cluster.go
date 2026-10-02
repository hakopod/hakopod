package cluster

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ManagedClusterNode identifies an operator-enrolled member. A replacement with
// the same name has a different UID and needs a reviewed configuration update.
type ManagedClusterNode struct {
	Name            string `json:"name" toml:"name"`
	UID             string `json:"uid" toml:"uid"`
	Architecture    string `json:"architecture,omitempty" toml:"architecture"`
	OperatingSystem string `json:"operating_system,omitempty" toml:"operating_system"`
}

func ValidateManagedClusterNodes(nodes []ManagedClusterNode) error {
	if len(nodes) < 1 || len(nodes) > database.MaxMembers {
		return fmt.Errorf("managed cluster inventory requires 1–%d nodes", database.MaxMembers)
	}
	seen := map[string]bool{}
	uids := map[string]bool{}
	for _, n := range nodes {
		if len(validation.IsDNS1123Subdomain(n.Name)) != 0 || n.UID == "" || len(n.UID) > 128 || len(validation.IsDNS1123Subdomain(n.UID)) != 0 || seen[n.Name] || uids[n.UID] {
			return fmt.Errorf("managed cluster inventory requires distinct node names and UIDs")
		}
		if (n.Architecture != "" && len(validation.IsQualifiedName(n.Architecture)) != 0) || (n.OperatingSystem != "" && len(validation.IsQualifiedName(n.OperatingSystem)) != 0) {
			return fmt.Errorf("managed cluster node platform is invalid")
		}
		seen[n.Name], uids[n.UID] = true, true
	}
	return nil
}

func ReadManagedClusterNodes(path string) ([]ManagedClusterNode, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, fmt.Errorf("managed cluster inventory must be a regular file of at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(b) > 64<<10 {
		return nil, fmt.Errorf("cannot read managed cluster inventory")
	}
	var config struct {
		SchemaVersion int                  `toml:"schema_version"`
		Nodes         []ManagedClusterNode `toml:"nodes"`
	}
	decoder := toml.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("managed cluster inventory requires strict schema_version 1")
	}
	if err := ValidateManagedClusterNodes(config.Nodes); err != nil {
		return nil, err
	}
	slices.SortFunc(config.Nodes, func(a, b ManagedClusterNode) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return config.Nodes, nil
}

func managedClusterMatches(approved []ManagedClusterNode, nodes []corev1.Node) bool {
	if ValidateManagedClusterNodes(approved) != nil || len(nodes) != len(approved) {
		return false
	}
	for _, n := range nodes {
		if !slices.ContainsFunc(approved, func(a ManagedClusterNode) bool {
			return a.Name == n.Name && a.UID == string(n.UID) && (a.Architecture == "" || a.Architecture == n.Status.NodeInfo.Architecture) && (a.OperatingSystem == "" || a.OperatingSystem == n.Status.NodeInfo.OperatingSystem)
		}) {
			return false
		}
	}
	return true
}
