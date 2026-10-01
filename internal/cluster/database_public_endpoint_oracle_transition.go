package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func oraclePublicEndpointTemplateHash(template corev1.PodTemplateSpec) (string, error) {
	encoded, err := json.Marshal(template)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func oraclePublicEndpointTransitionIntent(transition database.PublicEndpointIdentityTransition) (string, error) {
	intent := struct {
		SchemaVersion, DatabaseRevision, EndpointRevision                           int64
		OperationID, Engine, Kind, RouteFingerprint                                 string
		DesiredNames                                                                []string
		ReviewedTopologyFingerprint, ReviewedLeafFingerprint, ReviewedCAFingerprint string
		StatefulSetUID                                                              string
		OriginalGeneration                                                          int64
		OriginalTemplateHash                                                        string
		OldMember                                                                   database.PublicEndpointTransitionMember
		PVCs                                                                        []database.PublicEndpointTransitionPVC
	}{
		SchemaVersion: int64(transition.SchemaVersion), DatabaseRevision: transition.DatabaseRevision, EndpointRevision: transition.EndpointRevision,
		OperationID: transition.OperationID, Engine: transition.Engine, Kind: transition.Kind, RouteFingerprint: transition.RouteFingerprint,
		DesiredNames: transition.DesiredNames, ReviewedTopologyFingerprint: transition.ReviewedTopologyFingerprint,
		ReviewedLeafFingerprint: transition.ReviewedLeafFingerprint, ReviewedCAFingerprint: transition.ReviewedCAFingerprint,
		StatefulSetUID: transition.StatefulSetUID, OriginalGeneration: transition.OriginalGeneration, OriginalTemplateHash: transition.OriginalTemplateHash,
		OldMember: transition.OldMember, PVCs: transition.PVCs,
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func oraclePublicEndpointIdentityNamesExact(actual, expected []string) bool {
	return reflect.DeepEqual(actual, expected)
}

func oraclePublicEndpointIssuedCAStable(actual, reviewed string) error {
	if actual != reviewed {
		return fmt.Errorf("Oracle Free issuer changed during identity issuance")
	}
	return nil
}

func (c *Client) oraclePublicEndpointIssuedIdentity(ctx context.Context, d database.Resource, desiredNames []string, intent string) (string, string, *corev1.Secret, error) {
	ns, _, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil {
		return "", "", nil, err
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil || secret.UID == "" || intent != "" && secret.Annotations["hakopod.io/public-endpoint-identity-intent"] != intent {
		return "", "", nil, fmt.Errorf("Oracle Free public identity does not match its durable intent")
	}
	if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
		return "", "", nil, err
	}
	trust, ca, err := database.ParsePublicTrust(secret.Data["ca.crt"], time.Now())
	if err != nil {
		return "", "", nil, err
	}
	copy := d
	copy.PublicEndpointNames = desiredNames
	expectedNames := databaseIdentityNames(copy)
	identity, err := database.VerifyServerCertificate(secret.Data[corev1.TLSCertKey], ca, expectedNames, time.Now())
	if err != nil {
		return "", "", nil, fmt.Errorf("Oracle Free public identity is invalid: %w", err)
	}
	if !oraclePublicEndpointIdentityNamesExact(identity.DNSNames, expectedNames) {
		return "", "", nil, fmt.Errorf("Oracle Free public identity contains unexpected names")
	}
	return identity.Fingerprint, trust.Fingerprint, secret, nil
}

func oraclePublicEndpointDesiredTemplate(current corev1.PodTemplateSpec, identity *corev1.Secret) (corev1.PodTemplateSpec, string, error) {
	desired := *current.DeepCopy()
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	desired.Annotations["hakopod.io/oracle-identity"] = oracleIdentityFingerprint(identity)
	hash, err := oraclePublicEndpointTemplateHash(desired)
	return desired, hash, err
}

func (c *Client) oraclePublicEndpointIssuerStable(ctx context.Context, d database.Resource, namespace *corev1.Namespace, expectedFingerprint string) error {
	issuer, err := c.kube.CoreV1().Secrets(namespace.Name).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil || !boundedDatabaseKeyPair(issuer.Data["ca.crt"], issuer.Data["ca.key"]) {
		return fmt.Errorf("Oracle Free issuer is unavailable for identity transition")
	}
	if err = databaseIdentityOwned(issuer, d, namespace.UID); err != nil {
		return err
	}
	trust, certificate, err := database.ParsePublicTrust(issuer.Data["ca.crt"], time.Now())
	if err != nil || expectedFingerprint == "" || trust.Fingerprint != expectedFingerprint || certificate.NotAfter.Before(time.Now().Add(31*24*time.Hour)) {
		return fmt.Errorf("Oracle Free issuer requires renewal before identity transition")
	}
	return nil
}

func oraclePublicEndpointOldResourcesMatch(d database.Resource, transition database.PublicEndpointIdentityTransition, set *appsv1.StatefulSet, pod *corev1.Pod, pvcs []database.PublicEndpointTransitionPVC) error {
	if !d.Observation.Fresh(time.Now(), d.Revision) || d.Observation.Status != "ready" || d.Observation.TopologyFingerprint != transition.ReviewedTopologyFingerprint || d.Observation.TLS == nil || !d.Observation.TLS.Verified || !d.Observation.TLS.PlaintextRejected || d.Observation.TLS.Fingerprint != transition.ReviewedLeafFingerprint || d.Observation.TLS.CAFingerprint != transition.ReviewedCAFingerprint || len(d.Observation.Members) != 1 || d.Observation.Members[0].Name != transition.OldMember.Name || d.Observation.Members[0].UID != transition.OldMember.UID {
		return fmt.Errorf("Oracle Free reviewed member or served identity changed before issuance")
	}
	if set == nil || string(set.UID) != transition.StatefulSetUID || set.Generation != transition.OriginalGeneration || set.Status.ObservedGeneration != transition.OriginalGeneration {
		return fmt.Errorf("Oracle Free StatefulSet changed before identity issuance")
	}
	hash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil || hash != transition.OriginalTemplateHash {
		return fmt.Errorf("Oracle Free pod template changed before identity issuance")
	}
	if pod == nil || string(pod.UID) != transition.OldMember.UID {
		return fmt.Errorf("Oracle Free member changed before identity issuance")
	}
	if !reflect.DeepEqual(pvcs, transition.PVCs) {
		return fmt.Errorf("Oracle Free persistent identity changed before issuance")
	}
	return nil
}

func oraclePublicEndpointReplacementObservationMatches(d database.Resource, observed database.Observation, transition database.PublicEndpointIdentityTransition) error {
	if !observed.Fresh(time.Now(), d.Revision) || observed.Status != "ready" || len(observed.Members) != 1 || observed.Primary != observed.Members[0].Name || !observed.Members[0].Ready || observed.Members[0].Role != "primary" || observed.Members[0].UID == transition.OldMember.UID || observed.TLS == nil || !observed.TLS.Verified || !observed.TLS.PlaintextRejected || observed.TLS.Fingerprint != transition.ProposedLeafFingerprint || observed.TLS.CAFingerprint != transition.ProposedCAFingerprint {
		return fmt.Errorf("Oracle Free public replacement identity is invalid")
	}
	wantedTopology := fmt.Sprintf("%x", sha256.Sum256([]byte(observed.Primary+":"+observed.Members[0].UID)))
	if observed.TopologyFingerprint != wantedTopology {
		return fmt.Errorf("Oracle Free public replacement topology is invalid")
	}
	if transition.FinalMember != nil && (observed.Members[0].Name != transition.FinalMember.Name || observed.Members[0].UID != transition.FinalMember.UID || observed.TopologyFingerprint != transition.FinalTopologyFingerprint || observed.TLS.Fingerprint != transition.ServedLeafFingerprint || observed.TLS.CAFingerprint != transition.ServedCAFingerprint) {
		return fmt.Errorf("Oracle Free public final replacement proof changed")
	}
	return nil
}

func oraclePublicEndpointReplacementResourcesMatch(transition database.PublicEndpointIdentityTransition, set *appsv1.StatefulSet, pod *corev1.Pod, pvcs []database.PublicEndpointTransitionPVC) error {
	if set == nil || string(set.UID) != transition.StatefulSetUID || set.Generation != transition.TargetGeneration || set.Status.ObservedGeneration != transition.TargetGeneration || set.Status.CurrentReplicas != 1 || set.Status.UpdatedReplicas != 1 || set.Status.ReadyReplicas != 1 {
		return fmt.Errorf("Oracle Free public target StatefulSet changed")
	}
	templateHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil || templateHash != transition.TargetTemplateHash {
		return fmt.Errorf("Oracle Free public target template changed")
	}
	if pod == nil || string(pod.UID) == transition.OldMember.UID || transition.FinalMember != nil && string(pod.UID) != transition.FinalMember.UID {
		return fmt.Errorf("Oracle Free public replacement pod changed")
	}
	if !reflect.DeepEqual(pvcs, transition.PVCs) {
		return fmt.Errorf("Oracle Free public persistent identity changed during rollout")
	}
	return nil
}

func (c *Client) oraclePublicEndpointOldProof(ctx context.Context, d database.Resource, transition database.PublicEndpointIdentityTransition, intent string) (*corev1.Secret, error) {
	if !d.Observation.Fresh(time.Now(), d.Revision) || d.Observation.Status != "ready" || d.Observation.TopologyFingerprint != transition.ReviewedTopologyFingerprint || d.Observation.TLS == nil || !d.Observation.TLS.Verified || !d.Observation.TLS.PlaintextRejected || d.Observation.TLS.Fingerprint != transition.ReviewedLeafFingerprint || d.Observation.TLS.CAFingerprint != transition.ReviewedCAFingerprint || len(d.Observation.Members) != 1 || d.Observation.Members[0].Name != transition.OldMember.Name || d.Observation.Members[0].UID != transition.OldMember.UID {
		return nil, fmt.Errorf("Oracle Free reviewed member or served identity changed before issuance")
	}
	_, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("Oracle Free StatefulSet changed before identity issuance")
	}
	pod, _, err := c.databaseExecTarget(ctx, d, d.Observation.Members[0])
	if err != nil {
		return nil, fmt.Errorf("Oracle Free member changed before identity issuance")
	}
	pvcs, err := oraclePublicEndpointPVCIdentities(ctx, c, d, pod)
	if err != nil {
		return nil, fmt.Errorf("Oracle Free persistent identity changed before issuance")
	}
	if err = oraclePublicEndpointOldResourcesMatch(d, transition, set, pod, pvcs); err != nil {
		return nil, err
	}
	oldLeaf, oldCA, secret, err := c.oraclePublicEndpointIssuedIdentity(ctx, d, nil, "")
	if err != nil {
		return nil, err
	}
	if secret.Annotations["hakopod.io/public-endpoint-identity-intent"] == intent {
		leaf, ca, resumed, resumeErr := c.oraclePublicEndpointIssuedIdentity(ctx, d, transition.DesiredNames, intent)
		if resumeErr != nil {
			return nil, resumeErr
		}
		if transition.ProposedLeafFingerprint != "" && (leaf != transition.ProposedLeafFingerprint || ca != transition.ProposedCAFingerprint) {
			return nil, fmt.Errorf("Oracle Free resumed identity differs from its recorded result")
		}
		return resumed, nil
	}
	if oldLeaf != transition.ReviewedLeafFingerprint || oldCA != transition.ReviewedCAFingerprint {
		return nil, fmt.Errorf("Oracle Free issued identity changed before transition")
	}
	return secret, nil
}

// BeginOraclePublicEndpointIdentityTransition captures the complete old proof
// without mutating a Secret, StatefulSet, pod, or volume.
func (c *Client) BeginOraclePublicEndpointIdentityTransition(ctx context.Context, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, desiredNames []string) (database.PublicEndpointIdentityTransition, error) {
	var transition database.PublicEndpointIdentityTransition
	if operation.ID == "" || !oracleFreePublicEndpointSpec(d.Spec) || operation.Revision != endpoint.Revision || operation.Kind != "publish" && operation.Kind != "revoke" && operation.Kind != "cancel" {
		return transition, fmt.Errorf("Oracle Free public identity transition review is invalid")
	}
	names, err := database.NormalizePublicEndpointNames(desiredNames)
	if err != nil || len(names) > database.MaxPublicEndpoints {
		return transition, fmt.Errorf("Oracle Free public identity names are invalid")
	}
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || d.Observation.TLS == nil {
		return transition, fmt.Errorf("Oracle Free public identity no longer matches the reviewed route")
	}
	reviewedTopology, reviewedLeaf := d.Observation.TopologyFingerprint, d.Observation.TLS.Fingerprint
	if operation.Kind == "publish" {
		if operation.Review == nil || operation.Review.DatabaseRevision != d.Revision || operation.Review.RouteFingerprint != route.Fingerprint() || operation.Review.TopologyFingerprint != reviewedTopology || operation.Review.TLSFingerprint != reviewedLeaf {
			return transition, fmt.Errorf("Oracle Free public identity no longer matches the reviewed route")
		}
	}
	member, _, err := c.oraclePublicEndpointTarget(ctx, d, route)
	if err != nil {
		return transition, err
	}
	oldLeaf, oldCA, _, err := c.oraclePublicEndpointIssuedIdentity(ctx, d, nil, "")
	if err != nil || oldLeaf != d.Observation.TLS.Fingerprint || oldCA != d.Observation.TLS.CAFingerprint {
		return transition, fmt.Errorf("Oracle Free issued and served identity changed before transition")
	}
	namespace, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil || set.Generation < 1 || set.Status.ObservedGeneration != set.Generation {
		return transition, fmt.Errorf("Oracle Free public workload is not stable")
	}
	if err = c.oraclePublicEndpointIssuerStable(ctx, d, namespace, oldCA); err != nil {
		return transition, err
	}
	pod, _, err := c.databaseExecTarget(ctx, d, member)
	if err != nil || string(pod.UID) != member.UID {
		return transition, fmt.Errorf("Oracle Free public member identity changed")
	}
	pvcs, err := oraclePublicEndpointPVCIdentities(ctx, c, d, pod)
	if err != nil {
		return transition, err
	}
	templateHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil {
		return transition, err
	}
	transition = database.PublicEndpointIdentityTransition{
		SchemaVersion: database.PublicEndpointIdentityTransitionSchemaVersion, OperationID: operation.ID, Engine: "oracle", Kind: operation.Kind,
		DatabaseRevision: d.Revision, EndpointRevision: endpoint.Revision, RouteFingerprint: route.Fingerprint(), DesiredNames: names,
		ReviewedTopologyFingerprint: reviewedTopology, ReviewedLeafFingerprint: reviewedLeaf,
		ReviewedCAFingerprint: oldCA, StatefulSetUID: string(set.UID), OriginalGeneration: set.Generation,
		OriginalTemplateHash: templateHash, OldMember: database.PublicEndpointTransitionMember{Name: member.Name, UID: member.UID}, PVCs: pvcs,
	}
	return transition, nil
}

// IssueOraclePublicEndpointIdentity issues once for a persisted intent. A
// retry accepts the Secret only when its ownership, intent annotation, CA,
// SANs and leaf fingerprint all agree with that intent.
func (c *Client) IssueOraclePublicEndpointIdentity(ctx context.Context, d database.Resource, transition database.PublicEndpointIdentityTransition, allowIngress bool, before func() error) (database.PublicEndpointIdentityTransition, error) {
	intent, err := oraclePublicEndpointTransitionIntent(transition)
	if err != nil {
		return transition, err
	}
	if _, err = c.oraclePublicEndpointOldProof(ctx, d, transition, intent); err != nil {
		return transition, err
	}
	namespace, _, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil {
		return transition, err
	}
	if err = c.oraclePublicEndpointIssuerStable(ctx, d, namespace, transition.ReviewedCAFingerprint); err != nil {
		return transition, err
	}
	copy := d
	copy.PublicEndpointNames = append([]string(nil), transition.DesiredNames...)
	copy.PublicEndpointAccess = allowIngress && len(transition.DesiredNames) > 0
	if err = c.databaseNetworkPolicy(ctx, copy, before); err != nil {
		return transition, err
	}
	if err = c.prepareDatabaseIdentityForTransition(ctx, copy, intent, transition.ReviewedCAFingerprint, before); err != nil {
		return transition, err
	}
	leaf, ca, secret, err := c.oraclePublicEndpointIssuedIdentity(ctx, d, transition.DesiredNames, intent)
	if err != nil {
		return transition, err
	}
	if err = oraclePublicEndpointIssuedCAStable(ca, transition.ReviewedCAFingerprint); err != nil {
		return transition, err
	}
	_, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil || string(set.UID) != transition.StatefulSetUID {
		return transition, fmt.Errorf("Oracle Free public StatefulSet identity changed")
	}
	currentHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil || currentHash != transition.OriginalTemplateHash || set.Generation != transition.OriginalGeneration {
		return transition, fmt.Errorf("Oracle Free public StatefulSet template changed")
	}
	_, targetHash, err := oraclePublicEndpointDesiredTemplate(set.Spec.Template, secret)
	if err != nil {
		return transition, err
	}
	transition.ProposedLeafFingerprint, transition.ProposedCAFingerprint = leaf, ca
	transition.TargetTemplateHash = targetHash
	transition.TargetGeneration = transition.OriginalGeneration + 1
	return transition, nil
}

func (c *Client) RollOraclePublicEndpointIdentity(ctx context.Context, d database.Resource, transition database.PublicEndpointIdentityTransition, before func() error) error {
	intent, err := oraclePublicEndpointTransitionIntent(transition)
	if err != nil {
		return err
	}
	leaf, ca, secret, err := c.oraclePublicEndpointIssuedIdentity(ctx, d, transition.DesiredNames, intent)
	if err != nil || leaf != transition.ProposedLeafFingerprint || ca != transition.ProposedCAFingerprint || ca != transition.ReviewedCAFingerprint {
		return fmt.Errorf("Oracle Free public issued identity changed before rollout")
	}
	_, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil || string(set.UID) != transition.StatefulSetUID {
		return fmt.Errorf("Oracle Free public StatefulSet identity changed")
	}
	currentHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil {
		return err
	}
	if currentHash == transition.TargetTemplateHash {
		if set.Generation != transition.TargetGeneration {
			return fmt.Errorf("Oracle Free public target generation changed")
		}
		return nil
	}
	if currentHash != transition.OriginalTemplateHash || set.Generation != transition.OriginalGeneration || transition.TargetGeneration != transition.OriginalGeneration+1 {
		return fmt.Errorf("Oracle Free public StatefulSet changed before rollout")
	}
	desired, targetHash, err := oraclePublicEndpointDesiredTemplate(set.Spec.Template, secret)
	if err != nil || targetHash != transition.TargetTemplateHash {
		return fmt.Errorf("Oracle Free public target template changed before rollout")
	}
	set.Spec.Template = desired
	if err = before(); err != nil {
		return err
	}
	updated, err := c.kube.AppsV1().StatefulSets(set.Namespace).Update(ctx, set, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	updatedHash, hashErr := oraclePublicEndpointTemplateHash(updated.Spec.Template)
	if hashErr != nil || string(updated.UID) != transition.StatefulSetUID || updated.Generation != transition.TargetGeneration || updatedHash != transition.TargetTemplateHash {
		return fmt.Errorf("Oracle Free public StatefulSet did not accept the exact target rollout")
	}
	return nil
}

func (c *Client) ConvergeOraclePublicEndpointIdentity(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, observed database.Observation, transition database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error) {
	_, set, err := c.oraclePublicEndpointWorkload(ctx, d)
	if err != nil || string(set.UID) != transition.StatefulSetUID || set.Generation != transition.TargetGeneration {
		return transition, false, fmt.Errorf("Oracle Free public StatefulSet identity or generation changed")
	}
	if set.Status.ObservedGeneration != transition.TargetGeneration || set.Status.CurrentReplicas != 1 || set.Status.UpdatedReplicas != 1 || set.Status.ReadyReplicas != 1 || observed.Status != "ready" {
		return transition, false, nil
	}
	intent, err := oraclePublicEndpointTransitionIntent(transition)
	if err != nil {
		return transition, false, err
	}
	leaf, ca, _, err := c.oraclePublicEndpointIssuedIdentity(ctx, d, transition.DesiredNames, intent)
	if err != nil || leaf != transition.ProposedLeafFingerprint || ca != transition.ProposedCAFingerprint || ca != transition.ReviewedCAFingerprint {
		return transition, false, fmt.Errorf("Oracle Free public issued identity changed during rollout")
	}
	if err = oraclePublicEndpointReplacementObservationMatches(d, observed, transition); err != nil {
		return transition, false, err
	}
	copy := d
	copy.Observation, copy.PublicEndpointNames, copy.PublicEndpointAccess = observed, append([]string(nil), transition.DesiredNames...), len(transition.DesiredNames) > 0
	route, err := database.PublicEndpointRouteFor(copy.Spec, endpoint.Spec.Purpose)
	if err != nil {
		return transition, false, err
	}
	member, _, err := c.oraclePublicEndpointTarget(ctx, copy, route)
	if err != nil {
		return transition, false, err
	}
	pod, _, err := c.databaseExecTarget(ctx, copy, member)
	if err != nil || string(pod.UID) != member.UID {
		return transition, false, fmt.Errorf("Oracle Free public replacement pod changed")
	}
	pvcs, err := oraclePublicEndpointPVCIdentities(ctx, c, copy, pod)
	if err != nil {
		return transition, false, fmt.Errorf("Oracle Free public persistent identity changed during rollout")
	}
	if err = oraclePublicEndpointReplacementResourcesMatch(transition, set, pod, pvcs); err != nil {
		return transition, false, err
	}
	if transition.Kind == "publish" {
		if err = c.verifyOraclePublicEndpointBackend(ctx, copy, endpoint); err != nil {
			return transition, false, err
		}
	} else {
		listenerCheck := []string{"bash", "-c", `awk 'FNR>1 && $4=="0A" && $2 ~ /:(05F1|157C)$/ {bad=1} END {exit bad}' /proc/net/tcp /proc/net/tcp6`}
		if err = c.DatabaseExec(ctx, copy, member, listenerCheck, nil, nil); err != nil {
			return transition, false, fmt.Errorf("Oracle Free plaintext or management listener is exposed")
		}
	}
	transition.FinalMember = &database.PublicEndpointTransitionMember{Name: member.Name, UID: member.UID}
	transition.FinalTopologyFingerprint = observed.TopologyFingerprint
	transition.ServedLeafFingerprint = observed.TLS.Fingerprint
	transition.ServedCAFingerprint = observed.TLS.CAFingerprint
	return transition, true, nil
}
