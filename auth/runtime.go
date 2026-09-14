package auth

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/management"
	"net/http"
	"time"
)

type RuntimeConfig struct {
	Kubeconfig      string
	AppDomain       string
	IngressClass    string
	TLSIssuer       string
	PublicPort      int
	PublicHTTPSPort int
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
	rollout := 120 * time.Second
	kube, err := cluster.New(config.Kubeconfig, cluster.Options{DeploymentMode: cluster.DeploymentManagedCloud, AppDomain: config.AppDomain, IngressClass: config.IngressClass, TLSIssuer: config.TLSIssuer, PublicPort: config.PublicPort, PublicHTTPSPort: config.PublicHTTPSPort, RolloutTimeout: rollout, ApprovedDomains: s.store.ApprovedDomains, RegistrySecretName: s.store.RegistrySecretName, VirtualNetworks: s.store.ResolveVirtualNetworks})
	if err != nil {
		return nil, nil, err
	}
	if err = kube.ValidatePublicTCPInstallation(ctx); err != nil {
		return nil, nil, err
	}
	caps, err := kube.CloudCapabilities(ctx)
	if err != nil {
		return nil, nil, err
	}
	if caps.NodeCount != 1 || !caps.NodeCountComplete {
		return nil, nil, errors.New("the internal runtime requires exactly one registered node")
	}
	server := &api.Server{Store: s.store, Cluster: kube, Auth: s.config}
	handler, wait := management.Start(ctx, server, config.AppDomain, rollout)
	return handler, wait, nil
}
