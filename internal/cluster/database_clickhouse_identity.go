package cluster

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"slices"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ClickHouse's Keeper client verifies the resolved IP, not the original DNS
// name. Stable, individually selected Services give that address a verifiable
// identity without binding certificates to disposable pod IPs.
func (c *Client) prepareClickHousePeerServices(ctx context.Context, d database.Resource, before func() error) ([]net.IP, error) {
	if d.Spec.KeeperInstances() == 0 {
		return nil, nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, fmt.Errorf("Keeper service namespace ownership changed")
	}
	addresses := []net.IP{}
	for i := 0; i < d.Spec.KeeperInstances(); i++ {
		name := fmt.Sprintf("database-keeper-%d", i)
		selector := map[string]string{databaseOwner: d.ID, databaseKeeperLabel: "true", "statefulset.kubernetes.io/pod-name": name}
		ports := []corev1.ServicePort{{Name: "keeper-tls", Port: 9281, TargetPort: intstr.FromInt(9281), Protocol: corev1.ProtocolTCP}, {Name: "raft-tls", Port: 9444, TargetPort: intstr.FromInt(9444), Protocol: corev1.ProtocolTCP}}
		api := c.kube.CoreV1().Services(ns.Name)
		service, err := api.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err = before(); err != nil {
				return nil, err
			}
			service, err = api.Create(ctx, &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, name), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector, Ports: ports, PublishNotReadyAddresses: true}}, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, err
		}
		if !mongodbSupportOwned(service, d, ns.UID) || service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.ExternalIPs) != 0 || service.Spec.ExternalName != "" || !service.Spec.PublishNotReadyAddresses || !reflect.DeepEqual(service.Spec.Selector, selector) || !reflect.DeepEqual(service.Spec.Ports, ports) {
			return nil, fmt.Errorf("Keeper member service identity changed")
		}
		if len(service.Spec.ClusterIPs) == 0 || len(service.Spec.ClusterIPs) > 2 || service.Spec.ClusterIP != service.Spec.ClusterIPs[0] {
			return nil, fmt.Errorf("Keeper member service has no stable address")
		}
		for _, address := range service.Spec.ClusterIPs {
			ip := net.ParseIP(address)
			if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
				return nil, fmt.Errorf("Keeper member service address is invalid")
			}
			addresses = append(addresses, ip)
		}
	}
	slices.SortFunc(addresses, func(a, b net.IP) int { return slices.Compare([]byte(a), []byte(b)) })
	return addresses, nil
}
