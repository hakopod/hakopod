package auth

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/management"
	"github.com/hakopod/hakopod/internal/spec"
	"net/http"
	"time"
)

type WorkloadPolicy = cluster.WorkloadPolicy
type WorkloadSpec = spec.Application

type RuntimeConfig struct {
	BuildRegistry    string
	WorkloadPolicy   cluster.WorkloadPolicyResolver
	ApplicationLimit func(context.Context, string, string) (int, error)
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
	kube, err := cluster.New(config.Kubeconfig, cluster.Options{WorkloadPolicy: config.WorkloadPolicy, OperatorNodeLimit: config.NodeLimit, DeploymentMode: cluster.DeploymentManagedCloud, AppDomain: config.AppDomain, IngressClass: config.IngressClass, TLSIssuer: config.TLSIssuer, PublicPort: config.PublicPort, PublicHTTPSPort: config.PublicHTTPSPort, RolloutTimeout: rollout, ApprovedDomains: s.store.ApprovedDomains, RegistrySecretName: s.store.RegistrySecretName, VirtualNetworks: s.store.ResolveVirtualNetworks, ProxyNamespace: config.ProxyNamespace, ProxyConfigMap: config.ProxyConfigMap, ProxyRelease: config.ProxyRelease})
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
	s.store.ApplicationLimit = config.ApplicationLimit
	server := &api.Server{Store: s.store, Cluster: kube, Auth: s.config, OperatorRuntime: true}
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
