package auth

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/management"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"time"
)

type WorkloadPolicy = cluster.WorkloadPolicy
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

// AdmissionPrincipal is canonical engine authorization, never client claims.
type AdmissionPrincipal = store.Principal
type DeploymentAdmission = store.DeploymentAdmission

type RuntimeConfig struct {
	// ActionsEntitled is trusted product state, never user TOML or headers.
	ActionsEntitled          func(context.Context, string, string) (bool, error)
	AuthorizeRetainedCleanup func(context.Context, AdmissionPrincipal, string, string) error
	StorageBudget            func(context.Context, string, string) (int64, error)
	AuthorizeBackup          func(context.Context, string, string, string) error
	AdmitDeployment          DeploymentAdmission
	CloudResourceCeiling     *ResourceProfile
	Backups                  BackupConfig
	BuildRegistry            string
	WorkloadPolicy           cluster.WorkloadPolicyResolver
	ApplicationLimit         func(context.Context, string, string) (int, error)
	// NodeLimit bounds the private operator cluster. Zero defaults to one.
	NodeLimit       int
	Kubeconfig      string
	AppDomain       string
	IngressClass    string
	TLSIssuer       string
	PublicPort      int
	PublicHTTPSPort int
	ProxyNamespace  string
	ProxyConfigMap  string
	ProxyRelease    string
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
	kube, err := cluster.New(config.Kubeconfig, cluster.Options{WorkloadPolicy: config.WorkloadPolicy, CloudResourceCeiling: config.CloudResourceCeiling, OperatorNodeLimit: config.NodeLimit, DeploymentMode: cluster.DeploymentManagedCloud, AppDomain: config.AppDomain, IngressClass: config.IngressClass, TLSIssuer: config.TLSIssuer, PublicPort: config.PublicPort, PublicHTTPSPort: config.PublicHTTPSPort, RolloutTimeout: rollout, ApprovedDomains: s.store.ApprovedDomains, RegistrySecretName: s.store.RegistrySecretName, RegistryCredentialNames: s.store.RegistryCredentialNames, VirtualNetworks: s.store.ResolveVirtualNetworks, ProxyNamespace: config.ProxyNamespace, ProxyConfigMap: config.ProxyConfigMap, ProxyRelease: config.ProxyRelease})
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
	s.store.AuthorizeBackup = config.AuthorizeBackup
	s.store.StorageBudget = config.StorageBudget
	s.store.AuthorizeRetainedCleanup = config.AuthorizeRetainedCleanup
	server := &api.Server{Store: s.store, Cluster: kube, Auth: s.config, OperatorRuntime: true, CloudControlPlane: true}
	server.ConfigureBackups(config.Backups)
	if err := server.ConfigureBuildRegistry(ctx, config.BuildRegistry); err != nil {
		return nil, nil, err
	}
	handler, wait := management.Start(ctx, server, config.AppDomain, rollout)
	return handler, wait, nil
}

func (s *Service) CheckWorkloadPool(ctx context.Context, node, pool, runtime string) error {
	if s.runtime == nil {
		return errors.New("runtime is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.runtime.CheckWorkloadPool(bounded, node, pool, runtime)
}
