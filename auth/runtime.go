package auth

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/management"
	"github.com/hakopod/hakopod/internal/platformconfig"
	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type DatabaseSpec = database.Spec
type DatabasePolicy = cluster.DatabasePolicy
type DatabaseCapacity = database.Capacity
type DatabaseNodeReservation = cluster.DatabaseNodeReservation
type ManagedClusterNode = cluster.ManagedClusterNode
type DatabasePublicEndpointAllocation = database.PublicEndpointAllocation
type DatabasePublicEndpointMemberAllocation = database.PublicEndpointMemberAllocation
type DatabasePublicEndpoint = database.PublicEndpoint
type DatabasePublicEndpointReview = database.PublicEndpointReview
type DatabasePublicEndpointOperation = database.PublicEndpointOperation
type ManagedDatabaseResource = database.Resource
type ManagedPlatformCapacity = managedplatform.Capacity
type ManagedPlatformCapacityNode = managedplatform.CapacityNode
type ManagedPlatformCapacityPolicy = managedplatform.CapacityPolicy

func PublicEndpointAllocations(endpoint DatabasePublicEndpoint) ([]DatabasePublicEndpointMemberAllocation, error) {
	return database.PublicEndpointAllocations(endpoint)
}

// DatabasePublicEndpointAuthority is trusted Cloud infrastructure authority.
// Preflight is read-only. Mutations must invoke the supplied fence immediately
// before every provider write.
type DatabasePublicEndpointAuthority interface {
	Preflight(context.Context, ManagedDatabaseResource, []DatabasePublicEndpointAllocation) (string, error)
	Ensure(context.Context, DatabasePublicEndpointOperation, ManagedDatabaseResource, DatabasePublicEndpoint, func() error) error
	Verify(context.Context, DatabasePublicEndpointOperation, ManagedDatabaseResource, DatabasePublicEndpoint) error
	Delete(context.Context, DatabasePublicEndpointOperation, ManagedDatabaseResource, DatabasePublicEndpoint, func() error) error
	VerifyDeleted(context.Context, DatabasePublicEndpointOperation, ManagedDatabaseResource, DatabasePublicEndpoint) error
}

func ValidateManagedClusterNodes(nodes []ManagedClusterNode) error {
	return cluster.ValidateManagedClusterNodes(nodes)
}

type WorkloadPolicy = cluster.WorkloadPolicy
type PlacementRequest = cluster.PlacementRequest
type WorkloadSpec = spec.Application
type WorkloadService = spec.Service
type ResourceProfile = spec.Profile

// ServiceResourceProfile returns a copy of the engine's per-service defaults.
func ServiceResourceProfile(name string) (ResourceProfile, bool) {
	profile, ok := spec.Profiles[name]
	return profile, ok
}

// ValidateCloudResources shares the engine's bounded resource policy with gateways.
func ValidateCloudResources(s spec.Service) error {
	return spec.ValidateResourceCeiling(s, spec.Profiles["large"])
}

// ValidateRuntimeResources checks resources against a trusted embedding ceiling.
func ValidateRuntimeResources(s WorkloadService, ceiling ResourceProfile) error {
	return spec.ValidateResourceCeiling(s, ceiling)
}

type BackupConfig = api.BackupConfig

type VitessBackupApproval = backup.VitessBackupApproval

func ReadVitessBackupApprovals(path string) ([]VitessBackupApproval, error) {
	return backup.ReadVitessBackupApprovals(path)
}

// AdmissionPrincipal is canonical engine authorization, never client claims.
type AdmissionPrincipal = store.Principal
type DeploymentAdmission = store.DeploymentAdmission

// SlackCloudEvent is a committed, source-bound shared-runtime event. Cloud
// integrations import it from auth; they never import an internal package.
type SlackEventDefinition = slackevents.SlackEventDefinition

// SlackEventCatalog returns the stable event choices shared by Cloud and self-hosted Slack.
func SlackEventCatalog() []SlackEventDefinition { return slackevents.Catalog() }

// ValidSlackEventType permits every public catalog event and the internal test event.
func ValidSlackEventType(event string) bool { return slackevents.Valid(event) }

type SlackCloudEvent = api.SlackCloudEvent
type SlackCloudEventOutcome = api.SlackCloudEventOutcome

const (
	SlackCloudAccepted = api.SlackCloudAccepted
	SlackCloudRetry    = api.SlackCloudRetry
	SlackCloudSkipped  = api.SlackCloudSkipped
)

type SlackCloudEventSink interface {
	Enqueue(context.Context, SlackCloudEvent) (SlackCloudEventOutcome, error)
}
type slackCloudEventSink struct{ sink SlackCloudEventSink }

func (s slackCloudEventSink) Enqueue(ctx context.Context, event api.SlackCloudEvent) (api.SlackCloudEventOutcome, error) {
	return s.sink.Enqueue(ctx, event)
}

type RuntimeConfig struct {
	// ManagedPlatformConfigFile is operator input, never a customer setting.
	ManagedPlatformConfigFile string
	// ClickHouseSandbox requires the operator-installed, attested runtime.
	ClickHouseSandbox bool
	StorageBudgetTx   func(context.Context, pgx.Tx, string, string) (int64, error)
	// ActionsEntitled is trusted product state, never user TOML or headers.
	ActionsEntitled func(context.Context, string, string) (bool, error)
	// SlackCloud is trusted Cloud control-plane authority. It is never derived
	// from a request, node configuration, or customer workload data.
	SlackCloudEvents SlackCloudEventSink
	// SlackCloudSourceID identifies this trusted shared engine process to the
	// Cloud workspace mapper. It must be empty for BYO nodes.
	SlackCloudSourceID            string
	AuthorizeRetainedCleanup      func(context.Context, AdmissionPrincipal, string, string) error
	StorageBudget                 func(context.Context, string, string) (int64, error)
	AuthorizeBackup               func(context.Context, string, string, string) error
	AdmitDeployment               DeploymentAdmission
	AdmitDatabase                 DeploymentAdmission
	AdmitManagedPlatform          DeploymentAdmission
	ComputeBudget                 func(context.Context, pgx.Tx, string, string) (int64, error)
	DatabasePolicy                cluster.DatabasePolicyResolver
	DatabasePlacementPolicy       func(context.Context, string, string) (DatabasePolicy, error)
	DatabaseCapacityBudget        func(context.Context, pgx.Tx, string, string) (DatabaseCapacity, error)
	ManagedPlatformCapacityBudget func(context.Context, pgx.Tx, string, string) (ManagedPlatformCapacityPolicy, error)
	ManagedCapacityPool           func(context.Context, pgx.Tx, string, string) (string, error)
	ManagedClusterNodes           []ManagedClusterNode
	DatabaseNodeReservations      map[string]DatabaseNodeReservation
	CloudResourceCeiling          *ResourceProfile
	Backups                       BackupConfig
	BuildRegistry                 string
	WorkloadPolicy                cluster.WorkloadPolicyResolver
	PlacementPolicy               cluster.PlacementPolicyResolver
	ApplicationLimit              func(context.Context, string, string) (int, error)
	// NodeLimit bounds the private operator cluster. Zero defaults to one.
	NodeLimit    int
	Kubeconfig   string
	AppDomain    string
	IngressClass string
	TLSIssuer    string
	// TLSRedirectDisabled is operator-only for a private ingress behind an
	// HTTPS-enforcing front proxy. Leave false for tenant and self-hosted runtimes.
	TLSRedirectDisabled              bool
	PublicPort                       int
	PublicHTTPSPort                  int
	ProxyNamespace                   string
	ProxyConfigMap                   string
	ProxyRelease                     string
	DatabasePublicAddress            string
	DatabasePublicDomain             string
	DatabasePublicPorts              []int32
	DatabasePublicAuthority          DatabasePublicEndpointAuthority
	DatabasePublicEndpointsQualified bool
}

// StartRuntime shares the canonical database, API and reconciliation lifecycle.
// The embedding product must authorize the returned handler before dispatching
// any request. It never grants customer users access to the operator runtime.
func (s *Service) StartRuntime(ctx context.Context, config RuntimeConfig) (http.Handler, func(), error) {
	if config.Kubeconfig == "" || config.AppDomain == "" {
		return nil, nil, errors.New("configure the internal runtime kubeconfig and application domain")
	}
	if s.config.DeploymentMode != cluster.DeploymentManagedCloud {
		return nil, nil, errors.New("the embedded Cloud runtime requires managed-cloud mode")
	}
	// Match the canonical server defaults while allowing an embedding to select
	// its installed ingress release explicitly.
	if config.ProxyNamespace == "" {
		config.ProxyNamespace = "haproxy-controller"
	}
	if config.ProxyConfigMap == "" {
		config.ProxyConfigMap = "hakopod-ingress-kubernetes-ingress"
	}
	if config.ProxyRelease == "" {
		config.ProxyRelease = "hakopod-ingress"
	}
	rollout := 120 * time.Second
	kube, err := cluster.New(config.Kubeconfig, cluster.Options{ManagedClusterNodes: config.ManagedClusterNodes, DatabasePlacementPolicy: config.DatabasePlacementPolicy, ClickHouseSandbox: config.ClickHouseSandbox, DatabasePolicy: config.DatabasePolicy, WorkloadPolicy: config.WorkloadPolicy, PlacementPolicy: config.PlacementPolicy, CloudResourceCeiling: config.CloudResourceCeiling, OperatorNodeLimit: config.NodeLimit, DeploymentMode: cluster.DeploymentManagedCloud, AppDomain: config.AppDomain, IngressClass: config.IngressClass, TLSIssuer: config.TLSIssuer, TLSRedirectDisabled: config.TLSRedirectDisabled, PublicPort: config.PublicPort, PublicHTTPSPort: config.PublicHTTPSPort, RolloutTimeout: rollout, ApprovedDomains: s.store.ApprovedDomains, RegistrySecretName: s.store.RegistrySecretName, RegistryCredentialNames: s.store.RegistryCredentialNames, VirtualNetworks: s.store.ResolveVirtualNetworks, ProxyNamespace: config.ProxyNamespace, ProxyConfigMap: config.ProxyConfigMap, ProxyRelease: config.ProxyRelease, DatabasePublicAddress: config.DatabasePublicAddress, DatabasePublicDomain: config.DatabasePublicDomain, DatabasePublicPorts: config.DatabasePublicPorts, ManagedDatabasePublicEndpoints: config.DatabasePublicAuthority != nil, ManagedDatabasePublicEndpointsQualified: config.DatabasePublicEndpointsQualified})
	if err != nil {
		return nil, nil, err
	}
	if err = kube.ValidatePublicTCPInstallation(ctx); err != nil {
		return nil, nil, err
	}
	if err := kube.ValidateCloudCapacity(ctx); err != nil {
		return nil, nil, err
	}
	s.runtime = kube
	s.store.ActionsAccess = func(ctx context.Context, project, environment string) error {
		if config.ActionsEntitled == nil {
			return store.ErrLicenseRequired
		}
		allowed, err := config.ActionsEntitled(ctx, project, environment)
		if err != nil {
			return err
		}
		if !allowed {
			return store.ErrLicenseRequired
		}
		return nil
	}
	s.store.ApplicationLimit = config.ApplicationLimit
	s.store.AdmitDeployment = config.AdmitDeployment
	s.store.RequireDatabaseAdmission = true
	s.store.AdmitDatabase = config.AdmitDatabase
	s.store.ComputeBudget = config.ComputeBudget
	s.store.DatabaseCapacityBudget = config.DatabaseCapacityBudget
	s.store.RequireManagedPlatformAdmission = true
	s.store.AdmitManagedPlatform = config.AdmitManagedPlatform
	s.store.ManagedPlatformCapacityBudget = config.ManagedPlatformCapacityBudget
	s.store.ManagedCapacityPool = config.ManagedCapacityPool
	s.store.ValidateManagedPlatformCapacity = s.validateManagedPlatformCapacity
	s.store.StorageBudgetTx = config.StorageBudgetTx
	s.store.AuthorizeBackup = config.AuthorizeBackup
	s.store.StorageBudget = config.StorageBudget
	s.store.AuthorizeRetainedCleanup = config.AuthorizeRetainedCleanup
	if err = s.store.ReconcileManagedCapacityScopes(ctx); err != nil {
		return nil, nil, err
	}
	if err = s.CheckDatabaseNodeReservations(ctx, config.DatabaseNodeReservations); err != nil {
		return nil, nil, err
	}
	if config.SlackCloudEvents != nil && config.SlackCloudSourceID == "" {
		return nil, nil, errors.New("configure a trusted Slack Cloud source ID with the event sink")
	}
	if config.SlackCloudEvents == nil && config.SlackCloudSourceID != "" {
		return nil, nil, errors.New("Slack Cloud source ID requires the event sink")
	}
	if err = s.store.ConfigureSlackCloudEvents(ctx, config.SlackCloudSourceID); err != nil {
		return nil, nil, err
	}
	server := &api.Server{Store: s.store, Cluster: kube, Auth: s.config, OperatorRuntime: true, CloudControlPlane: true, DatabasePublicEndpointAuthority: config.DatabasePublicAuthority}
	if config.SlackCloudEvents != nil {
		server.SlackCloudEvents = slackCloudEventSink{sink: config.SlackCloudEvents}
	}
	if err = platformconfig.Attach(server, config.ManagedPlatformConfigFile, s.config.EncryptionKey, platformconfig.Options{ExternalCapacity: true, CatalogCapacity: s.managedPlatformCatalogCapacity}); err != nil {
		return nil, nil, err
	}
	if err = server.ConfigureBackups(config.Backups); err != nil {
		return nil, nil, err
	}
	if err := server.ConfigureBuildRegistry(ctx, config.BuildRegistry); err != nil {
		return nil, nil, err
	}
	handler, wait := management.Start(ctx, server, config.AppDomain, rollout)
	return handler, wait, nil
}

func (s *Service) validateManagedPlatformCapacity(ctx context.Context, project, environment string, policy managedplatform.CapacityPolicy) error {
	if s.runtime == nil {
		return errors.New("runtime is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owned, err := s.store.ManagedCapacityPoolOwnership(bounded, policy.Pool)
	if err != nil {
		return err
	}
	reservations := managedPlatformNodeReservations(policy, owned)
	return s.runtime.CheckManagedPlatformNodeReservations(bounded, reservations)
}

func managedPlatformNodeReservations(policy managedplatform.CapacityPolicy, owned managedplatform.CapacityPoolOwnership) map[string]cluster.ManagedPlatformNodeReservation {
	reservations := make(map[string]cluster.ManagedPlatformNodeReservation, len(policy.Nodes))
	for _, node := range policy.Nodes {
		reservations[node.Name] = cluster.ManagedPlatformNodeReservation{UID: node.UID, Architecture: node.Architecture, OperatingSystem: node.OperatingSystem, SchedulingPool: policy.SchedulingPool, SchedulingRuntimeClass: policy.SchedulingRuntimeClass, Capacity: policy.Capacity, Ownership: owned}
	}
	return reservations
}

func (s *Service) managedPlatformCatalogCapacity(ctx context.Context, project, environment string) (managedplatform.CapacityPolicy, error) {
	if s.store == nil || s.store.Pool == nil || s.store.ManagedPlatformCapacityBudget == nil {
		return managedplatform.CapacityPolicy{}, errors.New("managed platform workspace capacity is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.store.Pool.Begin(bounded)
	if err != nil {
		return managedplatform.CapacityPolicy{}, err
	}
	defer tx.Rollback(bounded)
	policy, err := s.store.ManagedPlatformCapacityBudget(bounded, tx, project, environment)
	if err != nil {
		return managedplatform.CapacityPolicy{}, err
	}
	if err = policy.Validate(); err != nil {
		return managedplatform.CapacityPolicy{}, err
	}
	return policy, nil
}

func (s *Service) CheckWorkloadPool(ctx context.Context, node, pool, runtime string) error {
	if s.runtime == nil {
		return errors.New("runtime is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.runtime.CheckWorkloadPool(bounded, node, pool, runtime)
}

func (s *Service) CheckDatabaseNodeReservations(ctx context.Context, reservations map[string]DatabaseNodeReservation) error {
	if s.runtime == nil {
		return errors.New("runtime is unavailable")
	}
	prepared := make(map[string]DatabaseNodeReservation, len(reservations))
	for name, reservation := range reservations {
		copy := reservation
		copy.Ownership.Workloads = append([]managedplatform.CapacityPoolWorkload(nil), reservation.Ownership.Workloads...)
		copy.Ownership.PlatformNamespaces = make(map[string]managedplatform.CapacityNamespaceOwnership, len(reservation.Ownership.PlatformNamespaces))
		for namespace, ownership := range reservation.Ownership.PlatformNamespaces {
			copy.Ownership.PlatformNamespaces[namespace] = ownership
		}
		copy.ManagedPlatformNamespaces = make(map[string]managedplatform.CapacityNamespaceOwnership, len(reservation.ManagedPlatformNamespaces))
		for namespace, ownership := range reservation.ManagedPlatformNamespaces {
			cloned := ownership
			cloned.Controllers = make(map[string]string, len(ownership.Controllers))
			for component, uid := range ownership.Controllers {
				cloned.Controllers[component] = uid
			}
			cloned.Workloads = make(map[string]managedplatform.CapacityWorkloadOwnership, len(ownership.Workloads))
			for component, workload := range ownership.Workloads {
				workloadCopy := workload
				workloadCopy.Nodes = make(map[string]managedplatform.Capacity, len(workload.Nodes))
				for node, capacity := range workload.Nodes {
					workloadCopy.Nodes[node] = capacity
				}
				cloned.Workloads[component] = workloadCopy
			}
			copy.ManagedPlatformNamespaces[namespace] = cloned
		}
		prepared[name] = copy
	}
	checkedScopes := map[string]bool{}
	checkedPlatformScopes := map[string]bool{}
	poolOwnership := map[string]managedplatform.CapacityPoolOwnership{}
	for name, reservation := range prepared {
		workloads := map[string]bool{}
		for _, workload := range reservation.Ownership.Workloads {
			workloads[workload.Kind+"\x00"+workload.ID] = true
		}
		if reservation.Ownership.PlatformNamespaces == nil {
			reservation.Ownership.PlatformNamespaces = map[string]managedplatform.CapacityNamespaceOwnership{}
		}
		pools := map[string]bool{}
		for _, scope := range reservation.Scopes {
			project, environment, ok := strings.Cut(scope, "/")
			if !ok || project == "" || environment == "" || !checkedScopes[scope] && len(checkedScopes) >= 64 {
				return errors.New("invalid database capacity scope")
			}
			if !checkedScopes[scope] {
				bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := s.store.CheckDatabaseCapacityReservation(bounded, project, environment)
				cancel()
				if err != nil {
					return err
				}
				checkedScopes[scope] = true
			}
			if s.store.ManagedCapacityPool == nil {
				bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
				owned, ownershipErr := s.store.ManagedCapacityScopeWorkloads(bounded, project, environment)
				cancel()
				if ownershipErr != nil {
					return ownershipErr
				}
				for _, workload := range owned {
					if workload.Kind != "database" {
						continue
					}
					key := workload.Kind + "\x00" + workload.ID
					if !workloads[key] {
						reservation.Ownership.Workloads = append(reservation.Ownership.Workloads, workload)
						workloads[key] = true
					}
				}
				continue
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			pool, err := s.store.ManagedCapacityScopePool(bounded, project, environment)
			cancel()
			if err != nil {
				return err
			}
			if pools[pool] {
				continue
			}
			pools[pool] = true
			owned, loaded := poolOwnership[pool]
			if !loaded {
				bounded, cancel = context.WithTimeout(ctx, 5*time.Second)
				platformScopes, scopeErr := s.store.ManagedCapacityPoolPlatformScopes(bounded, pool)
				cancel()
				if scopeErr != nil {
					return scopeErr
				}
				for _, platformScope := range platformScopes {
					if checkedPlatformScopes[platformScope] {
						continue
					}
					platformProject, platformEnvironment, ok := strings.Cut(platformScope, "/")
					if !ok {
						return errors.New("invalid managed platform capacity scope")
					}
					bounded, cancel = context.WithTimeout(ctx, 5*time.Second)
					err = s.store.CheckManagedPlatformCapacityReservation(bounded, platformProject, platformEnvironment)
					cancel()
					if err != nil {
						return err
					}
					checkedPlatformScopes[platformScope] = true
				}
				bounded, cancel = context.WithTimeout(ctx, 5*time.Second)
				owned, err = s.store.ManagedCapacityPoolOwnership(bounded, pool)
				cancel()
				if err != nil {
					return err
				}
				poolOwnership[pool] = owned
			}
			for _, workload := range owned.Workloads {
				key := workload.Kind + "\x00" + workload.ID
				if !workloads[key] {
					reservation.Ownership.Workloads = append(reservation.Ownership.Workloads, workload)
					workloads[key] = true
				}
			}
			for namespace, ownership := range owned.PlatformNamespaces {
				reservation.Ownership.PlatformNamespaces[namespace] = ownership
			}
		}
		prepared[name] = reservation
	}
	return s.runtime.CheckDatabaseNodeReservations(ctx, prepared)
}
