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
	ns := DatabaseNamespace(d.ID)
	ports := []networkingv1.NetworkPolicyPort{}
	for _, port := range []int{5432, 6379, 16379, 8000} {
		p := intstr.FromInt(port)
		ports = append(ports, networkingv1.NetworkPolicyPort{Port: &p})
	}
	local := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	operators := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"cnpg-system", "redis-operator"}}}}}
	apps := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/database-access-" + d.ID: "true"}}}
	dnsPort := intstr.FromInt(53)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns, Labels: databaseLabels(d)}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: databaseRecoveryHelper, Operator: metav1.LabelSelectorOpDoesNotExist}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{local, operators, apps}, Ports: ports}}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{local}, Ports: ports}, {To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: &dnsPort, Protocol: &udp}, {Port: &dnsPort, Protocol: &tcp}}}}}}
	if d.Status == "restoring" {
		policy.Spec.Ingress[0].From = []networkingv1.NetworkPolicyPeer{local, operators}
	}
	if d.Spec.Engine == "postgresql" {
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

// DatabaseExec does not accept an arbitrary namespace or pod identity. It checks
// the resource, controller owner, member UID and container immediately before exec.
func (c *Client) DatabaseExec(ctx context.Context, d database.Resource, member database.Member, command []string, stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database namespace ownership could not be verified")
	}
	gvr, _ := databaseGVR(d.Spec)
	object, err := c.dynamic.Resource(gvr).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetLabels()[databaseOwner] != d.ID {
		return fmt.Errorf("database controller ownership could not be verified")
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || pod.UID != types.UID(member.UID) || pod.DeletionTimestamp != nil || !c.databasePodOwned(ctx, *pod, object.GetUID()) {
		return fmt.Errorf("database member changed; obtain a new observation")
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
		return fmt.Errorf("database execution transport or pinned container is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace(ns.Name).Name(member.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdin: stdin != nil, Stdout: stdout != nil, Stderr: true, TTY: false}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return fmt.Errorf("database execution transport is unavailable")
	}
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: io.Discard, Tty: false}); err != nil {
		return fmt.Errorf("database command did not complete successfully")
	}
	return nil
}
func (c *Client) redisCommand(ctx context.Context, d database.Resource, m database.Member, subcommand string) (string, error) {
	// subcommand is supplied only by controller code. Credentials enter through
	// stdin and are never present in argv, a response, or a persisted operation.
	if subcommand != "CLUSTER NODES" && subcommand != "CLUSTER INFO" && subcommand != "PING" && subcommand != "DBSIZE" {
		return "", fmt.Errorf("unsupported Redis health command")
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
	raw, err := c.redisCommand(ctx, d, o.Members[0], "CLUSTER NODES")
	if err != nil {
		return err
	}
	nodes, fingerprint, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
	if err != nil {
		return err
	}
	info, err := c.redisCommand(ctx, d, o.Members[0], "CLUSTER INFO")
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
			step, cancel := context.WithTimeout(checkCtx, 5*time.Second)
			defer cancel()
			raw, err := c.redisCommand(step, d, member, "CLUSTER NODES")
			if err != nil {
				return err
			}
			_, other, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
			if err != nil || other != fingerprint {
				return fmt.Errorf("Redis members do not agree on slot ownership")
			}
			info, err := c.redisCommand(step, d, member, "CLUSTER INFO")
			if err != nil {
				return err
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
