package cluster

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var managedActionsSpecialNetworks = []string{"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "168.63.129.16/32", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}

func validateGitLabRuntimeDestinations(runtime GitLabActionsRuntime) error {
	if len(runtime.PrivateDestinations) > 16 || len(runtime.RegistryCAs) > 8 {
		return errors.New("GitLab runtime network destinations exceed their bound")
	}
	origins := map[string]bool{}
	coordinator, _ := url.Parse(runtime.TransportPolicy.CoordinatorURL)
	coordinatorOrigin := coordinator.Scheme + "://" + coordinator.Host
	origins[coordinatorOrigin] = true
	for _, origin := range runtime.TransportPolicy.ArtifactOrigins {
		origins[origin] = true
	}
	if runtime.Cache != nil {
		origins["https://"+runtime.Cache.ServerAddress] = true
	}
	for _, image := range []string{runtime.Images.Manager, runtime.Images.Helper, runtime.Images.DefaultJobImage} {
		ref, err := parseReference(image)
		if err != nil {
			return err
		}
		host := strings.SplitN(ref.canonical, "/", 2)[0]
		if host == "docker.io" {
			host = "registry-1.docker.io"
		}
		origins["https://"+host] = true
	}
	registrySeen := map[string]bool{}
	for _, ca := range runtime.RegistryCAs {
		host, err := managedActionsOrigin("https://" + ca.Registry)
		if err != nil || host != ca.Registry || registrySeen[host] || len(ca.CAPEM) == 0 {
			return errors.New("GitLab registry trust requires unique canonical host and port destinations")
		}
		if host == coordinator.Host {
			return errors.New("GitLab registry and coordinator trust must remain separate")
		}
		if err := validateGitLabCA(ca.CAPEM); err != nil {
			return err
		}
		registrySeen[host] = true
		origins["https://"+host] = true
	}
	seenOrigins, seenAddresses := map[string]bool{}, map[string]bool{}
	var coordinatorAddresses []string
	for _, grant := range runtime.PrivateDestinations {
		_, err := managedActionsOrigin(grant.Origin)
		if err != nil || !origins[grant.Origin] || seenOrigins[grant.Origin] || len(grant.Addresses) < 1 || len(grant.Addresses) > 8 {
			return errors.New("GitLab private grants require a distinct configured HTTPS origin and one to eight exact hosts")
		}
		seenOrigins[grant.Origin] = true
		for _, raw := range grant.Addresses {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() != 32 || prefix != prefix.Masked() || seenAddresses[raw] {
				return errors.New("GitLab private grants require distinct private IPv4 /32 addresses")
			}
			for _, reserved := range managedActionsSpecialNetworks {
				if netip.MustParsePrefix(reserved).Overlaps(prefix) {
					return errors.New("GitLab private grant intersects a reserved destination")
				}
			}
			seenAddresses[raw] = true
		}
		if grant.Origin == coordinatorOrigin {
			coordinatorAddresses = grant.Addresses
		}
	}
	if runtime.ControlPlaneTrust != nil {
		for _, networks := range [][]string{runtime.ClusterPodCIDRs, runtime.ClusterServiceCIDRs} {
			if len(networks) < 1 || len(networks) > 16 {
				return errors.New("GitLab original trust must retain bounded cluster networks")
			}
			for _, raw := range networks {
				prefix, err := netip.ParsePrefix(raw)
				if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
					return errors.New("GitLab original cluster networks are invalid")
				}
			}
		}
		policy := runtime.ControlPlaneTrust
		target := actions.ProviderTarget{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: runtime.TransportPolicy.CoordinatorURL, ProjectID: 1, TrustPolicy: policy.Name}}
		if policy.BaseURL != runtime.TransportPolicy.CoordinatorURL || !bytes.Equal(policy.CAPEM, runtime.CAPEM) || !slices.Equal(policy.AllowedPrivateCIDRs, coordinatorAddresses) || len(policy.DeniedCIDRs) != 0 {
			return errors.New("GitLab original control-plane trust must match its coordinator grant")
		}
		if err := actions.ValidateGitLabTrustPolicy(target, policy); err != nil {
			return err
		}
	} else if len(runtime.PrivateDestinations) != 0 || len(runtime.RegistryCAs) != 0 {
		return errors.New("GitLab private grants require original installation trust")
	}
	return nil
}

type managedActionsNetworkCache struct {
	mu     sync.Mutex
	at     time.Time
	denied []string
	key    string
}

// ManagedActionsDeniedNetworks uses a short bounded cache shared by every
// native API client. No stale inventory is used after a failed refresh.
func (c *Client) ManagedActionsDeniedNetworks(ctx context.Context) ([]string, error) {
	if c.options.ManagedActions == nil {
		return nil, errors.New("managed actions installation network configuration is unavailable")
	}
	return c.GitLabActionsDeniedNetworks(ctx, GitLabActionsRuntime{ClusterPodCIDRs: c.options.ManagedActions.PodCIDRs, ClusterServiceCIDRs: c.options.ManagedActions.ServiceCIDRs})
}

func (c *Client) GitLabActionsDeniedNetworks(ctx context.Context, runtime GitLabActionsRuntime) ([]string, error) {
	podCIDRs, serviceCIDRs := slices.Clone(runtime.ClusterPodCIDRs), slices.Clone(runtime.ClusterServiceCIDRs)
	if c.options.ManagedActions != nil {
		podCIDRs = append(podCIDRs, c.options.ManagedActions.PodCIDRs...)
		serviceCIDRs = append(serviceCIDRs, c.options.ManagedActions.ServiceCIDRs...)
	}
	if len(podCIDRs) == 0 || len(serviceCIDRs) == 0 {
		return nil, errors.New("managed actions original cluster networks are unavailable")
	}
	key := strings.Join(podCIDRs, ",") + ";" + strings.Join(serviceCIDRs, ",")
	cache := &c.managedActionsNetworks
	if !cache.mu.TryLock() {
		return nil, errors.New("managed actions network inventory is refreshing")
	}
	defer cache.mu.Unlock()
	if cache.key == key && !cache.at.IsZero() && time.Since(cache.at) < 5*time.Second {
		return slices.Clone(cache.denied), nil
	}
	cache.at, cache.denied = time.Time{}, nil
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	denied := slices.Clone(managedActionsSpecialNetworks)
	denied = append(denied, podCIDRs...)
	denied = append(denied, serviceCIDRs...)
	appendAddress := func(raw string) error {
		address, err := netip.ParseAddr(raw)
		if err != nil {
			return errors.New("managed actions cluster address inventory is invalid")
		}
		denied = append(denied, netip.PrefixFrom(address.Unmap(), address.Unmap().BitLen()).String())
		return nil
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1025})
	if err != nil || nodes.Continue != "" || len(nodes.Items) < 1 || len(nodes.Items) > 1024 {
		return nil, errors.New("managed actions cannot verify bounded current node inventory")
	}
	for _, node := range nodes.Items {
		for _, raw := range node.Spec.PodCIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, errors.New("managed actions node Pod CIDR is invalid")
			}
			covered := false
			for _, configured := range podCIDRs {
				parent := netip.MustParsePrefix(configured)
				covered = covered || (parent.Bits() <= prefix.Bits() && parent.Contains(prefix.Addr()))
			}
			if !covered {
				return nil, errors.New("managed actions configured Pod CIDRs do not cover current nodes")
			}
			denied = append(denied, raw)
		}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP {
				if err := appendAddress(address.Address); err != nil {
					return nil, err
				}
			}
		}
	}
	allocatedServiceCIDRs, err := c.kube.NetworkingV1().ServiceCIDRs().List(ctx, metav1.ListOptions{Limit: 33})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.New("managed actions cannot verify current Service CIDRs")
	}
	if err == nil {
		if allocatedServiceCIDRs.Continue != "" || len(allocatedServiceCIDRs.Items) > 32 {
			return nil, errors.New("managed actions Service CIDR inventory exceeds its bound")
		}
		for _, item := range allocatedServiceCIDRs.Items {
			for _, raw := range item.Spec.CIDRs {
				prefix, err := netip.ParsePrefix(raw)
				if err != nil {
					return nil, errors.New("managed actions Service CIDR is invalid")
				}
				covered := false
				for _, configured := range serviceCIDRs {
					parent := netip.MustParsePrefix(configured)
					covered = covered || (parent.Bits() <= prefix.Bits() && parent.Contains(prefix.Addr()))
				}
				if !covered {
					return nil, errors.New("managed actions configured Service CIDRs do not cover current allocation")
				}
				denied = append(denied, raw)
			}
		}
	}
	apiService, err := c.kube.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil || len(apiService.Spec.ClusterIPs) < 1 {
		return nil, errors.New("managed actions cannot verify the Kubernetes Service address")
	}
	for _, address := range apiService.Spec.ClusterIPs {
		if err := appendAddress(address); err != nil {
			return nil, err
		}
	}
	apiURL, err := url.Parse(c.kubeAPIHost())
	if err != nil || apiURL.Hostname() == "" {
		return nil, errors.New("managed actions cannot verify the Kubernetes API address")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", apiURL.Hostname())
	if err != nil || len(addresses) < 1 || len(addresses) > 16 {
		return nil, errors.New("managed actions cannot resolve the bounded Kubernetes API inventory")
	}
	for _, address := range addresses {
		if err := appendAddress(address.String()); err != nil {
			return nil, err
		}
	}
	slices.Sort(denied)
	denied = slices.Compact(denied)
	if len(denied) > 4096 {
		return nil, errors.New("managed actions denied network inventory exceeds its bound")
	}
	cache.at, cache.denied, cache.key = time.Now(), denied, key
	return slices.Clone(denied), nil
}

func (c *Client) validateGitLabPrivateNetworks(ctx context.Context, runtime GitLabActionsRuntime) ([]string, error) {
	denied, err := c.GitLabActionsDeniedNetworks(ctx, runtime)
	if err != nil {
		return nil, err
	}
	for _, destination := range runtime.PrivateDestinations {
		for _, raw := range destination.Addresses {
			address := netip.MustParsePrefix(raw).Addr()
			for _, blocked := range denied {
				if netip.MustParsePrefix(blocked).Contains(address) {
					return nil, errors.New("managed actions private grant intersects current cluster or reserved addresses")
				}
			}
		}
		u, _ := url.Parse(destination.Origin)
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
		if err != nil || len(addresses) < 1 || len(addresses) > 8 {
			return nil, errors.New("managed actions private origin could not be resolved within its bound")
		}
		for _, address := range addresses {
			prefix := netip.PrefixFrom(address.Unmap(), address.Unmap().BitLen())
			if !slices.Contains(destination.Addresses, prefix.String()) {
				return nil, errors.New("managed actions private origin resolved outside its exact address grant")
			}
			for _, raw := range denied {
				if netip.MustParsePrefix(raw).Contains(address.Unmap()) {
					return nil, errors.New("managed actions private origin intersects current cluster or reserved addresses")
				}
			}
		}
	}
	return denied, nil
}
