package cluster

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Completed and evicted pods cannot serve traffic. Exclude their retained API
// records before applying inventory limits; replacement pods still have to pass
// ownership, readiness, endpoint and native engine checks.
const activeDatabasePodFields = "status.phase!=Failed,status.phase!=Succeeded"

func (c *Client) observePostgresDatabase(ctx context.Context, d database.Resource, o *database.Observation) error {
	primaries := 0
	for _, member := range o.Members {
		if member.Name == o.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return fmt.Errorf("PostgreSQL has no unique observed primary")
	}
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, member := range o.Members {
		group.Go(func() error {
			output := &databaseBoundedWriter{limit: 4096}
			query := "SELECT pg_is_in_recovery(), (SELECT count(*) FROM pg_stat_replication WHERE state='streaming'), (SELECT count(*) FROM pg_stat_wal_receiver WHERE status='streaming')"
			if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", query}, nil, output); err != nil {
				return err
			}
			expected := "t|0|1"
			if member.Name == o.Primary {
				expected = "f|" + strconv.Itoa(d.Spec.Replicas) + "|0"
			}
			if strings.TrimSpace(output.String()) != expected {
				return fmt.Errorf("PostgreSQL primary or streaming replication is not healthy")
			}
			return nil
		})
	}
	return group.Wait()
}

func (c *Client) databaseEndpointsReady(ctx context.Context, d database.Resource, o database.Observation, inventory *vitessObservationInventory) error {
	ns := DatabaseNamespace(d.ID)
	for _, endpoint := range o.Endpoints {
		members := o.Members
		pooled := endpoint.Purpose == "pooled_read_write" || endpoint.Purpose == "pooled_read_only"
		routed := d.Spec.Engine == "mysql" || d.Spec.Engine == "vitess"
		if routed {
			if o.Routing == nil || !o.Routing.Ready {
				return fmt.Errorf("database routing observation is unavailable")
			}
			members = o.Routing.Members
		}
		if pooled {
			if o.Pooling == nil || d.Spec.Pooling == nil {
				return fmt.Errorf("pooler observation is unavailable")
			}
			members = o.Pooling.Members
		}
		name := strings.Split(endpoint.Host, ".")[0]
		service, err := c.kube.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil || service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.ExternalIPs) > 0 {
			return fmt.Errorf("database private service is unavailable")
		}
		portOK := false
		portName := ""
		for _, port := range service.Spec.Ports {
			if int(port.Port) == endpoint.Port && port.Protocol == corev1.ProtocolTCP {
				if port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal != int32(endpoint.Port) || port.TargetPort.Type == intstr.String && port.TargetPort.StrVal == "" {
					return fmt.Errorf("database service points at an unexpected protocol port")
				}
				portOK = true
				portName = port.Name
			}
		}
		if !portOK {
			return fmt.Errorf("database service port is unavailable")
		}
		slices, err := c.kube.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + name, Limit: 8})
		if err != nil || slices.Continue != "" {
			return fmt.Errorf("database endpoint discovery is unavailable")
		}
		found := map[string]bool{}
		for _, slice := range slices.Items {
			if !databaseEndpointSlicePortMatches(slice.Ports, portName, int32(endpoint.Port)) {
				return fmt.Errorf("database endpoint discovery points at an unexpected protocol port")
			}
			owned := false
			for _, owner := range slice.OwnerReferences {
				if owner.UID == service.UID {
					owned = true
				}
			}
			if !owned {
				return fmt.Errorf("database endpoint ownership changed")
			}
			for _, e := range slice.Endpoints {
				if e.Conditions.Ready == nil || !*e.Conditions.Ready || (e.Conditions.Terminating != nil && *e.Conditions.Terminating) {
					continue
				}
				matched := false
				for _, member := range members {
					if e.TargetRef == nil || e.TargetRef.Namespace != ns || e.TargetRef.Name != member.Name || string(e.TargetRef.UID) != member.UID {
						continue
					}
					if !routed && endpoint.Purpose == "read_write" && member.Role != "primary" {
						return fmt.Errorf("write endpoint points at a replica")
					}
					if !routed && endpoint.Purpose == "read_only" && member.Role != "replica" {
						return fmt.Errorf("read endpoint points at a primary")
					}
					if pooled && member.Role != endpoint.Purpose {
						return fmt.Errorf("pooler endpoint points at a different route")
					}
					found[member.UID] = true
					matched = true
				}
				if !matched {
					return fmt.Errorf("database endpoint points at an unexpected member")
				}
			}
		}
		minimum := databaseEndpointMinimum(d, endpoint, pooled, routed)
		if len(found) != minimum {
			return fmt.Errorf("database service has not published the expected ready members")
		}
	}
	if d.Spec.Engine == "vitess" {
		return c.verifyVitessStorage(ctx, d, o.Members, inventory)
	}
	for _, member := range o.Members {
		pod, err := c.kube.CoreV1().Pods(ns).Get(ctx, member.Name, metav1.GetOptions{})
		if err != nil || string(pod.UID) != member.UID {
			return fmt.Errorf("database member changed")
		}
		volumes := 0
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				continue
			}
			claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			if err != nil || claim.Status.Phase != corev1.ClaimBound || len(claim.Status.Conditions) > 0 {
				return fmt.Errorf("database storage is not ready")
			}
			size := claim.Status.Capacity[corev1.ResourceStorage]
			expectedSize := d.Spec.StorageGiB
			if d.Spec.Engine == "mongodb" && volume.Name == "logs-volume" {
				expectedSize = database.MongoDBLogStorageGiB
			}
			if size.Cmp(resource.MustParse(fmt.Sprintf("%dGi", expectedSize))) < 0 {
				return fmt.Errorf("database storage has not reached the requested size")
			}
			volumes++
		}
		if volumes == 0 {
			return fmt.Errorf("database member has no persistent storage")
		}
	}
	return nil
}

func databaseEndpointMinimum(d database.Resource, endpoint database.Endpoint, pooled, routed bool) int {
	minimum := 1
	if pooled {
		minimum = d.Spec.Pooling.Instances
	}
	if endpoint.Purpose == "read_only" {
		minimum = d.Spec.Replicas
	}
	if endpoint.Purpose == "cluster" {
		minimum = d.Spec.Shards
		if d.Spec.Engine == "mongodb" || d.Spec.Engine == "clickhouse" {
			minimum = d.Spec.Members()
		}
	}
	// ClickHouse exposes cluster, native and HTTPS views through the same
	// all-ready-member service, so each view must retain the complete topology.
	if d.Spec.Engine == "clickhouse" && (endpoint.Purpose == "native" || endpoint.Purpose == "https") {
		minimum = d.Spec.Members()
	}
	if routed {
		minimum = d.Spec.RouterInstances()
		if d.Spec.Engine == "vitess" {
			minimum = d.Spec.VitessGateways()
		}
	}
	return minimum
}

// A healthy pod and a valid certificate do not prove the Service routes to
// that listener. Verify the named port's resolved target as well as its UID.
func databaseEndpointSlicePortMatches(ports []discoveryv1.EndpointPort, name string, expected int32) bool {
	matched := 0
	for _, port := range ports {
		actualName := ""
		if port.Name != nil {
			actualName = *port.Name
		}
		if actualName != name {
			continue
		}
		if port.Port == nil || *port.Port != expected || port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
			return false
		}
		matched++
	}
	return matched == 1
}

// CNPG instance managers require Kubernetes API access. Resolve only the actual
// service and control-plane endpoint addresses, never arbitrary HTTPS egress.
func (c *Client) databaseAPIEgress(ctx context.Context) ([]networkingv1.NetworkPolicyEgressRule, error) {
	service, err := c.kube.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	rules := []networkingv1.NetworkPolicyEgressRule{}
	seen := map[string]bool{}
	add := func(ip string, port int32) error {
		address, err := netip.ParseAddr(ip)
		if err != nil {
			return fmt.Errorf("Kubernetes API address is invalid")
		}
		key := ip + ":" + strconv.Itoa(int(port))
		if seen[key] {
			return nil
		}
		seen[key] = true
		cidr := netip.PrefixFrom(address, address.BitLen()).String()
		p := intstr.FromInt32(port)
		protocol := corev1.ProtocolTCP
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &p, Protocol: &protocol}}})
		return nil
	}
	for _, ip := range service.Spec.ClusterIPs {
		if err = add(ip, 443); err != nil {
			return nil, err
		}
	}
	slices, err := c.kube.DiscoveryV1().EndpointSlices("default").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes", Limit: 8})
	if err != nil || slices.Continue != "" {
		return nil, fmt.Errorf("Kubernetes API endpoints are unavailable")
	}
	for _, slice := range slices.Items {
		for _, e := range slice.Endpoints {
			for _, ip := range e.Addresses {
				for _, p := range slice.Ports {
					if p.Port != nil {
						if err = add(ip, *p.Port); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("Kubernetes API endpoints are unavailable")
	}
	return rules, nil
}

func (c *Client) ValidateDatabaseResize(ctx context.Context, d database.Resource, next database.Spec) error {
	placement := d
	placement.Spec = next
	if err := c.ValidateDatabasePlacement(ctx, placement); err != nil {
		return err
	}
	if next.StorageGiB <= d.Spec.StorageGiB {
		return nil
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 1})
	if err != nil || len(claims.Items) == 0 || len(claims.Items) > database.MaxMembers || claims.Continue != "" {
		return fmt.Errorf("database storage could not be verified")
	}
	for _, claim := range claims.Items {
		if claim.Spec.StorageClassName == nil {
			return fmt.Errorf("database storage has no expansion-capable class")
		}
		class, err := c.kube.StorageV1().StorageClasses().Get(ctx, *claim.Spec.StorageClassName, metav1.GetOptions{})
		if err != nil || class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion {
			return fmt.Errorf("the database storage class does not allow expansion; restore into a separate database with larger storage")
		}
	}
	return nil
}
