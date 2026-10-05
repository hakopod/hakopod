package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type oracleEnterpriseHealth struct {
	Role                string   `json:"role"`
	Mode                string   `json:"mode"`
	Version             string   `json:"version"`
	PDB                 string   `json:"pdb"`
	UniqueName          string   `json:"unique_name"`
	DBID                string   `json:"dbid"`
	Protection          string   `json:"protection"`
	ApplyProcesses      int      `json:"apply_processes"`
	Synchronized        int      `json:"synchronized"`
	SynchronizedMembers []string `json:"synchronized_members"`
}

const oracleEnterpriseHealthSQL = `SELECT JSON_OBJECT(
 'role' VALUE database_role, 'mode' VALUE open_mode,
 'version' VALUE (SELECT version_full FROM v$instance),
 'pdb' VALUE (SELECT open_mode FROM v$pdbs WHERE name='APPDB'),
 'unique_name' VALUE db_unique_name, 'dbid' VALUE TO_CHAR(dbid),
 'protection' VALUE protection_level,
 'apply_processes' VALUE (SELECT COUNT(*) FROM v$managed_standby WHERE process='MRP0' AND status IN ('APPLYING_LOG','WAIT_FOR_LOG')),
 'synchronized' VALUE (SELECT COUNT(*) FROM v$archive_dest_status WHERE status='VALID' AND type='PHYSICAL' AND synchronized='YES'),
 'synchronized_members' VALUE COALESCE((SELECT JSON_ARRAYAGG(db_unique_name ORDER BY db_unique_name) FROM v$archive_dest_status WHERE status='VALID' AND type='PHYSICAL' AND synchronized='YES'),'[]') FORMAT JSON) FROM v$database`

func validateOracleEnterpriseHealth(s database.Spec, health []oracleEnterpriseHealth) (int, error) {
	if len(health) != s.Members() {
		return -1, fmt.Errorf("Oracle has not reported every expected member")
	}
	primary, dbid := -1, ""
	for i, h := range health {
		if !strings.HasPrefix(h.Version, s.Version+".") || h.UniqueName != oracleEnterpriseSID(i) || h.DBID == "" {
			return -1, fmt.Errorf("Oracle member version or database identity does not match the accepted deployment")
		}
		if dbid == "" {
			dbid = h.DBID
		}
		if h.DBID != dbid {
			return -1, fmt.Errorf("Oracle members do not belong to the same database")
		}
		switch h.Role {
		case "PRIMARY":
			if primary >= 0 || h.Mode != "READ WRITE" || h.PDB != "READ WRITE" {
				return -1, fmt.Errorf("Oracle has no unique writable primary")
			}
			if s.Mode == "cluster" && (h.Protection != "MAXIMUM AVAILABILITY" || h.Synchronized != s.Replicas) {
				return -1, fmt.Errorf("Oracle synchronous redo transport is not healthy for every standby")
			}
			if s.Mode == "cluster" {
				wanted := []string{}
				for member := 0; member < s.Members(); member++ {
					if member != i {
						wanted = append(wanted, oracleEnterpriseSID(member))
					}
				}
				observed := append([]string(nil), h.SynchronizedMembers...)
				sort.Strings(wanted)
				sort.Strings(observed)
				if strings.Join(wanted, ",") != strings.Join(observed, ",") {
					return -1, fmt.Errorf("Oracle synchronized destinations differ from the accepted standby set")
				}
			}
			primary = i
		case "PHYSICAL STANDBY":
			// Opening a standby for reads is an Active Data Guard licensing and
			// qualification decision. This contract keeps physical standbys mounted.
			if s.Mode != "cluster" || h.Mode != "MOUNTED" || h.ApplyProcesses != 1 {
				return -1, fmt.Errorf("Oracle physical standby redo apply is not healthy")
			}
		default:
			return -1, fmt.Errorf("Oracle member role is outside the accepted Data Guard topology")
		}
	}
	if primary < 0 {
		return -1, fmt.Errorf("Oracle has no observed primary")
	}
	return primary, nil
}

func (c *Client) oracleEnterpriseInventory(ctx context.Context, d database.Resource) ([]database.Member, []corev1.Pod, error) {
	if err := d.Spec.Validate(); err != nil {
		return nil, nil, err
	}
	if _, err := c.oracleEnterpriseNamespace(ctx, d); err != nil {
		return nil, nil, err
	}
	list, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: databaseOwner + "=" + d.ID + "," + oracleEnterpriseMemberLabel + "=true", FieldSelector: activeDatabasePodFields, Limit: database.MaxMembers + 1})
	if err != nil {
		return nil, nil, fmt.Errorf("Oracle member inventory is unavailable")
	}
	if list.Continue != "" || len(list.Items) > database.MaxMembers {
		return nil, nil, fmt.Errorf("Oracle member inventory exceeds its bound")
	}
	members := make([]database.Member, d.Spec.Members())
	pods := make([]corev1.Pod, d.Spec.Members())
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	for _, pod := range list.Items {
		m := database.Member{Name: pod.Name, UID: string(pod.UID), Image: d.Spec.Oracle.Image, Role: "unknown", Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
		_, controller, err := c.oracleEnterpriseExecTarget(ctx, d, m)
		if err != nil {
			return nil, nil, err
		}
		index := -1
		for i := 0; i < d.Spec.Members(); i++ {
			if oracleEnterpriseMemberName(i) == controller {
				index = i
			}
		}
		if index < 0 || members[index].UID != "" {
			return nil, nil, fmt.Errorf("Oracle member replacement has not converged")
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				m.Ready = true
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			m.Restarts += status.RestartCount
		}
		stamp := pod.CreationTimestamp.Time
		m.CreatedAt = &stamp
		m.Ready = m.Ready && databasePodPolicyMatches(pod, policy) && oracleEnterprisePodMatches(pod, d)
		members[index], pods[index] = m, pod
	}
	return members, pods, nil
}

func oracleEnterprisePodMatches(pod corev1.Pod, d database.Resource) bool {
	if len(pod.Spec.Containers) != 1 || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		return false
	}
	container := pod.Spec.Containers[0]
	if container.Image != d.Spec.Oracle.Image || container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		return false
	}
	requests, limits := container.Resources.Requests, container.Resources.Limits
	return requests.Cpu().Cmp(resource.MustParse(d.Spec.CPU)) == 0 && requests.Memory().Cmp(resource.MustParse(d.Spec.Memory)) == 0 && requests.Cpu().Cmp(*limits.Cpu()) == 0 && requests.Memory().Cmp(*limits.Memory()) == 0 && !limits.StorageEphemeral().IsZero()
}

func (c *Client) observeOracleEnterpriseCore(ctx context.Context, d database.Resource) (database.Observation, error) {
	o := database.Observation{ObservedAt: time.Now().UTC(), Revision: d.Revision, Status: "pending", Members: []database.Member{}, Endpoints: []database.Endpoint{}}
	members, pods, err := c.oracleEnterpriseInventory(ctx, d)
	if err != nil {
		return o, err
	}
	o.Members = members
	for _, member := range members {
		if member.UID == "" || !member.Ready {
			return o, fmt.Errorf("Oracle members are still initializing or replacing")
		}
	}
	health := make([]oracleEnterpriseHealth, len(members))
	group, check := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, member := range members {
		group.Go(func() error {
			step, stop := context.WithTimeout(check, 12*time.Second)
			defer stop()
			output, err := c.oracleLocalQuery(step, d, member, oracleEnterpriseHealthSQL)
			if err != nil {
				return err
			}
			if json.Unmarshal([]byte(output), &health[i]) != nil {
				return fmt.Errorf("Oracle native role observation is incomplete")
			}
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return o, err
	}
	primary, err := validateOracleEnterpriseHealth(d.Spec, health)
	if err != nil {
		return o, err
	}
	for i := range members {
		o.Members[i].Role = "replica"
	}
	o.Members[primary].Role = "primary"
	o.Primary = o.Members[primary].Name
	if d.Spec.Mode == "cluster" {
		broker, err := c.dynamic.Resource(oracleEnterpriseBrokerResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database-broker", metav1.GetOptions{})
		if err != nil || broker.GetLabels()[databaseOwner] != d.ID || broker.GetLabels()[managedBy] != "hakopod" || broker.GetDeletionTimestamp() != nil {
			return o, fmt.Errorf("Oracle Data Guard broker ownership or availability changed")
		}
		if err = c.oracleEnterpriseObjectOwned(ctx, d, broker); err != nil {
			return o, err
		}
		conditions, _, _ := unstructured.NestedSlice(broker.Object, "status", "conditions")
		ready := false
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if ok && condition["type"] == "Ready" && condition["status"] == "True" && condition["observedGeneration"] == broker.GetGeneration() {
				ready = true
			}
		}
		fsfo, _, _ := unstructured.NestedString(broker.Object, "status", "fastStartFailover")
		brokerPrimary, _, _ := unstructured.NestedString(broker.Object, "status", "primaryDatabase")
		if !ready || fsfo != "false" || brokerPrimary != health[primary].UniqueName {
			return o, fmt.Errorf("Oracle broker and native member roles have not converged")
		}
	}
	c.observeDatabasePlacement(ctx, d, &o)
	if d.Spec.Placement.Spread != "" && (o.Placement == nil || !o.Placement.Verified) {
		return o, fmt.Errorf("Oracle member placement has not been verified")
	}
	step, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	client, err := c.oracleApplicationConnection(step, d, o.Members[primary], true)
	if err != nil {
		return o, err
	}
	defer client.Close()
	var identity string
	if err = client.QueryRowContext(step, "SELECT SYS_CONTEXT('USERENV','SESSION_USER')||'|'||SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&identity); err != nil || identity != "APP|APPDB" {
		return o, fmt.Errorf("Oracle application account or required TCPS could not be verified")
	}
	identities := make([]string, len(o.Members))
	for i, member := range o.Members {
		identities[i] = member.UID + ":" + member.Role
	}
	sort.Strings(identities)
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "|"))))
	o.Endpoints = []database.Endpoint{{Purpose: "read_write", Host: oracleHost(d), Port: 2484}}
	c.observeDatabaseMetrics(ctx, d, pods, &o)
	return o, nil
}

func (c *Client) observeOracleEnterpriseDatabase(ctx context.Context, d database.Resource) (database.Observation, error) {
	o, err := c.observeOracleEnterpriseCore(ctx, d)
	if err != nil {
		return o, err
	}
	if err = c.databaseEndpointsReady(ctx, d, o, nil); err != nil {
		return o, err
	}
	if err = c.observeDatabaseTLS(ctx, d, &o, nil); err != nil {
		return o, err
	}
	o.Status = "ready"
	c.observeDatabaseEngineMetrics(ctx, d, &o)
	return o, nil
}

func (c *Client) renewOracleEnterpriseSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
		return err
	}
	if _, err := c.prepareOracleEnterpriseRegistry(ctx, d, before); err != nil {
		return err
	}
	if err := c.prepareOracleEnterpriseApplication(ctx, d, before); err != nil {
		return errors.Join(err, c.reconcileOracleEnterpriseRoute(ctx, d, nil, before))
	}
	o, err := c.observeOracleEnterpriseCore(ctx, d)
	if err != nil {
		return errors.Join(err, c.reconcileOracleEnterpriseRoute(ctx, d, nil, before))
	}
	if err = c.verifyOracleTLS(ctx, d, &o); err != nil {
		return errors.Join(err, c.reconcileOracleEnterpriseRoute(ctx, d, nil, before))
	}
	for _, member := range o.Members {
		if member.Name == o.Primary {
			return c.reconcileOracleEnterpriseRoute(ctx, d, &member, before)
		}
	}
	return fmt.Errorf("Oracle routing has no verified primary")
}

func (c *Client) prepareOracleEnterpriseApplication(ctx context.Context, d database.Resource, before func() error) error {
	members, _, err := c.oracleEnterpriseInventory(ctx, d)
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || len(secret.Data["password"]) != 64 {
		return fmt.Errorf("Oracle application credentials are unavailable")
	}
	password := string(secret.Data["password"])
	if _, err = hex.DecodeString(password); err != nil {
		return fmt.Errorf("Oracle application credential is invalid")
	}
	var primary *database.Member
	for _, member := range members {
		if member.UID == "" || !member.Ready {
			continue
		}
		role, err := c.oracleLocalQuery(ctx, d, member, "SELECT database_role FROM v$database")
		if err != nil {
			return err
		}
		if role == "PRIMARY" {
			if primary != nil {
				return fmt.Errorf("Oracle has multiple primaries; application routing remains closed")
			}
			value := member
			primary = &value
		}
	}
	if primary == nil {
		return fmt.Errorf("Oracle has no primary for application initialization")
	}
	if err = before(); err != nil {
		return err
	}
	query := `ALTER SESSION SET CONTAINER=APPDB;
DECLARE n NUMBER;
BEGIN
 SELECT COUNT(*) INTO n FROM dba_users WHERE username='APP';
 IF n=0 THEN
  EXECUTE IMMEDIATE 'CREATE USER APP IDENTIFIED BY "` + password + `" DEFAULT TABLESPACE USERS';
  EXECUTE IMMEDIATE 'GRANT CREATE SESSION, CREATE TABLE, CREATE VIEW, CREATE SEQUENCE, CREATE PROCEDURE, CREATE TRIGGER, CREATE TYPE TO APP';
 END IF;
END;
/
ALTER USER APP QUOTA ` + strconv.FormatInt(max(1, d.Spec.StorageGiB-8), 10) + `G ON USERS;
CREATE OR REPLACE TRIGGER SYS.HAKOPOD_CAPTURE_DDL_GUARD BEFORE DDL ON DATABASE
DECLARE lock_result NUMBER;
BEGIN
 IF ORA_DICT_OBJ_OWNER='APP' AND SYS_CONTEXT('USERENV','SESSION_USER')='APP' THEN
  lock_result:=DBMS_LOCK.REQUEST(id=>82635001,lockmode=>DBMS_LOCK.S_MODE,timeout=>0,release_on_commit=>TRUE);
  IF lock_result NOT IN (0,4) THEN RAISE_APPLICATION_ERROR(-20010,'Schema changes are temporarily paused for a managed backup. Retry after capture completes.'); END IF;
 END IF;
END;
/`
	_, err = c.oracleLocalQuery(ctx, d, *primary, query)
	return err
}
