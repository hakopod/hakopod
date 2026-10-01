package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const mongodbPublicEndpointMemberLabel = "hakopod.io/database-public-member"
const mongodbPublicEndpointHorizon = "hakopod-public"

func mongodbPublicEndpointHorizonHosts(current []any) ([]string, error) {
	hosts := make([]string, 0, len(current))
	for _, raw := range current {
		horizon, ok := raw.(map[string]any)
		if !ok || len(horizon) != 1 {
			return nil, fmt.Errorf("MongoDB public endpoint horizons are invalid")
		}
		address, ok := horizon[mongodbPublicEndpointHorizon].(string)
		if !ok {
			return nil, fmt.Errorf("MongoDB public endpoint horizons are invalid")
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("MongoDB public endpoint horizons are invalid")
		}
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	return hosts, nil
}

func mongodbPublicEndpointServiceName(endpoint database.PublicEndpoint, member database.PublicEndpointMemberAllocation) string {
	sum := sha256.Sum256([]byte(endpoint.DatabaseID + "\x00" + member.MemberName))
	return "database-public-" + fmt.Sprintf("%x", sum[:8])
}

func mongodbPublicEndpointMembers(d database.Resource, endpoint database.PublicEndpoint) ([]database.PublicEndpointMemberAllocation, error) {
	members, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return nil, err
	}
	if d.Spec.Engine != "mongodb" || len(endpoint.MemberAllocations) == 0 || len(members) != d.Spec.Members() || len(d.Observation.Members) != len(members) {
		return nil, fmt.Errorf("MongoDB public endpoint requires one reviewed allocation for every current member")
	}
	observed := make(map[string]database.Member, len(d.Observation.Members))
	for _, member := range d.Observation.Members {
		if member.Name == "" || member.UID == "" || observed[member.Name].Name != "" {
			return nil, fmt.Errorf("MongoDB member inventory is invalid")
		}
		observed[member.Name] = member
	}
	for index, member := range members {
		ordinal, parseErr := strconv.Atoi(strings.TrimPrefix(member.MemberName, "database-"))
		current, exists := observed[member.MemberName]
		if parseErr != nil || ordinal != index || !exists || !current.Ready || current.UID != member.MemberUID {
			return nil, fmt.Errorf("MongoDB public member order or immutable identity changed")
		}
	}
	return members, nil
}

func mongodbPublicEndpointHorizons(d database.Resource, endpoint database.PublicEndpoint) ([]any, error) {
	members, err := mongodbPublicEndpointMembers(d, endpoint)
	if err != nil {
		return nil, err
	}
	horizons := make([]any, len(members))
	for i, member := range members {
		horizons[i] = map[string]any{mongodbPublicEndpointHorizon: net.JoinHostPort(member.Allocation.Host, strconv.Itoa(int(member.Allocation.Port)))}
	}
	return horizons, nil
}

func mongodbPublicEndpointMemberServices(ctx context.Context, c *Client, d database.Resource, endpoint database.PublicEndpoint, create bool, before func() error) error {
	members, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	if create {
		members, err = mongodbPublicEndpointMembers(d, endpoint)
		if err != nil {
			return err
		}
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MongoDB public endpoint namespace ownership changed")
	}
	for _, member := range members {
		pod, podErr := c.kube.CoreV1().Pods(ns.Name).Get(ctx, member.MemberName, metav1.GetOptions{})
		if create && (podErr != nil || pod.UID != types.UID(member.MemberUID) || pod.DeletionTimestamp != nil || !mongodbPodImagesMatch(*pod, d)) {
			return fmt.Errorf("MongoDB public endpoint member identity changed")
		}
		if !create && podErr != nil && !apierrors.IsNotFound(podErr) {
			return podErr
		}
		labelValue := ownerID(member.Allocation.ID)
		if create && pod.Labels[mongodbPublicEndpointMemberLabel] != labelValue {
			updated := pod.DeepCopy()
			if updated.Labels == nil {
				updated.Labels = map[string]string{}
			}
			updated.Labels[mongodbPublicEndpointMemberLabel] = labelValue
			if err = before(); err != nil {
				return err
			}
			pod, err = c.kube.CoreV1().Pods(ns.Name).Update(ctx, updated, metav1.UpdateOptions{})
			if err != nil || pod.UID != types.UID(member.MemberUID) {
				return fmt.Errorf("MongoDB public endpoint member label was not applied to the reviewed member")
			}
		}
		name := mongodbPublicEndpointServiceName(endpoint, member)
		api := c.kube.CoreV1().Services(ns.Name)
		service, serviceErr := api.Get(ctx, name, metav1.GetOptions{})
		if !create {
			if serviceErr == nil {
				if !mongodbPublicEndpointServiceOwned(service, d, endpoint, member, ns.UID) {
					return fmt.Errorf("MongoDB public member Service ownership changed")
				}
				if err = before(); err != nil {
					return err
				}
				uid := service.UID
				if err = api.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			} else if !apierrors.IsNotFound(serviceErr) {
				return serviceErr
			}
			if podErr == nil && pod.UID == types.UID(member.MemberUID) && pod.Labels[mongodbPublicEndpointMemberLabel] == labelValue {
				updated := pod.DeepCopy()
				delete(updated.Labels, mongodbPublicEndpointMemberLabel)
				if err = before(); err != nil {
					return err
				}
				if _, err = c.kube.CoreV1().Pods(ns.Name).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
			continue
		}
		desired := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, name), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{databaseOwner: d.ID, mongodbPublicEndpointMemberLabel: labelValue, "statefulset.kubernetes.io/pod-name": member.MemberName}, Ports: []corev1.ServicePort{{Name: "mongodb", Protocol: corev1.ProtocolTCP, Port: 27017}}}}
		desired.Labels[databasePublicEndpointLabel] = member.Allocation.ID
		desired.Annotations = map[string]string{"hakopod.io/database-member-name": member.MemberName, "hakopod.io/database-member-uid": member.MemberUID}
		if apierrors.IsNotFound(serviceErr) {
			if err = before(); err != nil {
				return err
			}
			service, err = api.Create(ctx, desired, metav1.CreateOptions{})
		} else if serviceErr != nil {
			return serviceErr
		}
		if err != nil || !mongodbPublicEndpointServiceOwned(service, d, endpoint, member, ns.UID) {
			return fmt.Errorf("MongoDB public member Service ownership or routing changed")
		}
	}
	return nil
}

func mongodbPublicEndpointServiceOwned(service *corev1.Service, d database.Resource, endpoint database.PublicEndpoint, member database.PublicEndpointMemberAllocation, namespaceUID types.UID) bool {
	wantedSelector := map[string]string{databaseOwner: d.ID, mongodbPublicEndpointMemberLabel: ownerID(member.Allocation.ID), "statefulset.kubernetes.io/pod-name": member.MemberName}
	if service == nil || service.DeletionTimestamp != nil || service.Namespace != DatabaseNamespace(d.ID) || service.Name != mongodbPublicEndpointServiceName(endpoint, member) || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ClusterIP == corev1.ClusterIPNone || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 || !reflect.DeepEqual(service.Spec.Selector, wantedSelector) || len(service.Spec.Ports) != 1 || service.Labels[databaseOwner] != d.ID || service.Labels[managedBy] != "hakopod" || service.Labels[databasePublicEndpointLabel] != member.Allocation.ID || service.Annotations["hakopod.io/database-member-name"] != member.MemberName || service.Annotations["hakopod.io/database-member-uid"] != member.MemberUID {
		return false
	}
	port := service.Spec.Ports[0]
	if port.Name != "mongodb" || port.Protocol != corev1.ProtocolTCP || port.Port != 27017 || port.NodePort != 0 || port.AppProtocol != nil || port.TargetPort.StrVal != "" || port.TargetPort.IntVal != 0 && port.TargetPort.IntVal != 27017 {
		return false
	}
	for _, owner := range service.OwnerReferences {
		if owner.APIVersion == "v1" && owner.Kind == "Namespace" && owner.Name == service.Namespace && owner.UID == namespaceUID {
			return true
		}
	}
	return false
}

func (c *Client) reconcileMongoDBPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if err := c.prepareMongoDBSecurity(ctx, d, before); err != nil {
		return err
	}
	if len(d.PublicEndpointMembers) == 0 {
		object, err := c.dynamic.Resource(mongodbDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
		if err != nil || object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID {
			return fmt.Errorf("MongoDB public endpoint controller ownership changed")
		}
		current, found, readErr := unstructured.NestedSlice(object.Object, "spec", "replicaSetHorizons")
		if readErr != nil {
			return fmt.Errorf("MongoDB public endpoint horizons are invalid")
		}
		if len(d.PublicEndpointNames) > 0 {
			hosts, parseErr := mongodbPublicEndpointHorizonHosts(current)
			if parseErr != nil {
				return parseErr
			}
			if !found || !reflect.DeepEqual(hosts, d.PublicEndpointNames) {
				return fmt.Errorf("MongoDB public endpoint horizons differ from durable active names")
			}
			return nil
		}
		if found {
			updated := object.DeepCopy()
			unstructured.RemoveNestedField(updated.Object, "spec", "replicaSetHorizons")
			if err = before(); err != nil {
				return err
			}
			_, err = c.dynamic.Resource(mongodbDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Update(ctx, updated, metav1.UpdateOptions{})
		}
		return err
	}
	endpoint := database.PublicEndpoint{DatabaseID: d.ID, Allocation: d.PublicEndpointMembers[0].Allocation, MemberAllocations: append([]database.PublicEndpointMemberAllocation(nil), d.PublicEndpointMembers...)}
	return c.reconcileMongoDBPublicEndpoint(ctx, d, endpoint, d.PublicEndpointAccess, before)
}

// reconcileMongoDBPublicEndpoint applies the exact reviewed member order. It is
// separate from name-only reconciliation because horizons bind hostnames to UIDs.
// Publication requires the MongoDB agent to hot-reload the new leaf without pod
// replacement. A changed pod UID deliberately leaves the route closed and requires
// revocation followed by a new review; allocations and certificate state are kept
// until that fail-closed cleanup completes.
func (c *Client) reconcileMongoDBPublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, publish bool, before func() error) error {
	copy := d
	if publish {
		names, err := database.PublicEndpointAllocationNames(endpoint)
		if err != nil {
			return err
		}
		copy.PublicEndpointNames = names
	} else {
		copy.PublicEndpointNames = nil
	}
	if err := c.prepareDatabaseIdentity(ctx, copy, before); err != nil {
		return err
	}
	if err := c.prepareMongoDBSecurity(ctx, copy, before); err != nil {
		return err
	}
	object, err := c.dynamic.Resource(mongodbDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("MongoDB public endpoint controller ownership or revision changed")
	}
	var desired []any
	if publish {
		desired, err = mongodbPublicEndpointHorizons(d, endpoint)
		if err != nil {
			return err
		}
	}
	current, found, _ := unstructured.NestedSlice(object.Object, "spec", "replicaSetHorizons")
	if !found {
		current = nil
	}
	if !reflect.DeepEqual(current, desired) {
		updated := object.DeepCopy()
		if len(desired) == 0 {
			unstructured.RemoveNestedField(updated.Object, "spec", "replicaSetHorizons")
		} else if err = unstructured.SetNestedSlice(updated.Object, desired, "spec", "replicaSetHorizons"); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		if _, err = c.dynamic.Resource(mongodbDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if !publish {
		if err = c.verifyMongoDBPrivateDiscovery(ctx, copy); err != nil {
			return err
		}
	}
	return mongodbPublicEndpointMemberServices(ctx, c, d, endpoint, publish, before)
}

type mongodbEndpointHello struct {
	Hosts   []string `bson:"hosts"`
	Primary string   `bson:"primary"`
	SetName string   `bson:"setName"`
}

func verifyMongoDBEndpointHello(hello mongodbEndpointHello, expected []string) error {
	hosts := append([]string(nil), hello.Hosts...)
	wanted := append([]string(nil), expected...)
	slices.Sort(hosts)
	slices.Sort(wanted)
	if hello.SetName != "database" || !reflect.DeepEqual(hosts, wanted) || !slices.Contains(wanted, hello.Primary) {
		return fmt.Errorf("MongoDB advertised topology differs from the expected member identities")
	}
	return nil
}

func mongodbEndpointTLSConfig(trust database.PublicTrust, host, fingerprint string) (*tls.Config, error) {
	config, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, err
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != fingerprint {
			return fmt.Errorf("MongoDB has not loaded its reviewed certificate")
		}
		return nil
	}
	return config, nil
}

func disconnectMongoDBProbe(client *mongo.Client) {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = client.Disconnect(cleanup)
}

func (c *Client) verifyMongoDBDirectSeed(ctx context.Context, d database.Resource, member database.Member, address string, expected []string, trust database.PublicTrust, fingerprint, password string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("MongoDB seed address is invalid")
	}
	tlsConfig, err := mongodbEndpointTLSConfig(trust, host, fingerprint)
	if err != nil {
		return err
	}
	dialer := mongodbDialer{dial: func(dial context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" || target != address {
			return nil, fmt.Errorf("MongoDB advertised an unexpected seed target")
		}
		return c.mongodbStream(dial, ctx, d, member)
	}}
	client, err := mongo.Connect(options.Client().SetHosts([]string{address}).SetDirect(true).SetReplicaSet("database").SetTLSConfig(tlsConfig).SetAuth(options.Credential{Username: "app", Password: password, AuthSource: "app", AuthMechanism: "SCRAM-SHA-256"}).SetDialer(dialer).SetMaxPoolSize(1).SetMinPoolSize(0).SetMaxConnecting(1).SetConnectTimeout(5 * time.Second).SetServerSelectionTimeout(8 * time.Second).SetTimeout(8 * time.Second).SetRetryReads(false).SetRetryWrites(false))
	if err != nil {
		return fmt.Errorf("MongoDB seed probe could not be configured")
	}
	defer disconnectMongoDBProbe(client)
	if err = client.Ping(ctx, readpref.Nearest()); err != nil {
		return fmt.Errorf("MongoDB seed did not accept authenticated TLS")
	}
	var hello mongodbEndpointHello
	if err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}, options.RunCmd().SetReadPreference(readpref.Nearest())).Decode(&hello); err != nil {
		return fmt.Errorf("MongoDB seed discovery was unavailable")
	}
	return verifyMongoDBEndpointHello(hello, expected)
}

func (c *Client) verifyMongoDBPrivateDiscovery(ctx context.Context, d database.Resource) error {
	observed, err := c.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" || len(observed.Members) != d.Spec.Members() {
		return fmt.Errorf("MongoDB private topology has not converged")
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	expected := make([]string, len(observed.Members))
	privateNames := make([]string, len(observed.Members))
	byAddress := make(map[string]database.Member, len(observed.Members)+1)
	var primary database.Member
	for i, member := range observed.Members {
		privateNames[i] = mongodbMemberHost(d, member)
		expected[i] = net.JoinHostPort(privateNames[i], "27017")
		byAddress[expected[i]] = member
		if member.Role == "primary" {
			primary = member
		}
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, privateNames, time.Now())
	if err != nil {
		return fmt.Errorf("MongoDB private certificate names are pending: %w", err)
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 {
		return fmt.Errorf("MongoDB private probe credentials are unavailable")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, member := range observed.Members {
		member, address := member, expected[i]
		group.Go(func() error {
			return c.verifyMongoDBDirectSeed(step, d, member, address, expected, trust, identity.Fingerprint, string(secret.Data["password"]))
		})
	}
	if err = group.Wait(); err != nil {
		return err
	}
	if primary.Name == "" {
		return fmt.Errorf("MongoDB private discovery has no primary")
	}
	seedHost := "database-svc." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	seedAddress := net.JoinHostPort(seedHost, "27017")
	byAddress[seedAddress] = primary
	tlsConfig, err := mongodbEndpointTLSConfig(trust, "", identity.Fingerprint)
	if err != nil {
		return err
	}
	dialer := mongodbDialer{dial: func(dial context.Context, network, address string) (net.Conn, error) {
		member, ok := byAddress[address]
		if network != "tcp" || !ok {
			return nil, fmt.Errorf("MongoDB private discovery advertised an unexpected target")
		}
		return c.mongodbStream(dial, ctx, d, member)
	}}
	client, err := mongo.Connect(options.Client().SetHosts([]string{seedAddress}).SetReplicaSet("database").SetTLSConfig(tlsConfig).SetAuth(options.Credential{Username: "app", Password: string(secret.Data["password"]), AuthSource: "app", AuthMechanism: "SCRAM-SHA-256"}).SetDialer(dialer).SetMaxPoolSize(uint64(len(observed.Members))).SetMinPoolSize(0).SetMaxConnecting(2).SetConnectTimeout(5 * time.Second).SetServerSelectionTimeout(8 * time.Second).SetTimeout(8 * time.Second).SetRetryReads(false).SetRetryWrites(false))
	if err != nil {
		return fmt.Errorf("MongoDB private discovery probe could not be configured")
	}
	defer disconnectMongoDBProbe(client)
	if err = client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("MongoDB private client discovery did not reach its primary")
	}
	var hello mongodbEndpointHello
	if err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return fmt.Errorf("MongoDB private client discovery was unavailable")
	}
	return verifyMongoDBEndpointHello(hello, expected)
}

func (c *Client) verifyMongoDBPublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	members, err := mongodbPublicEndpointMembers(d, endpoint)
	if err != nil {
		return err
	}
	wanted := make([]string, len(members))
	for i, member := range members {
		wanted[i] = net.JoinHostPort(member.Allocation.Host, strconv.Itoa(int(member.Allocation.Port)))
		serviceName := mongodbPublicEndpointServiceName(endpoint, member)
		service, serviceErr := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, serviceName, metav1.GetOptions{})
		ns, nsErr := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
		if serviceErr != nil || nsErr != nil || !mongodbPublicEndpointServiceOwned(service, d, endpoint, member, ns.UID) {
			return fmt.Errorf("MongoDB public member Service ownership changed")
		}
		slicesList, sliceErr := c.kube.DiscoveryV1().EndpointSlices(service.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + serviceName, Limit: 2})
		if sliceErr != nil || slicesList.Continue != "" || len(slicesList.Items) != 1 || len(slicesList.Items[0].Endpoints) != 1 {
			return fmt.Errorf("MongoDB public member Service has no exact reviewed endpoint")
		}
		target := slicesList.Items[0].Endpoints[0]
		if target.TargetRef == nil || target.TargetRef.Kind != "Pod" || target.TargetRef.Name != member.MemberName || string(target.TargetRef.UID) != member.MemberUID || target.Conditions.Ready == nil || !*target.Conditions.Ready || len(target.Addresses) != 1 {
			return fmt.Errorf("MongoDB public member Service endpoint identity changed")
		}
		ports := slicesList.Items[0].Ports
		if len(ports) != 1 || ports[0].Name == nil || *ports[0].Name != "mongodb" || ports[0].Port == nil || *ports[0].Port != 27017 {
			return fmt.Errorf("MongoDB public member Service port changed")
		}
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	identity, err := database.VerifyServerCertificate(certificate, ca, func() []string {
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.Allocation.Host
		}
		return names
	}(), time.Now())
	if err != nil {
		return fmt.Errorf("MongoDB public certificate names are pending: %w", err)
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 {
		return fmt.Errorf("MongoDB public probe credentials are unavailable")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, allocation := range members {
		allocation, address := allocation, wanted[i]
		group.Go(func() error {
			member := database.Member{Name: allocation.MemberName, UID: allocation.MemberUID}
			return c.verifyMongoDBDirectSeed(step, d, member, address, wanted, trust, identity.Fingerprint, string(secret.Data["password"]))
		})
	}
	return group.Wait()
}
