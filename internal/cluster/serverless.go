package cluster

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ServerlessAddress is installation-owned, never supplied by application TOML.
func ValidateServerlessAddress(address string) error {
	if address == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	p, e := strconv.Atoi(port)
	if err != nil || e != nil || ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || !ip.IsGlobalUnicast() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || p < 1024 || p > 65535 {
		return fmt.Errorf("serverless address must be a reachable unicast IPv4 address with a port from 1024 to 65535")
	}
	return nil
}

// ServerlessSourceIP selects the trusted management node's source address.
// Kube-proxy may still translate it to that node's reserved Flannel address.
func (c *Client) ServerlessSourceIP() net.IP {
	host, _, _ := net.SplitHostPort(c.options.ServerlessAddress)
	return net.ParseIP(host)
}

func (c *Client) ServerlessEnabled() bool { return c.options.ServerlessAddress != "" }

// Native K3s masquerades host-to-ClusterIP traffic to the outgoing interface:
// Flannel's reserved subnet address across nodes, or its bridge gateway locally.
// Admit only those host addresses, never the node's entire workload subnet.
func (c *Client) serverlessGatewaySources(ctx context.Context, t Target) ([]string, error) {
	needed := false
	for _, svc := range t.Spec.Services {
		needed = needed || svc.Serverless != nil
	}
	if !needed {
		return nil, nil
	}
	host, _, _ := net.SplitHostPort(c.options.ServerlessAddress)
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 201})
	if err != nil {
		return nil, err
	}
	if nodes.Continue != "" || len(nodes.Items) > 200 {
		return nil, fmt.Errorf("serverless gateway node inventory exceeds 200 nodes")
	}
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type != corev1.NodeInternalIP || a.Address != host {
				continue
			}
			ips := []string{host}
			if n.Annotations["flannel.alpha.coreos.com/backend-type"] != "" {
				prefix, err := netip.ParsePrefix(n.Spec.PodCIDR)
				if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 30 || prefix.Bits() < 1 {
					return nil, fmt.Errorf("serverless gateway node has no usable IPv4 pod subnet")
				}
				base := prefix.Masked().Addr()
				ips = append(ips, base.String(), base.Next().String())
			}
			return ips, nil
		}
	}
	return nil, fmt.Errorf("serverless gateway address must be the internal IPv4 address of its Kubernetes node")
}
func (c *Client) validateServerless(t Target) error {
	for name, s := range t.Spec.Services {
		if s.Serverless != nil && !c.ServerlessEnabled() {
			return fmt.Errorf("services.%s.serverless: the installation owner must enable the HTTP activation gateway first", name)
		}
	}
	return nil
}

// Longer than the maximum user service name, avoiding application collisions.
func ActivationServiceName(service string) string { return "hakopod-activate-" + ownerID(service) }

func (c *Client) applyActivationService(ctx context.Context, t Target, name string, s spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	ns, activation := Namespace(t.ApplicationID), ActivationServiceName(name)
	services := c.kube.CoreV1().Services(ns)
	current, err := services.Get(ctx, activation, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err = owned(current, t); err != nil {
			return err
		}
	}
	if s.Serverless == nil {
		// EndpointSlice is garbage-collected with its owning Service.
		if current != nil && err == nil {
			return services.Delete(ctx, activation, deleteOptions(current))
		}
		return nil
	}
	if err = ValidateServerlessAddress(c.options.ServerlessAddress); err != nil || c.options.ServerlessAddress == "" {
		return fmt.Errorf("serverless gateway is not configured")
	}
	host, port, _ := net.SplitHostPort(c.options.ServerlessAddress)
	portNumber, _ := strconv.Atoi(port)
	wanted := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: activation, Namespace: ns, Labels: labelsFor(t, name)}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "http", Port: s.Port, TargetPort: intstr.FromInt(portNumber), Protocol: corev1.ProtocolTCP}}}}
	if current == nil || apierrors.IsNotFound(err) || current.Name == "" {
		current, err = services.Create(ctx, wanted, metav1.CreateOptions{})
	} else {
		current.Spec.Ports = wanted.Spec.Ports
		current.Spec.Selector = nil
		current, err = services.Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	slices := c.kube.DiscoveryV1().EndpointSlices(ns)
	existing, err := slices.Get(ctx, activation, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	wantedSlice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: activation, Namespace: ns, Labels: labelsFor(t, name), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: activation, UID: current.UID}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(int32(portNumber)), Protocol: ptr(corev1.ProtocolTCP)}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(true)}}}}
	wantedSlice.Labels[discoveryv1.LabelServiceName] = activation
	wantedSlice.Labels[discoveryv1.LabelManagedBy] = "hakopod"
	if apierrors.IsNotFound(err) {
		_, err = slices.Create(ctx, wantedSlice, metav1.CreateOptions{})
		return err
	}
	if err = owned(existing, t); err != nil {
		return err
	}
	wantedSlice.ResourceVersion = existing.ResourceVersion
	_, err = slices.Update(ctx, wantedSlice, metav1.UpdateOptions{})
	return err
}

const serverlessBackendLabel = "hakopod.io/serverless-backend"

type ServerlessBackends struct{ services map[string]*corev1.Service }

// Read the bounded backend inventory in one Kubernetes call per route refresh,
// avoiding one API request per service on the HTTP routing path.
func (c *Client) ServerlessBackends(ctx context.Context) (ServerlessBackends, error) {
	result := ServerlessBackends{services: map[string]*corev1.Service{}}
	items, err := c.kube.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + serverlessBackendLabel + "=true", Limit: 1025})
	if err != nil {
		return result, err
	}
	if items.Continue != "" || len(items.Items) > 1024 {
		return result, fmt.Errorf("serverless backend inventory exceeds its bound")
	}
	for i := range items.Items {
		svc := &items.Items[i]
		result.services[svc.Namespace+"/"+svc.Name] = svc
	}
	return result, nil
}

func (b ServerlessBackends) Backend(t Target, name string) (string, error) {
	if t.Spec.Services[name].Serverless == nil {
		return "", ErrIdleIneligible
	}
	svc := b.services[Namespace(t.ApplicationID)+"/"+name]
	if svc == nil {
		return "", fmt.Errorf("serverless workload Service is unavailable")
	}
	if err := owned(svc, t); err != nil {
		return "", err
	}
	if net.ParseIP(svc.Spec.ClusterIP) == nil || len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("serverless workload Service is unavailable")
	}
	return "http://" + net.JoinHostPort(svc.Spec.ClusterIP, strconv.Itoa(int(t.Spec.Services[name].Port))), nil
}
