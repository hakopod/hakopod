package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const oracleRoleRequest = "hakopod.io/oracle-role-request"

func oracleBrokerOperationsIdle(broker *unstructured.Unstructured) bool {
	operations, _, err := unstructured.NestedMap(broker.Object, "spec", "operations")
	if err != nil {
		return false
	}
	for kind, raw := range operations {
		operation, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		request, _ := operation["requestId"].(string)
		if request == "" {
			continue
		}
		observed, _, _ := unstructured.NestedString(broker.Object, "status", "operations", kind, "observedRequestId")
		phase, _, _ := unstructured.NestedString(broker.Object, "status", "operations", kind, "phase")
		if observed != request || phase != "Succeeded" {
			return false
		}
	}
	return true
}

func (c *Client) oracleRoleBroker(ctx context.Context, d database.Resource) (*unstructured.Unstructured, error) {
	broker, err := c.dynamic.Resource(oracleEnterpriseBrokerResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database-broker", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Oracle role operation broker is unavailable")
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, broker); err != nil {
		return nil, err
	}
	return broker, nil
}

func oracleSwitchoverIdentity(d database.Resource, plan database.OracleSwitchoverReview) error {
	if !oracleEnterprise(d.Spec) || d.Spec.Mode != "cluster" || plan.DatabaseID != d.ID || plan.Project != d.Project || plan.Environment != d.Environment || plan.Revision != d.Revision || plan.BrokerUID == "" || plan.TopologyFingerprint == "" || plan.Primary == "" || plan.Target == plan.Primary || plan.Target == "" || !oracleEnterpriseMemberAllowed(d, plan.TargetController) || len(plan.MemberUIDs) != d.Spec.Members() || len(plan.RequestID) != 32 {
		return fmt.Errorf("Oracle switchover review no longer matches this deployment")
	}
	if _, err := hex.DecodeString(plan.RequestID); err != nil {
		return fmt.Errorf("Oracle switchover request identity is invalid")
	}
	validTarget := false
	for i := 0; i < d.Spec.Members(); i++ {
		if oracleEnterpriseMemberName(i) == plan.TargetController && oracleEnterpriseSID(i) == plan.TargetUniqueName {
			validTarget = true
		}
	}
	if !validTarget {
		return fmt.Errorf("Oracle switchover target identity is invalid")
	}
	for _, uid := range plan.MemberUIDs {
		if uid == "" {
			return fmt.Errorf("Oracle switchover review has an incomplete member identity")
		}
	}
	return nil
}

// ReviewOracleSwitchover performs no mutations. A graceful switchover requires
// every member and redo destination to be healthy; this is not forced failover.
func (c *Client) ReviewOracleSwitchover(ctx context.Context, d database.Resource, target string) (database.OracleSwitchoverReview, error) {
	var plan database.OracleSwitchoverReview
	if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
		return plan, err
	}
	if !oracleEnterprise(d.Spec) || d.Spec.Mode != "cluster" || d.Status != "ready" && d.Status != "failed" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		return plan, fmt.Errorf("Oracle switchover requires a healthy cluster without a recovery operation")
	}
	broker, err := c.oracleRoleBroker(ctx, d)
	if err != nil {
		return plan, err
	}
	if !oracleBrokerOperationsIdle(broker) || broker.GetAnnotations()[oracleRoleRequest] != "" {
		return plan, fmt.Errorf("Oracle already has an unresolved role operation")
	}
	o, err := c.observeOracleEnterpriseDatabase(ctx, d)
	if err != nil || o.Status != "ready" {
		return plan, fmt.Errorf("Oracle switchover requires a completely healthy topology")
	}
	var candidate database.Member
	for _, member := range o.Members {
		if member.Name == target && member.Role == "replica" {
			candidate = member
		}
	}
	if candidate.UID == "" {
		return plan, fmt.Errorf("choose an observed physical standby for switchover")
	}
	_, controller, err := c.oracleEnterpriseExecTarget(ctx, d, candidate)
	if err != nil {
		return plan, err
	}
	request := make([]byte, 16)
	if _, err = rand.Read(request); err != nil {
		return plan, err
	}
	uids := make([]string, len(o.Members))
	uniqueName := ""
	for i, member := range o.Members {
		uids[i] = member.UID
	}
	for i := 0; i < d.Spec.Members(); i++ {
		if oracleEnterpriseMemberName(i) == controller {
			uniqueName = oracleEnterpriseSID(i)
		}
	}
	plan = database.OracleSwitchoverReview{RequestID: hex.EncodeToString(request), DatabaseID: d.ID, Project: d.Project, Environment: d.Environment, Revision: d.Revision, BrokerUID: string(broker.GetUID()), TopologyFingerprint: o.TopologyFingerprint, Primary: o.Primary, Target: candidate.Name, TargetController: controller, TargetUniqueName: uniqueName, MemberUIDs: uids, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	return plan, nil
}

// guardOracleRoleRoute atomically closes routing and records the request on the
// service. Maintenance uses the same resource version and cannot reopen it while
// a role operation is unresolved, even if its earlier observation was healthy.
func (c *Client) guardOracleRoleRoute(ctx context.Context, d database.Resource, request string, release bool, before func() error) error {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	api := c.kube.CoreV1().Services(ns.Name)
	service, err := api.Get(ctx, "database-rw", metav1.GetOptions{})
	if err != nil || !mongodbSupportOwned(service, d, ns.UID) {
		return fmt.Errorf("Oracle role operation endpoint ownership changed")
	}
	annotations := service.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	current := annotations[oracleRoleRequest]
	if current != "" && current != request {
		return fmt.Errorf("another Oracle role operation holds the endpoint")
	}
	if release {
		if current == "" {
			return nil
		}
		delete(annotations, oracleRoleRequest)
	} else {
		annotations[oracleRoleRequest] = request
	}
	service.Annotations = annotations
	service.Spec.Selector = map[string]string{databaseOwner: d.ID, "hakopod.io/oracle-pod": "unverified"}
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, service, metav1.UpdateOptions{})
	return err
}

// RequestOracleSwitchover submits the upstream token once. The caller must save
// the review in PostgreSQL before calling and retry with the same request ID.
func (c *Client) RequestOracleSwitchover(ctx context.Context, d database.Resource, plan database.OracleSwitchoverReview, before func() error) error {
	if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
		return err
	}
	if err := oracleSwitchoverIdentity(d, plan); err != nil {
		return err
	}
	broker, err := c.oracleRoleBroker(ctx, d)
	if err != nil {
		return err
	}
	if string(broker.GetUID()) != plan.BrokerUID {
		return fmt.Errorf("Oracle broker was replaced after review")
	}
	request, _, _ := unstructured.NestedString(broker.Object, "spec", "operations", "switchover", "requestId")
	target, _, _ := unstructured.NestedString(broker.Object, "spec", "operations", "switchover", "target")
	if request == plan.RequestID {
		if target != plan.TargetUniqueName {
			return fmt.Errorf("Oracle switchover token was reused for another target")
		}
		return nil
	}
	if !oracleBrokerOperationsIdle(broker) || broker.GetAnnotations()[oracleRoleRequest] != "" {
		return fmt.Errorf("Oracle has an unresolved broker operation")
	}
	observe := func() (database.Observation, error) {
		o, err := c.observeOracleEnterpriseCore(ctx, d)
		if err == nil {
			err = c.verifyOracleEnterpriseTLS(ctx, d, &o)
		}
		return o, err
	}
	if err = c.prepareOracleSwitchoverRoute(ctx, d, plan, broker, before, observe); err != nil {
		return err
	}
	if err = unstructured.SetNestedMap(broker.Object, map[string]any{"target": plan.TargetUniqueName, "requestId": plan.RequestID}, "spec", "operations", "switchover"); err != nil {
		return err
	}
	annotations := broker.GetAnnotations()
	annotations[oracleRoleRequest] = plan.RequestID
	broker.SetAnnotations(annotations)
	if err = before(); err != nil {
		return err
	}
	_, err = c.dynamic.Resource(oracleEnterpriseBrokerResource).Namespace(DatabaseNamespace(d.ID)).Update(ctx, broker, metav1.UpdateOptions{})
	return err
}

func oracleSwitchoverTopologyMatches(o database.Observation, plan database.OracleSwitchoverReview) bool {
	uids := make([]string, len(o.Members))
	for i, member := range o.Members {
		uids[i] = member.UID
	}
	return o.TopologyFingerprint == plan.TopologyFingerprint && o.Primary == plan.Primary && reflect.DeepEqual(uids, plan.MemberUIDs)
}

// Reject ordinary stale reviews before touching a healthy endpoint. Once the
// endpoint is guarded, repeat native observation to catch a replacement during
// preflight. This helper never submits a broker operation.
func (c *Client) prepareOracleSwitchoverRoute(ctx context.Context, d database.Resource, plan database.OracleSwitchoverReview, broker *unstructured.Unstructured, before func() error, observe func() (database.Observation, error)) error {
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, "database-rw", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Oracle write endpoint is unavailable")
	}
	held := service.Annotations[oracleRoleRequest] == plan.RequestID
	if service.Annotations[oracleRoleRequest] != "" && !held {
		return fmt.Errorf("another Oracle request holds the endpoint")
	}
	if time.Now().After(plan.ExpiresAt) && !held {
		return database.ErrOracleSwitchoverReviewChanged
	}
	o, err := observe()
	if err != nil {
		return err
	}
	if !oracleSwitchoverTopologyMatches(o, plan) {
		if held {
			return database.ErrOracleSwitchoverFailed
		}
		return database.ErrOracleSwitchoverReviewChanged
	}
	if err = c.guardOracleRoleRoute(ctx, d, plan.RequestID, false, before); err != nil {
		return err
	}
	after, observedErr := observe()
	if observedErr == nil && oracleSwitchoverTopologyMatches(after, plan) {
		return nil
	}
	// An old guard may represent a timed-out API write. Keep it closed until
	// that exact token is resolved; a fresh GET cannot exclude an in-flight RPC.
	if held {
		return database.ErrOracleSwitchoverFailed
	}
	current, err := c.oracleRoleBroker(ctx, d)
	if err != nil || current.GetUID() != broker.GetUID() || current.GetResourceVersion() != broker.GetResourceVersion() || !oracleBrokerOperationsIdle(current) || current.GetAnnotations()[oracleRoleRequest] != "" {
		return fmt.Errorf("Oracle broker changed during preflight; routing remains closed")
	}
	// This attempt created the guard and has not attempted a broker write.
	// Release it with a current lease; maintenance still requires native health
	// before opening the selector, including when observation just failed.
	if err = c.guardOracleRoleRoute(ctx, d, plan.RequestID, true, before); err != nil {
		return err
	}
	if observedErr == nil {
		primary, err := oracleObservedPrimary(d, after)
		if err != nil {
			return err
		}
		if err = c.reconcileOracleEnterpriseRoute(ctx, d, &primary, before); err != nil {
			return err
		}
	}
	return database.ErrOracleSwitchoverReviewChanged
}

// OracleSwitchoverComplete checks native roles, every synchronous destination,
// placement and issued TLS before releasing routing. Controller status alone
// never authorizes the new primary. False means the durable worker retries.
func (c *Client) OracleSwitchoverComplete(ctx context.Context, d database.Resource, plan database.OracleSwitchoverReview, before func() error) (bool, error) {
	if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
		return false, err
	}
	if err := oracleSwitchoverIdentity(d, plan); err != nil {
		return false, err
	}
	broker, err := c.oracleRoleBroker(ctx, d)
	if err != nil {
		return false, err
	}
	if string(broker.GetUID()) != plan.BrokerUID {
		return false, fmt.Errorf("Oracle broker was replaced during switchover")
	}
	request, _, _ := unstructured.NestedString(broker.Object, "spec", "operations", "switchover", "requestId")
	target, _, _ := unstructured.NestedString(broker.Object, "spec", "operations", "switchover", "target")
	if request != plan.RequestID || target != plan.TargetUniqueName {
		return false, fmt.Errorf("Oracle broker does not contain the reviewed switchover")
	}
	observed, _, _ := unstructured.NestedString(broker.Object, "status", "operations", "switchover", "observedRequestId")
	phase, _, _ := unstructured.NestedString(broker.Object, "status", "operations", "switchover", "phase")
	if observed != request {
		return false, nil
	}
	if phase == "Failed" {
		return false, database.ErrOracleSwitchoverFailed
	}
	if phase != "Succeeded" {
		return false, nil
	}
	o, err := c.observeOracleEnterpriseCore(ctx, d)
	if err != nil {
		return false, nil
	}
	if err = c.verifyOracleEnterpriseTLS(ctx, d, &o); err != nil {
		return false, err
	}
	primary, err := oracleObservedPrimary(d, o)
	if err != nil {
		return false, err
	}
	_, controller, err := c.oracleEnterpriseExecTarget(ctx, d, primary)
	if err != nil || controller != plan.TargetController {
		return false, fmt.Errorf("Oracle native primary differs from the reviewed target")
	}
	// Retain the completed token in spec/status for retry safety. Only release
	// the transient guard; a later request must receive a new review and token.
	annotations := broker.GetAnnotations()
	if guard := annotations[oracleRoleRequest]; guard != "" && guard != request {
		return false, fmt.Errorf("Oracle broker role guard changed")
	}
	if annotations[oracleRoleRequest] != "" {
		delete(annotations, oracleRoleRequest)
		broker.SetAnnotations(annotations)
		if err = before(); err != nil {
			return false, err
		}
		if _, err = c.dynamic.Resource(oracleEnterpriseBrokerResource).Namespace(DatabaseNamespace(d.ID)).Update(ctx, broker, metav1.UpdateOptions{}); err != nil {
			return false, err
		}
	}
	if err = c.guardOracleRoleRoute(ctx, d, request, true, before); err != nil {
		return false, err
	}
	if err = c.reconcileOracleEnterpriseRoute(ctx, d, &primary, before); err != nil {
		return false, err
	}
	return true, nil
}
