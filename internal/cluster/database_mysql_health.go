package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const mysqlGroupQuery = `SELECT JSON_OBJECT(
 'uuid', @@server_uuid, 'read_only', @@super_read_only,
 'secure', @@require_secure_transport, 'tls', @@tls_version,
 'group_tls', @@group_replication_ssl_mode,
 'recovery_tls', @@group_replication_recovery_use_ssl,
 'members', (SELECT JSON_ARRAYAGG(JSON_OBJECT('id',MEMBER_ID,'host',MEMBER_HOST,'role',MEMBER_ROLE,'state',MEMBER_STATE)) FROM performance_schema.replication_group_members)
)`

type mysqlGroupView struct {
	UUID        string                                   `json:"uuid"`
	ReadOnly    int                                      `json:"read_only"`
	Secure      int                                      `json:"secure"`
	TLS         string                                   `json:"tls"`
	GroupTLS    string                                   `json:"group_tls"`
	RecoveryTLS int                                      `json:"recovery_tls"`
	Members     []struct{ ID, Host, Role, State string } `json:"members"`
}

func verifyMySQLGroupView(raw []byte, d database.Resource, member database.Member, members []database.Member) (string, string, error) {
	var view mysqlGroupView
	if len(raw) > 16<<10 || json.Unmarshal(raw, &view) != nil || view.Secure != 1 || view.TLS != "TLSv1.2,TLSv1.3" || view.GroupTLS != "VERIFY_IDENTITY" || view.RecoveryTLS != 1 || len(view.Members) != d.Spec.Members() {
		return "", "", fmt.Errorf("MySQL replication or transport policy is not healthy")
	}
	expected := map[string]bool{}
	for _, m := range members {
		expected[m.Name] = true
	}
	seen, ids := map[string]bool{}, map[string]bool{}
	primary, role := "", ""
	for _, m := range view.Members {
		name, suffix, ok := strings.Cut(m.Host, ".")
		if !ok || suffix != "database-instances."+DatabaseNamespace(d.ID)+".svc.cluster.local" || !expected[name] || seen[name] || m.ID == "" || ids[m.ID] || m.State != "ONLINE" {
			return "", "", fmt.Errorf("MySQL replication membership does not match its owned pods")
		}
		seen[name], ids[m.ID] = true, true
		switch m.Role {
		case "PRIMARY":
			if primary != "" {
				return "", "", fmt.Errorf("MySQL has multiple write primaries")
			}
			primary = name
		case "SECONDARY":
		default:
			return "", "", fmt.Errorf("MySQL member role is unavailable")
		}
		if name == member.Name && m.ID == view.UUID {
			role = m.Role
		}
	}
	if primary == "" || role == "" || (role == "PRIMARY" && view.ReadOnly != 0) || (role == "SECONDARY" && view.ReadOnly != 1) {
		return "", "", fmt.Errorf("MySQL local role differs from replication membership")
	}
	if role == "PRIMARY" {
		return primary, "primary", nil
	}
	return primary, "replica", nil
}

func (c *Client) observeMySQLDatabase(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation) error {
	// The operator can retain ONLINE_PARTIAL after a completed scale-down.
	// Both states still require exact owned pods and native membership, role,
	// routing and TLS checks before the database can become ready.
	status, _, _ := unstructured.NestedString(object.Object, "status", "cluster", "status")
	if status != "ONLINE" && status != "ONLINE_PARTIAL" {
		return fmt.Errorf("waiting for MySQL Group Replication")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	primaries := make([]string, len(o.Members))
	members := append([]database.Member(nil), o.Members...)
	for i, m := range o.Members {
		group.Go(func() error {
			out := &databaseBoundedWriter{limit: 16 << 10}
			if err := c.DatabaseExec(step, d, m, mysqlLocalCommand(mysqlGroupQuery), nil, out); err != nil {
				return err
			}
			primary, role, err := verifyMySQLGroupView(out.Bytes(), d, m, members)
			if err == nil {
				primaries[i], o.Members[i].Role = primary, role
			}
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for _, p := range primaries {
		if p == "" || (o.Primary != "" && p != o.Primary) {
			return fmt.Errorf("MySQL members disagree about their primary")
		}
		o.Primary = p
	}
	identities := make([]string, len(o.Members))
	for i, m := range o.Members {
		identities[i] = m.Name + ":" + m.UID + ":" + m.Role
	}
	slices.Sort(identities)
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "\n"))))
	return c.observeMySQLRouters(ctx, d, object, o)
}

func (c *Client) observeMySQLRouters(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation) error {
	o.Routing = &database.RoutingObservation{Kind: "mysql-router", Members: []database.Member{}}
	ns := DatabaseNamespace(d.ID)
	dep, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "database-router", metav1.GetOptions{})
	if err != nil || dep.DeletionTimestamp != nil {
		return fmt.Errorf("MySQL routers are unavailable")
	}
	owned := false
	for _, ref := range dep.OwnerReferences {
		owned = owned || ref.UID == object.GetUID() && ref.APIVersion == "mysql.oracle.com/v2" && ref.Kind == "InnoDBCluster"
	}
	if !owned {
		return fmt.Errorf("MySQL router deployment ownership changed")
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 5, LabelSelector: databaseRouterLabel + "=true", FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) > 4 {
		return fmt.Errorf("MySQL router inventory exceeds its bound")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	ready := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		m, err := c.mysqlRouterMember(ctx, d, dep, pod, policy)
		if err != nil {
			return err
		}
		if m.Ready {
			ready++
		}
		o.Routing.Members = append(o.Routing.Members, m)
	}
	c.observeDatabaseMemberPlacement(ctx, o.Routing.Members)
	placement := d.Spec
	placement.Replicas = d.Spec.RouterInstances() - 1
	if ready != d.Spec.RouterInstances() || !databasePlacementObservation(placement, o.Routing.Members).Verified {
		return fmt.Errorf("waiting for MySQL router capacity and placement")
	}
	service, err := c.kube.CoreV1().Services(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("MySQL router service is unavailable")
	}
	owned = false
	for _, ref := range service.OwnerReferences {
		owned = owned || ref.UID == object.GetUID() && ref.Kind == "InnoDBCluster" && ref.APIVersion == "mysql.oracle.com/v2"
	}
	if !owned {
		return fmt.Errorf("MySQL router service ownership changed")
	}
	o.Routing.Ready = true
	o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_write", Host: "database." + ns + ".svc", Port: 6446})
	if d.Spec.Replicas > 0 {
		o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_only", Host: "database." + ns + ".svc", Port: 6447})
	}
	return nil
}

// Use the same live identity and readiness checks during observation and
// publication. A previously ready UID alone does not prove a current Router.
func (c *Client) mysqlRouterMember(ctx context.Context, d database.Resource, dep *appsv1.Deployment, pod corev1.Pod, policy *DatabasePolicy) (database.Member, error) {
	if dep == nil || dep.UID == "" || dep.DeletionTimestamp != nil || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Namespace != DatabaseNamespace(d.ID) {
		return database.Member{}, fmt.Errorf("MySQL router identity changed")
	}
	owned := false
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "ReplicaSet" || ref.APIVersion != "apps/v1" {
			continue
		}
		rs, err := c.kube.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil || rs.UID != ref.UID || rs.UID == "" || rs.DeletionTimestamp != nil {
			return database.Member{}, fmt.Errorf("MySQL router replica ownership changed")
		}
		for _, parent := range rs.OwnerReferences {
			owned = owned || parent.UID == dep.UID && parent.Name == dep.Name && parent.Kind == "Deployment" && parent.APIVersion == "apps/v1"
		}
	}
	if !owned || pod.Labels[databaseOwner] != d.ID {
		return database.Member{}, fmt.Errorf("MySQL router member ownership changed")
	}
	m := database.Member{Name: pod.Name, UID: string(pod.UID), Role: "router", Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
	stamp := pod.CreationTimestamp.Time
	m.CreatedAt = &stamp
	matching := pod.Status.Phase == corev1.PodRunning && len(pod.Spec.Containers) == 1 && len(pod.Spec.InitContainers) == 0 && databasePodPolicyMatches(pod, policy)
	for _, container := range pod.Spec.Containers {
		matching = matching && container.Name == "router" && container.Image == mysqlRouterImage && container.Resources.Requests.Cpu().String() == database.MySQLRouterCPU && container.Resources.Limits.Cpu().String() == database.MySQLRouterCPU && container.Resources.Requests.Memory().String() == database.MySQLRouterMemory && container.Resources.Limits.Memory().String() == database.MySQLRouterMemory
		m.Image = container.Image
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			m.Ready = matching
		}
	}
	for _, state := range pod.Status.ContainerStatuses {
		m.Restarts += state.RestartCount
	}
	return m, nil
}

const mysqlTLSProbe = `set -eu
IFS= read -r MYSQL_PWD; export MYSQL_PWD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
result=$(mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --batch --raw --skip-column-names --execute="SELECT @@super_read_only; SHOW SESSION STATUS LIKE 'Ssl_version'")
case "$3:$result" in
  "primary:0
Ssl_version	TLSv1.2"|"primary:0
Ssl_version	TLSv1.3"|"replica:1
Ssl_version	TLSv1.2"|"replica:1
Ssl_version	TLSv1.3") ;;
  *) exit 1;;
esac
if mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=DISABLED --execute='SELECT 1' > /dev/null 2> "$work/plaintext"; then exit 1; fi
grep -E 'ERROR 1045|insecure transport|requires a TLS|SSL connection is required|requires secure connection|Connections using insecure transport|SSL is required|SSL connection required' "$work/plaintext" >/dev/null
`

// The pinned sidecar supplies openssl for the native MySQL TLS negotiation.
// Certificate inspection sends no database password and returns public PEM only.
const mysqlCertificateProbe = `set -eu
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
openssl s_client -starttls mysql -connect "$1:$2" -servername "$1" -verify_hostname "$1" -verify_return_error -CAfile "$work/ca.crt" -showcerts < /dev/null 2>/dev/null
`

func (c *Client) verifyMySQLTLS(ctx context.Context, d database.Resource, o *database.Observation, trust database.PublicTrust, status *database.TLSObservation) error {
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || bytes.ContainsAny(secret.Data["password"], "\r\n") {
		return fmt.Errorf("MySQL TLS credentials are unavailable")
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	type target struct {
		member     database.Member
		host, role string
		port       int
	}
	targets := []target{}
	for _, m := range o.Members {
		targets = append(targets, target{m, m.Name + ".database-instances." + DatabaseNamespace(d.ID) + ".svc.cluster.local", m.Role, 3306})
	}
	for _, e := range o.Endpoints {
		role := "primary"
		if e.Purpose == "read_only" {
			role = "replica"
		}
		targets = append(targets, target{o.Members[0], e.Host, role, e.Port})
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, t := range targets {
		group.Go(func() error {
			out := &databaseBoundedWriter{limit: 32 << 10}
			input := bytes.Join([][]byte{secret.Data["password"], []byte(trust.CertificatePEM)}, []byte{'\n'})
			if err := c.DatabaseExec(step, d, t.member, []string{"sh", "-c", mysqlTLSProbe, "verify-mysql-tls", t.host, strconv.Itoa(t.port), t.role}, bytes.NewReader(input), out); err != nil {
				return err
			}
			if err := c.databaseExecContainer(step, d, t.member, "sidecar", []string{"sh", "-c", mysqlCertificateProbe, "mysql-certificate", t.host, strconv.Itoa(t.port)}, strings.NewReader(trust.CertificatePEM), out); err != nil {
				return err
			}
			block, _ := pem.Decode(out.Bytes())
			if block == nil || block.Type != "CERTIFICATE" {
				return fmt.Errorf("MySQL active certificate is unavailable")
			}
			active, err := database.VerifyServerCertificate(pem.EncodeToMemory(block), ca, []string{t.host}, time.Now())
			if err != nil || active.Fingerprint != status.Fingerprint {
				return fmt.Errorf("MySQL active certificate differs from its issued certificate")
			}
			return nil
		})
	}
	return group.Wait()
}
