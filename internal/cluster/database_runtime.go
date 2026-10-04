package cluster

import (
	"bytes"
	"context"
	"fmt"
	"golang.org/x/sync/errgroup"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (c *Client) databaseNetworkPolicy(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine == "vitess" {
		return c.vitessNetworkPolicy(ctx, d, before)
	}
	ns := DatabaseNamespace(d.ID)
	tcp := corev1.ProtocolTCP
	ports := []networkingv1.NetworkPolicyPort{}
	for _, port := range []int{5432, 6379, 16379, 8000} {
		p := intstr.FromInt(port)
		ports = append(ports, networkingv1.NetworkPolicyPort{Port: &p, Protocol: &tcp})
	}
	local := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	operators := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"cnpg-system", "redis-operator"}}}}}
	apps := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/database-access-" + d.ID: "true"}}}
	dnsPort := intstr.FromInt(53)
	udp := corev1.ProtocolUDP
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns, Labels: databaseLabels(d)}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: databaseRecoveryHelper, Operator: metav1.LabelSelectorOpDoesNotExist}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{local, operators, apps}, Ports: ports}}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{local}, Ports: ports}, {To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &dnsPort, Protocol: &udp}, {Port: &dnsPort, Protocol: &tcp}}}}}}
	allowApplications := d.Status != "restoring" && (d.Recovery == nil || d.Recovery.RestoredAt != nil && d.Recovery.InspectedAt != nil)
	if !allowApplications {
		policy.Spec.Ingress[0].From = []networkingv1.NetworkPolicyPeer{local, operators}
	}
	if (d.Spec.Engine == "postgresql" || d.Spec.Engine == "redis") && d.PublicEndpointAccess && allowApplications {
		portNumber := 5432
		if d.Spec.Engine == "redis" {
			portNumber = 6379
		}
		postgresPort := intstr.FromInt(portNumber)
		haproxy := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.options.ProxyNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": c.options.ProxyRelease}}}
		policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{haproxy}, Ports: []networkingv1.NetworkPolicyPort{{Port: &postgresPort, Protocol: &tcp}}})
	}
	if d.Spec.Engine == "mysql" {
		mysqlPorts := []networkingv1.NetworkPolicyPort{}
		for _, value := range []int{3306, 33060, 33061, 6446, 6447} {
			port := intstr.FromInt(value)
			mysqlPorts = append(mysqlPorts, networkingv1.NetworkPolicyPort{Port: &port, Protocol: &tcp})
		}
		mysqlOperator := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "mysql-operator"}}}
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{local, mysqlOperator}, Ports: mysqlPorts}}
		if allowApplications {
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{apps}, Ports: mysqlPorts[3:]})
		}
		policy.Spec.Egress[0].Ports = mysqlPorts
	}
	if d.Spec.Engine == "mongodb" {
		port := intstr.FromInt(27017)
		mongoPorts := []networkingv1.NetworkPolicyPort{{Port: &port, Protocol: &tcp}}
		peers := []networkingv1.NetworkPolicyPeer{local}
		if allowApplications {
			peers = append(peers, apps)
		}
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: peers, Ports: mongoPorts}}
		policy.Spec.Egress[0].Ports = mongoPorts
		if d.PublicEndpointAccess && allowApplications {
			haproxy := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.options.ProxyNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": c.options.ProxyRelease}}}
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{haproxy}, Ports: mongoPorts})
		}
	}
	if d.Spec.Engine == "oracle" {
		port := intstr.FromInt(2484)
		oraclePorts := []networkingv1.NetworkPolicyPort{{Port: &port, Protocol: &tcp}}
		peers := []networkingv1.NetworkPolicyPeer{local}
		if allowApplications {
			peers = append(peers, apps)
		}
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: peers, Ports: oraclePorts}}
		policy.Spec.Egress[0].Ports = oraclePorts
	}
	if d.Spec.Engine == "clickhouse" {
		clientPorts := []networkingv1.NetworkPolicyPort{}
		for _, value := range []int{8443, 9440} {
			port := intstr.FromInt(value)
			clientPorts = append(clientPorts, networkingv1.NetworkPolicyPort{Port: &port, Protocol: &tcp})
		}
		internalPorts := append([]networkingv1.NetworkPolicyPort{}, clientPorts...)
		for _, value := range []int{9010, 9281, 9444} {
			port := intstr.FromInt(value)
			internalPorts = append(internalPorts, networkingv1.NetworkPolicyPort{Port: &port, Protocol: &tcp})
		}
		operator := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "clickhouse-operator"}}}
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{local}, Ports: internalPorts}, {From: []networkingv1.NetworkPolicyPeer{operator}, Ports: clientPorts}}
		if allowApplications {
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{apps}, Ports: clientPorts})
		}
		policy.Spec.Egress[0].Ports = internalPorts
	}
	if (d.Spec.Engine == "mysql" || d.Spec.Engine == "clickhouse") && d.PublicEndpointAccess && allowApplications {
		haproxy := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.options.ProxyNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": c.options.ProxyRelease}}}
		clientPorts := []networkingv1.NetworkPolicyPort{}
		portNumbers := []int{6446, 6447}
		if d.Spec.Engine == "clickhouse" {
			portNumbers = []int{8443, 9440}
		}
		for _, value := range portNumbers {
			port := intstr.FromInt(value)
			clientPorts = append(clientPorts, networkingv1.NetworkPolicyPort{Port: &port, Protocol: &tcp})
		}
		policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{haproxy}, Ports: clientPorts})
	}
	if d.Spec.Engine == "oracle" && d.PublicEndpointAccess && allowApplications {
		haproxy := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.options.ProxyNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": c.options.ProxyRelease}}}
		port := intstr.FromInt(2484)
		policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{haproxy}, Ports: []networkingv1.NetworkPolicyPort{{Port: &port, Protocol: &tcp}}})
	}
	if d.Spec.Engine == "postgresql" || d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb" {
		rules, err := c.databaseAPIEgress(ctx)
		if err != nil {
			return err
		}
		policy.Spec.Egress = append(policy.Spec.Egress, rules...)
	}
	api := c.kube.NetworkingV1().NetworkPolicies(ns)
	old, err := api.Get(ctx, policy.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, policy, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if old.Labels[databaseOwner] != d.ID || old.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database network policy has a different owner")
	}
	if reflect.DeepEqual(old.Spec, policy.Spec) {
		return nil
	}
	updated := old.DeepCopy()
	updated.Spec = policy.Spec
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

// Refresh API egress and recovery isolation under the ready resource's
// maintenance lease. Uninspected targets remain closed.
func (c *Client) ReconcileDatabaseNetworkPolicy(ctx context.Context, d database.Resource, before func() error) error {
	if d.Status != "ready" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database network namespace ownership could not be verified")
	}
	return c.databaseNetworkPolicy(ctx, d, before)
}

// DatabaseExec does not accept an arbitrary namespace or pod identity. It checks
// the resource, controller owner, member UID and container immediately before exec.
func (c *Client) DatabaseExec(ctx context.Context, d database.Resource, member database.Member, command []string, stdin io.Reader, stdout io.Writer) error {
	return c.databaseExecContainer(ctx, d, member, "", command, stdin, stdout)
}

func (c *Client) databaseExecContainer(ctx context.Context, d database.Resource, member database.Member, helper string, command []string, stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pod, container, err := c.databaseExecTarget(ctx, d, member)
	if err != nil {
		return err
	}
	if helper != "" {
		if d.Spec.Engine != "mysql" || helper != "sidecar" || !mysqlPodImagesMatch(*pod, d) {
			return fmt.Errorf("database helper is not an owned pinned container")
		}
		container = helper
	}
	return c.databaseExecVerifiedPod(ctx, pod, container, command, stdin, stdout)
}

// databaseExecVerifiedPod executes only after the caller has checked this exact
// Pod UID, its controller chain, runtime and purpose. Keeping transport here
// lets maintenance paths use stricter transition-specific identity checks.
func (c *Client) databaseExecVerifiedPod(ctx context.Context, pod *corev1.Pod, container string, command []string, stdin io.Reader, stdout io.Writer) error {
	if pod == nil || pod.Name == "" || pod.Namespace == "" || pod.UID == "" || container == "" || c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("database execution transport is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: stdin != nil, Stdout: stdout != nil, Stderr: true, TTY: false}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return fmt.Errorf("database execution transport is unavailable")
	}
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: io.Discard, Tty: false}); err != nil {
		return fmt.Errorf("database command did not complete successfully")
	}
	return nil
}

// Resolve the owned exec target without running a command. Native transport
// uses the same checks, avoiding an extra exec for every health connection.
func (c *Client) databaseExecTarget(ctx context.Context, d database.Resource, member database.Member) (*corev1.Pod, string, error) {
	if oracleEnterprise(d.Spec) {
		if c.execConfig == nil || c.restClient() == nil {
			return nil, "", fmt.Errorf("Oracle execution transport is unavailable")
		}
		return c.oracleEnterpriseExecTarget(ctx, d, member)
	}
	if d.Spec.Engine == "vitess" {
		return c.vitessExecTarget(ctx, d, member)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, "", fmt.Errorf("database namespace ownership could not be verified")
	}
	gvr, _ := databaseGVR(d.Spec)
	object, err := c.dynamic.Resource(gvr).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetLabels()[databaseOwner] != d.ID {
		return nil, "", fmt.Errorf("database controller ownership could not be verified")
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || pod.UID != types.UID(member.UID) || pod.DeletionTimestamp != nil || !c.databasePodOwned(ctx, *pod, object.GetUID()) {
		return nil, "", fmt.Errorf("database member changed; obtain a new observation")
	}
	container := ""
	expected := databaseImages[d.Spec.Engine+":"+d.Spec.Version]
	for _, candidate := range pod.Spec.Containers {
		if candidate.Image == expected {
			container = candidate.Name
			break
		}
	}
	if container == "" || c.execConfig == nil || c.restClient() == nil {
		return nil, "", fmt.Errorf("database execution transport or pinned container is unavailable")
	}
	return pod, container, nil
}

func (c *Client) redisCommand(ctx context.Context, d database.Resource, m database.Member, subcommand string) (string, error) {
	// subcommand is supplied only by controller code. Credentials enter through
	// stdin and are never present in argv, a response, or a persisted operation.
	if subcommand != "CLUSTER NODES" && subcommand != "CLUSTER INFO" && subcommand != "PING" && subcommand != "DBSIZE" && subcommand != "INFO" {
		return "", fmt.Errorf("unsupported Redis health command")
	}
	if d.Spec.TLSRequired() {
		values, err := c.redisTLSCommands(ctx, d, m, []string{subcommand})
		if err != nil {
			return "", err
		}
		return values[0], nil
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID {
		return "", fmt.Errorf("Redis credentials are unavailable")
	}
	output := &databaseBoundedWriter{limit: 128 << 10}
	input := append(append([]byte(nil), secret.Data["password"]...), '\n')
	command := []string{"sh", "-c", "IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --raw " + subcommand}
	if err = c.DatabaseExec(ctx, d, m, command, bytes.NewReader(input), output); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Reuse one verified transport for a bounded group of native read-only checks.
// Reopening the Kubernetes exec stream for every command multiplied API reads
// and exhausted the observation deadline on larger healthy clusters.
func (c *Client) redisTLSCommands(ctx context.Context, d database.Resource, member database.Member, commands []string) ([]string, error) {
	if len(commands) == 0 || len(commands) > 4 {
		return nil, fmt.Errorf("Redis health command batch exceeds its bound")
	}
	for _, command := range commands {
		if command != "CLUSTER NODES" && command != "CLUSTER INFO" && command != "PING" && command != "DBSIZE" && command != "INFO" {
			return nil, fmt.Errorf("unsupported Redis health command")
		}
	}
	wire, closeStream, err := c.databaseRedisMemberConnection(ctx, d, member)
	if err != nil {
		return nil, err
	}
	defer closeStream()
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
		return nil, fmt.Errorf("Redis credentials are unavailable")
	}
	if _, err = wire.Command([]byte("AUTH"), secret.Data["password"]); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(commands))
	for _, command := range commands {
		args := [][]byte{}
		for _, part := range strings.Fields(command) {
			args = append(args, []byte(part))
		}
		result, err := wire.Command(args...)
		if err != nil {
			return nil, err
		}
		switch value := result.(type) {
		case string:
			values = append(values, value)
		case []byte:
			values = append(values, string(value))
		case int64:
			values = append(values, fmt.Sprint(value))
		default:
			return nil, fmt.Errorf("Redis health response is invalid")
		}
	}
	return values, nil
}

func (c *Client) redisClusterView(ctx context.Context, d database.Resource, member database.Member) (string, string, error) {
	if d.Spec.TLSRequired() {
		values, err := c.redisTLSCommands(ctx, d, member, []string{"CLUSTER NODES", "CLUSTER INFO"})
		if err != nil {
			return "", "", err
		}
		return values[0], values[1], nil
	}
	nodes, err := c.redisCommand(ctx, d, member, "CLUSTER NODES")
	if err != nil {
		return "", "", err
	}
	info, err := c.redisCommand(ctx, d, member, "CLUSTER INFO")
	return nodes, info, err
}
func (c *Client) observeRedisDatabase(ctx context.Context, d database.Resource, o *database.Observation) error {
	if len(o.Members) == 0 {
		return fmt.Errorf("Redis has no observed members")
	}
	if d.Spec.Mode == "standalone" {
		response, err := c.redisCommand(ctx, d, o.Members[0], "PING")
		if err != nil || strings.TrimSpace(response) != "PONG" {
			return fmt.Errorf("Redis did not answer its health check")
		}
		o.Members[0].Role = "primary"
		o.Primary = o.Members[0].Name
		o.Endpoints = []database.Endpoint{{Purpose: "read_write", Host: "database." + DatabaseNamespace(d.ID) + ".svc", Port: 6379}}
		return nil
	}
	raw, info, err := c.redisClusterView(ctx, d, o.Members[0])
	if err != nil {
		return err
	}
	nodes, fingerprint, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
	if err != nil {
		return err
	}
	if !strings.Contains(info, "cluster_state:ok\r\n") && !strings.Contains(info, "cluster_state:ok\n") {
		return fmt.Errorf("Redis reports an unhealthy cluster")
	}
	// Every member must agree before resharding is eligible. One healthy-looking
	// node is insufficient when another member still has an old cluster view.
	group, checkCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for _, member := range o.Members[1:] {
		member := member
		group.Go(func() error {
			step, cancel := context.WithTimeout(checkCtx, 8*time.Second)
			defer cancel()
			raw, info, err := c.redisClusterView(step, d, member)
			if err != nil {
				return err
			}
			_, other, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
			if err != nil || other != fingerprint {
				return fmt.Errorf("Redis members do not agree on slot ownership")
			}
			if !strings.Contains(info, "cluster_state:ok\r\n") && !strings.Contains(info, "cluster_state:ok\n") {
				return fmt.Errorf("Redis has an unhealthy member")
			}
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return err
	}
	for i := range o.Members {
		pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, o.Members[i].Name, metav1.GetOptions{})
		if err != nil || string(pod.UID) != o.Members[i].UID {
			return fmt.Errorf("Redis membership changed during observation")
		}
		found := false
		for _, node := range nodes {
			if node.Host == pod.Status.PodIP {
				o.Members[i].Role = node.Role
				o.Members[i].Shard = node.Primary
				if node.Role == "primary" {
					o.Members[i].Shard = node.ID
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Redis reported an unexpected member address")
		}
	}
	o.SlotsAssigned = 16384
	o.SlotsHealthy = true
	o.TopologyFingerprint = fingerprint
	o.Endpoints = []database.Endpoint{{Purpose: "cluster", Host: "database-leader." + DatabaseNamespace(d.ID) + ".svc", Port: 6379}}
	return nil
}

type databaseBoundedWriter struct {
	bytes.Buffer
	limit int
}

func (w *databaseBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, fmt.Errorf("database response exceeds its bound")
	}
	return w.Buffer.Write(p)
}
