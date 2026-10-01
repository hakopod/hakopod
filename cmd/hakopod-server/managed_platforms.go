package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/pelletier/go-toml/v2"
)

var managedPlatformSnapshotName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,38}[a-z0-9])?-r[1-9][0-9]*$`)

const (
	managedPlatformConfigMaxBytes      = 1 << 20
	managedPlatformMaxIdentities       = managedplatform.MaxComponents
	managedPlatformMaxSecretSnapshots  = 64
	managedPlatformMaxSecretValues     = 64
	managedPlatformMaxSecretValueBytes = 64 << 10
	managedPlatformMaxSecretBytes      = 512 << 10
)

type managedPlatformFile struct {
	SchemaVersion                 int                                                `toml:"schema_version"`
	ApprovedEncryptedStorageClass string                                             `toml:"approved_encrypted_storage_class"`
	SharedStorageGID              int64                                              `toml:"shared_storage_gid"`
	ApprovedExternalHTTPSCIDRs    []string                                           `toml:"approved_external_https_cidrs"`
	Images                        map[string]string                                  `toml:"images"`
	Identities                    map[string]managedplatform.RuntimeIdentity         `toml:"identities"`
	NeonImages                    map[string]string                                  `toml:"neon_images"`
	NeonIdentities                map[string]managedplatform.NeonRuntimeIdentity     `toml:"neon_identities"`
	NeonProxyControlPlaneOrigin   string                                             `toml:"neon_proxy_control_plane_origin"`
	NeonControlPlaneNamespace     string                                             `toml:"neon_control_plane_namespace"`
	NeonControlPlanePodLabels     map[string]string                                  `toml:"neon_control_plane_pod_labels"`
	NeonProxyToken                string                                             `toml:"neon_proxy_token"`
	NeonProxyEndpoints            map[string]api.NeonProxyBootstrapConfig            `toml:"neon_proxy_endpoints"`
	Secrets                       map[string]map[string]map[string]map[string]string `toml:"secrets"`
	Capacity                      managedplatform.CapacityPolicy                     `toml:"capacity"`
}

func validateManagedPlatformInventories(config managedPlatformFile) (bool, bool, error) {
	supabaseConfigured := len(config.Images) > 0 || len(config.Identities) > 0
	neonConfigured := len(config.NeonImages) > 0 || len(config.NeonIdentities) > 0 || config.NeonProxyControlPlaneOrigin != "" || config.NeonControlPlaneNamespace != "" || len(config.NeonControlPlanePodLabels) > 0 || config.NeonProxyToken != "" || len(config.NeonProxyEndpoints) > 0
	if !supabaseConfigured && !neonConfigured {
		return false, false, fmt.Errorf("managed platform runtime requires at least one complete platform inventory")
	}
	if supabaseConfigured {
		if err := managedplatform.ValidateImages(config.Images, managedplatform.SupabaseComponentNames()); err != nil {
			return false, false, fmt.Errorf("managed platform runtime images: %w", err)
		}
		components := managedplatform.SupabaseComponentNames()
		if len(config.Identities) != len(components) || len(config.Identities) > managedPlatformMaxIdentities {
			return false, false, fmt.Errorf("managed platform runtime identities must contain the complete supported inventory")
		}
		for _, name := range components {
			identity, ok := config.Identities[name]
			if !ok {
				return false, false, fmt.Errorf("managed platform runtime identity %q is unavailable", name)
			}
			if identity.UID < 1 || identity.GID < 1 {
				return false, false, fmt.Errorf("managed platform runtime identity %q requires positive UID and GID", name)
			}
		}
	}
	if neonConfigured {
		if err := managedplatform.ValidateImages(config.NeonImages, managedplatform.NeonComponents()); err != nil {
			return false, false, fmt.Errorf("Neon runtime images: %w", err)
		}
		components := managedplatform.NeonComponents()
		if len(config.NeonIdentities) != len(components) || len(config.NeonIdentities) > managedPlatformMaxIdentities {
			return false, false, fmt.Errorf("Neon runtime identities must contain the complete supported inventory")
		}
		for _, name := range components {
			identity, ok := config.NeonIdentities[name]
			if !ok || identity.UID < 1 || identity.GID < 1 {
				return false, false, fmt.Errorf("Neon runtime identity %q is unavailable", name)
			}
		}
		if err := managedplatform.ValidateHTTPSOrigin(config.NeonProxyControlPlaneOrigin, "Neon proxy control-plane origin"); err != nil {
			return false, false, err
		}
		if err := managedplatform.ValidateNeonNetworkTrust(config.NeonControlPlaneNamespace, config.NeonControlPlanePodLabels, config.ApprovedExternalHTTPSCIDRs); err != nil {
			return false, false, err
		}
	}
	return supabaseConfigured, neonConfigured, nil
}

func configureManagedPlatforms(path string, db *store.Store, kube *cluster.Client, encodedKey string) (*api.NativeManagedPlatformPlanner, *cluster.ManagedPlatformRuntime, api.NeonProxyAuthority, error) {
	if path == "" {
		return nil, nil, nil, nil
	}
	if db == nil {
		return nil, nil, nil, fmt.Errorf("managed platform runtime requires PostgreSQL storage")
	}
	if kube == nil {
		return nil, nil, nil, fmt.Errorf("managed platform runtime requires a Kubernetes client")
	}
	config, err := loadManagedPlatformFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	_, neonConfigured, err := validateManagedPlatformInventories(config)
	if err != nil {
		return nil, nil, nil, err
	}
	key := decodeManagedPlatformKey(encodedKey)
	if len(key) != 32 {
		return nil, nil, nil, fmt.Errorf("managed platform runtime requires the persistent authentication encryption key")
	}
	resolver := func(_ context.Context, _ store.Principal, item store.ManagedPlatform, _ string, ref managedplatform.SecretReference) (map[string][]byte, error) {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		project, ok := config.Secrets[item.Project]
		if !ok {
			return nil, fmt.Errorf("configured immutable secret snapshot is unavailable for project")
		}
		environment, ok := project[item.Environment]
		if !ok {
			return nil, fmt.Errorf("configured immutable secret snapshot is unavailable for environment")
		}
		name := fmt.Sprintf("%s-r%d", ref.Name, ref.Revision)
		values, ok := environment[name]
		if !ok {
			return nil, fmt.Errorf("configured immutable secret snapshot %s is unavailable", name)
		}
		out := make(map[string][]byte, len(values))
		for key, value := range values {
			out[key] = []byte(value)
		}
		return out, nil
	}
	if len(config.NeonProxyEndpoints) > 64 {
		return nil, nil, nil, fmt.Errorf("Neon proxy endpoint configuration exceeds its bound")
	}
	for scope, value := range config.NeonProxyEndpoints {
		parts := strings.Split(scope, "/")
		if len(scope) > 191 || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, nil, nil, fmt.Errorf("Neon proxy bootstrap scope must be project/environment/name")
		}
		if err := api.ValidateNeonProxyBootstrap(value); err != nil {
			return nil, nil, nil, fmt.Errorf("Neon proxy bootstrap %s: %w", scope, err)
		}
	}
	planner := &api.NativeManagedPlatformPlanner{Store: db, EncryptionKey: key, SupabaseImages: config.Images, SupabaseIdentities: config.Identities, ApprovedEncryptedStorageClass: config.ApprovedEncryptedStorageClass, SharedStorageGID: config.SharedStorageGID, ApprovedExternalHTTPSCIDRs: config.ApprovedExternalHTTPSCIDRs, ResolveSupabaseSecret: resolver, NeonImages: config.NeonImages, NeonIdentities: config.NeonIdentities, NeonProxyControlPlaneOrigin: config.NeonProxyControlPlaneOrigin, NeonControlPlaneNamespace: config.NeonControlPlaneNamespace, NeonControlPlanePodLabels: config.NeonControlPlanePodLabels, NeonProxyToken: config.NeonProxyToken, NeonProxyEndpoints: config.NeonProxyEndpoints, ResolveNeonSecret: resolver}
	if config.Capacity.Enabled {
		if err = config.Capacity.Validate(); err != nil {
			return nil, nil, nil, err
		}
		if config.Capacity.StorageClass != config.ApprovedEncryptedStorageClass {
			return nil, nil, nil, fmt.Errorf("managed platform capacity must use the approved encrypted StorageClass")
		}
		db.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
			return config.Capacity, nil
		}
		db.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) {
			return config.Capacity.Pool, nil
		}
		bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.ReconcileManagedCapacityScopes(bounded)
		cancel()
		if err != nil {
			return nil, nil, nil, err
		}
		bounded, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		scopes, scopeErr := db.ManagedPlatformCapacityScopes(bounded)
		cancel()
		if scopeErr != nil {
			return nil, nil, nil, scopeErr
		}
		owned := map[string]managedplatform.CapacityNamespaceOwnership{}
		for _, scope := range scopes {
			project, environment, _ := strings.Cut(scope, "/")
			bounded, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			scopeErr = db.CheckManagedPlatformCapacityReservation(bounded, project, environment)
			cancel()
			if scopeErr != nil {
				return nil, nil, nil, scopeErr
			}
			bounded, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			namespaces, ownershipErr := db.ManagedPlatformNamespaceReservations(bounded, project, environment)
			cancel()
			if ownershipErr != nil {
				return nil, nil, nil, ownershipErr
			}
			for namespace, uid := range namespaces {
				owned[namespace] = uid
			}
		}
		reservations := make(map[string]cluster.ManagedPlatformNodeReservation, len(config.Capacity.Nodes))
		for _, node := range config.Capacity.Nodes {
			reservations[node.Name] = cluster.ManagedPlatformNodeReservation{UID: node.UID, Capacity: config.Capacity.Capacity, Namespaces: owned}
		}
		bounded, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		err = kube.CheckManagedPlatformNodeReservations(bounded, reservations)
		cancel()
		if err != nil {
			return nil, nil, nil, err
		}
		db.ValidateManagedPlatformCapacity = func(ctx context.Context, project, environment string, policy managedplatform.CapacityPolicy) error {
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			namespaces, ownershipErr := db.ManagedPlatformNamespaceReservations(bounded, project, environment)
			if ownershipErr != nil {
				return ownershipErr
			}
			current := make(map[string]cluster.ManagedPlatformNodeReservation, len(policy.Nodes))
			for _, node := range policy.Nodes {
				current[node.Name] = cluster.ManagedPlatformNodeReservation{UID: node.UID, Capacity: policy.Capacity, Namespaces: namespaces}
			}
			return kube.CheckManagedPlatformNodeReservations(bounded, current)
		}
	}
	var authority api.NeonProxyAuthority
	if neonConfigured {
		authority, err = api.NewDatabaseNeonProxyAuthority(db, config.NeonProxyToken, key)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return planner, &cluster.ManagedPlatformRuntime{Cluster: kube, EncryptionKey: key}, authority, nil
}

func loadManagedPlatformFile(path string) (managedPlatformFile, error) {
	var config managedPlatformFile
	before, err := os.Lstat(path)
	if err != nil {
		return config, fmt.Errorf("read managed platform runtime configuration: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return config, fmt.Errorf("managed platform runtime configuration must be a regular file")
	}
	if before.Mode().Perm() != 0600 {
		return config, fmt.Errorf("managed platform runtime configuration must have mode 0600")
	}
	if before.Size() > managedPlatformConfigMaxBytes {
		return config, fmt.Errorf("managed platform runtime configuration exceeds 1 MiB")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return config, fmt.Errorf("open managed platform runtime configuration: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		syscall.Close(fd)
		return config, fmt.Errorf("open managed platform runtime configuration")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return config, fmt.Errorf("inspect managed platform runtime configuration: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return config, fmt.Errorf("managed platform runtime configuration changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, managedPlatformConfigMaxBytes+1))
	if err != nil {
		return config, fmt.Errorf("read managed platform runtime configuration: %w", err)
	}
	if len(body) > managedPlatformConfigMaxBytes {
		return config, fmt.Errorf("managed platform runtime configuration exceeds 1 MiB")
	}
	decoder := toml.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode managed platform runtime configuration: %w", err)
	}
	if config.SchemaVersion != 1 {
		return config, fmt.Errorf("managed platform runtime configuration requires schema_version 1")
	}
	if err := validateManagedPlatformSecrets(config.Secrets); err != nil {
		return config, err
	}
	return config, nil
}

func validateManagedPlatformSecrets(scopes map[string]map[string]map[string]map[string]string) error {
	snapshots, total := 0, 0
	for project, environments := range scopes {
		if project == "" {
			return fmt.Errorf("managed platform secret snapshots require a project scope")
		}
		for environment, names := range environments {
			if environment == "" {
				return fmt.Errorf("managed platform secret snapshots require an environment scope")
			}
			for name, values := range names {
				snapshots++
				if snapshots > managedPlatformMaxSecretSnapshots {
					return fmt.Errorf("managed platform runtime has too many secret snapshots")
				}
				if !managedPlatformSnapshotName.MatchString(name) {
					return fmt.Errorf("managed platform secret snapshot name is not an immutable name-revision reference")
				}
				if len(values) == 0 || len(values) > managedPlatformMaxSecretValues {
					return fmt.Errorf("managed platform secret snapshot %q has an unsupported value count", name)
				}
				for key, value := range values {
					if key == "" || len(value) > managedPlatformMaxSecretValueBytes {
						return fmt.Errorf("managed platform secret snapshot %q contains an invalid value", name)
					}
					total += len(value)
					if total > managedPlatformMaxSecretBytes {
						return fmt.Errorf("managed platform runtime secret snapshots exceed the total byte limit")
					}
				}
			}
		}
	}
	return nil
}

func decodeManagedPlatformKey(value string) []byte {
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString} {
		if key, err := decode(value); err == nil && len(key) == 32 {
			return key
		}
	}
	return nil
}
