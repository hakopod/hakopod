package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func redisPublicEndpointServiceName(endpoint database.PublicEndpoint, member database.PublicEndpointMemberAllocation) string {
	sum := sha256.Sum256([]byte(endpoint.DatabaseID + "\x00" + member.MemberName))
	return fmt.Sprintf("database-public-%x", sum[:8])
}

func (c *Client) redisPublicEndpointMembers(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) ([]database.PublicEndpointMemberAllocation, map[string]corev1.Pod, types.UID, error) {
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil || d.Spec.Engine != "redis" || !d.Spec.TLSRequired() || !d.Observation.Fresh(time.Now(), d.Revision) || d.Observation.Status != "ready" || len(d.Observation.Members) != d.Spec.Members() {
		return nil, nil, "", fmt.Errorf("Redis public endpoint requires a current complete TLS member inventory")
	}
	if d.Spec.Mode == "cluster" && (len(endpoint.MemberAllocations) != d.Spec.Members() || !d.Observation.SlotsHealthy || d.Observation.SlotsAssigned != 16384) || d.Spec.Mode == "standalone" && (len(endpoint.MemberAllocations) != 0 || len(allocations) != 1) {
		return nil, nil, "", fmt.Errorf("Redis public endpoint allocation shape differs from its topology")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, nil, "", fmt.Errorf("Redis public endpoint namespace ownership changed")
	}
	gvr, _ := databaseGVR(d.Spec)
	controller, err := c.dynamic.Resource(gvr).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || controller.GetUID() == "" || controller.GetDeletionTimestamp() != nil || controller.GetLabels()[databaseOwner] != d.ID || controller.GetLabels()[managedBy] != "hakopod" || controller.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return nil, nil, "", fmt.Errorf("Redis public endpoint controller ownership or revision changed")
	}
	observed := make(map[string]database.Member, len(d.Observation.Members))
	for _, member := range d.Observation.Members {
		if !member.Ready || member.Name == "" || member.UID == "" || observed[member.Name].Name != "" {
			return nil, nil, "", fmt.Errorf("Redis public endpoint observation has invalid member identities")
		}
		observed[member.Name] = member
	}
	if d.Spec.Mode == "standalone" {
		allocations[0].MemberName, allocations[0].MemberUID = d.Observation.Members[0].Name, d.Observation.Members[0].UID
	}
	pods := make(map[string]corev1.Pod, len(allocations))
	for _, allocation := range allocations {
		member, found := observed[allocation.MemberName]
		pod, getErr := c.kube.CoreV1().Pods(ns.Name).Get(ctx, allocation.MemberName, metav1.GetOptions{})
		if !found || member.UID != allocation.MemberUID || getErr != nil || string(pod.UID) != allocation.MemberUID || pod.Labels["statefulset.kubernetes.io/pod-name"] != allocation.MemberName || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !databasePodMatches(*pod, d) || !c.databasePodOwned(ctx, *pod, controller.GetUID()) {
			return nil, nil, "", fmt.Errorf("Redis public endpoint member identity changed")
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
		}
		address, parseErr := netip.ParseAddr(pod.Status.PodIP)
		if !ready || parseErr != nil || !address.Is4() || address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() {
			return nil, nil, "", fmt.Errorf("Redis public endpoint member is not ready or has no usable address")
		}
		pods[allocation.MemberName] = *pod
	}
	return allocations, pods, ns.UID, nil
}

func redisPublicMemberService(d database.Resource, endpoint database.PublicEndpoint, member database.PublicEndpointMemberAllocation, namespaceUID types.UID) *corev1.Service {
	service := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, namespaceUID, redisPublicEndpointServiceName(endpoint, member)), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"statefulset.kubernetes.io/pod-name": member.MemberName}, Ports: []corev1.ServicePort{{Name: "redis", Protocol: corev1.ProtocolTCP, Port: 6379, TargetPort: intstr.FromInt32(6379)}}}}
	service.Labels[databasePublicEndpointLabel] = member.Allocation.ID
	service.Annotations = map[string]string{"hakopod.io/database-member-name": member.MemberName, "hakopod.io/database-member-uid": member.MemberUID}
	return service
}

func redisPublicMemberServiceOwned(service, desired *corev1.Service) bool {
	if service == nil || service.UID == "" || service.DeletionTimestamp != nil || service.Name != desired.Name || service.Namespace != desired.Namespace || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 || service.Spec.ClusterIP == corev1.ClusterIPNone || !reflect.DeepEqual(service.Spec.Selector, desired.Spec.Selector) || !reflect.DeepEqual(service.Spec.Ports, desired.Spec.Ports) || !reflect.DeepEqual(service.Labels, desired.Labels) || !reflect.DeepEqual(service.Annotations, desired.Annotations) || !reflect.DeepEqual(service.OwnerReferences, desired.OwnerReferences) {
		return false
	}
	return true
}

// Cluster listeners preserve Redis's private advertised addresses. Public
// clients must apply the reviewed per-member address mapping; Redis does not
// provide separate private and public discovery through TLS SNI.
func (c *Client) reconcileRedisPublicMemberServices(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, create bool, before func() error) error {
	if d.Spec.Mode != "cluster" {
		return nil
	}
	members, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Redis public member namespace ownership changed")
	}
	if create {
		members, _, _, err = c.redisPublicEndpointMembers(ctx, d, endpoint)
		if err != nil {
			return err
		}
	}
	api := c.kube.CoreV1().Services(ns.Name)
	// Inspect the complete service set before removing any member. A foreign
	// service later in the list must not cause partially authorized teardown.
	for _, member := range members {
		desired := redisPublicMemberService(d, endpoint, member, ns.UID)
		service, getErr := api.Get(ctx, desired.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil || !redisPublicMemberServiceOwned(service, desired) {
			return fmt.Errorf("Redis public member Service ownership or target changed")
		}
	}
	for _, member := range members {
		desired := redisPublicMemberService(d, endpoint, member, ns.UID)
		service, getErr := api.Get(ctx, desired.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			if !create {
				continue
			}
			if err = before(); err != nil {
				return err
			}
			if _, err = api.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
				return err
			}
			continue
		}
		if getErr != nil || !redisPublicMemberServiceOwned(service, desired) {
			return fmt.Errorf("Redis public member Service ownership or target changed")
		}
		if !create {
			if err = before(); err != nil {
				return err
			}
			uid, version := service.UID, service.ResourceVersion
			if err = api.Delete(ctx, service.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (c *Client) reconcileRedisPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Mode == "cluster" && len(d.PublicEndpointMembers) > 0 {
		endpoint := database.PublicEndpoint{DatabaseID: d.ID, MemberAllocations: d.PublicEndpointMembers, Allocation: d.PublicEndpointMembers[0].Allocation}
		if err := c.reconcileRedisPublicMemberServices(ctx, d, endpoint, true, before); err != nil {
			return err
		}
	}
	return c.RenewDatabaseIdentity(ctx, d, before)
}

func redisPublicAddressMap(d database.Resource, endpoint database.PublicEndpoint, pods map[string]corev1.Pod, raw string) ([]database.PublicEndpointClientAddress, error) {
	members, err := database.PublicEndpointAllocations(endpoint)
	if err != nil || len(members) != d.Spec.Members() || len(pods) != len(members) {
		return nil, fmt.Errorf("Redis public address map requires every reviewed member")
	}
	_, fingerprint, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
	if err != nil || fingerprint != d.Observation.TopologyFingerprint {
		return nil, fmt.Errorf("Redis public address map topology changed")
	}
	byAddress := make(map[string]string, len(members))
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return nil, fmt.Errorf("Redis public discovery row is invalid")
		}
		address := strings.Split(fields[1], ",")
		if len(address) != 2 || address[1] == "" {
			return nil, fmt.Errorf("Redis public discovery requires its verified private hostname")
		}
		native := strings.Split(address[0], "@")
		if len(native) != 2 || native[1] != "16379" {
			return nil, fmt.Errorf("Redis public discovery cluster bus port changed")
		}
		host, port, parseErr := net.SplitHostPort(native[0])
		if parseErr != nil || port != "6379" || byAddress[host] != "" {
			return nil, fmt.Errorf("Redis public discovery member port or identity changed")
		}
		byAddress[host] = address[1]
	}
	result := make([]database.PublicEndpointClientAddress, 0, len(members))
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		pod, found := pods[member.MemberName]
		privateHost := redisMemberHostname(d, database.Member{Name: member.MemberName})
		if !found || string(pod.UID) != member.MemberUID || seen[pod.Status.PodIP] || byAddress[pod.Status.PodIP] != privateHost {
			return nil, fmt.Errorf("Redis public discovery does not match the reviewed private members")
		}
		seen[pod.Status.PodIP] = true
		result = append(result, database.PublicEndpointClientAddress{MemberName: member.MemberName, MemberUID: member.MemberUID, AdvertisedAddresses: []string{net.JoinHostPort(pod.Status.PodIP, "6379"), net.JoinHostPort(privateHost, "6379")}, PublicHost: member.Allocation.Host, PublicPort: member.Allocation.Port})
	}
	slices.SortFunc(result, func(a, b database.PublicEndpointClientAddress) int {
		return strings.Compare(a.MemberName, b.MemberName)
	})
	return result, nil
}

func (c *Client) observeRedisPublicAddressMap(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) ([]database.PublicEndpointClientAddress, error) {
	members, pods, _, err := c.redisPublicEndpointMembers(ctx, d, endpoint)
	if err != nil {
		return nil, err
	}
	raw, _, err := c.redisClusterView(ctx, d, database.Member{Name: members[0].MemberName, UID: members[0].MemberUID})
	if err != nil {
		return nil, err
	}
	return redisPublicAddressMap(d, endpoint, pods, raw)
}

func (c *Client) verifyRedisPublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	members, pods, namespaceUID, err := c.redisPublicEndpointMembers(ctx, d, endpoint)
	if err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	names, err := database.PublicEndpointAllocationNames(endpoint)
	if err != nil {
		return err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, names, time.Now())
	if err != nil {
		return fmt.Errorf("Redis public certificate names are pending")
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 || strings.ContainsAny(string(secret.Data["password"]), "\x00\r\n") {
		return fmt.Errorf("Redis public endpoint credentials are unavailable")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, allocation := range members {
		group.Go(func() error {
			serviceName := "database"
			if d.Spec.Mode == "cluster" {
				serviceName = redisPublicEndpointServiceName(endpoint, allocation)
			}
			service, getErr := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(step, serviceName, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if d.Spec.Mode == "cluster" && !redisPublicMemberServiceOwned(service, redisPublicMemberService(d, endpoint, allocation, namespaceUID)) {
				return fmt.Errorf("Redis public member Service ownership changed")
			}
			route, _ := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
			route.BackendService = serviceName
			checkedService := service
			if d.Spec.Mode == "standalone" {
				gvr, kind := databaseGVR(d.Spec)
				controller, controllerErr := c.dynamic.Resource(gvr).Namespace(service.Namespace).Get(step, "database", metav1.GetOptions{})
				owned := false
				if controllerErr == nil && controller.GetUID() != "" {
					for _, owner := range service.OwnerReferences {
						owned = owned || owner.UID == controller.GetUID() && owner.Name == "database" && owner.Kind == kind && owner.APIVersion == gvr.Group+"/"+gvr.Version
					}
				}
				if !owned || len(service.Spec.Selector) == 0 {
					return fmt.Errorf("Redis standalone public backend Service ownership changed")
				}
				for key, value := range service.Spec.Selector {
					if pods[allocation.MemberName].Labels[key] != value {
						return fmt.Errorf("Redis standalone public backend selector changed")
					}
				}
				// The pinned operator owns standalone Services; the common address
				// helper expects Hakopod labels after ownership has been checked.
				checkedService = service.DeepCopy()
				checkedService.Labels = databaseLabels(d)
			}
			address, getErr := databasePublicEndpointBackendServiceAddress(checkedService, d, route)
			if getErr != nil {
				return getErr
			}
			slices, getErr := c.kube.DiscoveryV1().EndpointSlices(service.Namespace).List(step, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + service.Name, Limit: 2})
			if getErr != nil || slices.Continue != "" || len(slices.Items) != 1 || len(slices.Items[0].Endpoints) != 1 || len(slices.Items[0].Ports) == 0 || len(slices.Items[0].Ports) > 2 {
				return fmt.Errorf("Redis public member Service has no exact reviewed endpoint")
			}
			slice := slices.Items[0]
			ownedSlice := false
			for _, owner := range slice.OwnerReferences {
				ownedSlice = ownedSlice || owner.APIVersion == "v1" && owner.Kind == "Service" && owner.Name == service.Name && owner.UID == service.UID && owner.Controller != nil && *owner.Controller
			}
			if !ownedSlice || slice.DeletionTimestamp != nil || slice.AddressType != discoveryv1.AddressTypeIPv4 || slice.Namespace != service.Namespace {
				return fmt.Errorf("Redis public member EndpointSlice ownership changed")
			}
			matchedPort := false
			for _, port := range slices.Items[0].Ports {
				if port.Name != nil && *port.Name == route.BackendPortName && port.Port != nil && *port.Port == 6379 && port.Protocol != nil && *port.Protocol == corev1.ProtocolTCP {
					if matchedPort {
						return fmt.Errorf("Redis public member Service has duplicate protocol ports")
					}
					matchedPort = true
				}
			}
			target := slices.Items[0].Endpoints[0]
			if target.TargetRef == nil || target.TargetRef.Kind != "Pod" || target.TargetRef.Name != allocation.MemberName || string(target.TargetRef.UID) != allocation.MemberUID || target.Conditions.Ready == nil || !*target.Conditions.Ready || target.Conditions.Terminating != nil && *target.Conditions.Terminating || len(target.Addresses) != 1 || target.Addresses[0] != pods[allocation.MemberName].Status.PodIP || !matchedPort {
				return fmt.Errorf("Redis public member Service endpoint identity changed")
			}
			member := database.Member{Name: allocation.MemberName, UID: allocation.MemberUID}
			for _, destination := range []string{address, pods[allocation.MemberName].Status.PodIP} {
				config, configErr := redisTLSConfig(trust, allocation.Allocation.Host)
				if configErr != nil {
					return configErr
				}
				config.VerifyConnection = redisPublicCertificateVerifier(identity.Fingerprint)
				probe, cancel := context.WithTimeout(step, 8*time.Second)
				raw, closeStream, streamErr := c.databaseRedisStream(probe, DatabaseNamespace(d.ID), member.Name, types.UID(member.UID), destination)
				if streamErr != nil {
					cancel()
					return streamErr
				}
				conn := tls.Client(raw, config)
				if streamErr = conn.HandshakeContext(probe); streamErr == nil {
					wire := database.NewRedisWire(conn)
					_, streamErr = wire.Command([]byte("AUTH"), secret.Data["password"])
					if streamErr == nil {
						var reply any
						reply, streamErr = wire.Command([]byte("PING"))
						if reply != "PONG" && streamErr == nil {
							streamErr = fmt.Errorf("Redis public endpoint authentication probe failed")
						}
						if streamErr == nil && d.Spec.Mode == "cluster" {
							reply, streamErr = wire.Command([]byte("CLUSTER"), []byte("NODES"))
							body, ok := reply.([]byte)
							if streamErr == nil && !ok {
								streamErr = fmt.Errorf("Redis public member discovery is invalid")
							}
							if streamErr == nil {
								_, streamErr = redisPublicAddressMap(d, endpoint, pods, string(body))
							}
						}
					}
				}
				closeStream()
				cancel()
				if streamErr != nil {
					return fmt.Errorf("Redis public endpoint TLS, hostname or authentication verification failed")
				}
				output := &databaseBoundedWriter{limit: 1024}
				plain, stop := context.WithTimeout(step, 4*time.Second)
				streamErr = c.DatabaseExec(plain, d, member, []string{"sh", "-c", redisPublicPlaintextProbe, "redis-public-plaintext", destination}, nil, output)
				stop()
				if streamErr != nil || output.String() != "plaintext-rejected" {
					return fmt.Errorf("Redis public backend plaintext refusal was not observed")
				}
			}
			return nil
		})
	}
	return group.Wait()
}

func redisPublicCertificateVerifier(fingerprint string) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || state.PeerCertificates[0] == nil || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != fingerprint {
			return fmt.Errorf("Redis has not loaded its current public certificate")
		}
		return nil
	}
}

const redisPublicPlaintextProbe = `set -eu
export LC_ALL=C
if response=$(redis-cli -h "$1" -p 6379 PING 2>&1); then exit 1; fi
case "$response" in *"Server closed the connection"*|*"Connection reset by peer"*) printf 'plaintext-rejected';; *) exit 1;; esac`
