package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func vitessControlCommand(d database.Resource, args ...string) []string {
	base := []string{"vtctldclient", "--server=127.0.0.1:15999", "--action-timeout=20s", "--vtctld-grpc-ca=" + vitessTLSPath + "/ca.crt", "--vtctld-grpc-server-name=database-internal." + DatabaseNamespace(d.ID) + ".svc.cluster.local"}
	return append(base, args...)
}

func (c *Client) vitessControlMember(ctx context.Context, d database.Resource) (database.Member, error) {
	expectedIdentity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return database.Member{}, err
	}
	return c.vitessControlMemberWithIdentity(ctx, d, expectedIdentity)
}

func (c *Client) vitessControlMemberWithIdentity(ctx context.Context, d database.Resource, expectedIdentity string) (database.Member, error) {
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID {
		return database.Member{}, fmt.Errorf("Vitess routing controller ownership changed")
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=control", Limit: 3, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) > 2 {
		return database.Member{}, fmt.Errorf("Vitess control inventory exceeds its bound")
	}
	var member database.Member
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || !vitessPodIdentityMatches(pod, expectedIdentity) {
			continue
		}
		if !c.vitessPodOwned(ctx, pod, object.GetUID()) || !vitessPodMatches(pod, d) {
			return database.Member{}, fmt.Errorf("Vitess control identity changed")
		}
		if !vitessPodReadyWithIdentity(pod, expectedIdentity) {
			continue
		}
		if member.Name != "" {
			return database.Member{}, fmt.Errorf("Vitess control identity changed")
		}
		member = database.Member{Name: pod.Name, UID: string(pod.UID), Role: "control", Ready: true}
	}
	if member.Name == "" {
		return member, fmt.Errorf("waiting for Vitess control readiness")
	}
	return member, nil
}

// Native protobuf JSON includes default values and may use camelCase. Remove
// defaults while retaining every meaningful field, including unknown routing
// settings. A drifted vindex must not pass because expected fields still exist.
func vitessCanonicalSchema(raw []byte) (any, error) {
	if len(raw) > 128<<10 {
		return nil, fmt.Errorf("Vitess routing schema exceeds its bound")
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("Vitess routing schema is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("Vitess routing schema contains trailing data")
	}
	var normalize func(any) (any, error)
	normalize = func(value any) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, item := range v {
				canonicalKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				// Vitess emits this enum's zero value even when routing does
				// not specify it. Other foreign-key modes remain meaningful.
				if canonicalKey == "foreignkeymode" && item == "unspecified" {
					continue
				}
				clean, err := normalize(item)
				if err != nil {
					return nil, err
				}
				if clean == nil {
					continue
				}
				key = canonicalKey
				if _, exists := out[key]; exists {
					return nil, fmt.Errorf("Vitess routing schema has ambiguous fields")
				}
				out[key] = clean
			}
			if len(out) == 0 {
				return nil, nil
			}
			return out, nil
		case []any:
			if len(v) == 0 {
				return nil, nil
			}
			out := make([]any, len(v))
			for i, item := range v {
				clean, err := normalize(item)
				if err != nil {
					return nil, err
				}
				out[i] = clean
			}
			return out, nil
		case nil:
			return nil, nil
		case bool:
			if !v {
				return nil, nil
			}
		case string:
			if v == "" {
				return nil, nil
			}
		case json.Number:
			if v == "0" {
				return nil, nil
			}
		}
		return value, nil
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("Vitess routing schema must be an object")
	}
	return normalize(value)
}

func vitessSchemaMatches(want, actual []byte) bool {
	a, err := vitessCanonicalSchema(want)
	if err != nil {
		return false
	}
	b, err := vitessCanonicalSchema(actual)
	return err == nil && reflect.DeepEqual(a, b)
}

func (c *Client) readVitessVSchema(ctx context.Context, d database.Resource, member database.Member) ([]byte, error) {
	out := &databaseBoundedWriter{limit: 128 << 10}
	if err := c.DatabaseExec(ctx, d, member, vitessControlCommand(d, "GetVSchema", "app"), nil, out); err != nil {
		return nil, fmt.Errorf("Vitess routing schema could not be read")
	}
	return out.Bytes(), nil
}

func (c *Client) reconcileVitessRouting(ctx context.Context, d database.Resource, before func() error) error {
	expectedIdentity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	return c.reconcileVitessRoutingWithIdentity(ctx, d, expectedIdentity, before)
}

func (c *Client) reconcileVitessRoutingWithIdentity(ctx context.Context, d database.Resource, expectedIdentity string, before func() error) error {
	want, err := d.Spec.VitessVSchema()
	if err != nil {
		return err
	}
	member, err := c.vitessControlMemberWithIdentity(ctx, d, expectedIdentity)
	if err != nil {
		return err
	}
	actual, err := c.readVitessVSchema(ctx, d, member)
	if err == nil && vitessSchemaMatches(want, actual) {
		return nil
	}
	if err = before(); err != nil {
		return err
	}
	// Apply only the immutable, validated schema mounted by Hakopod. Never
	// execute user-supplied SQL or accept unbounded native CLI arguments.
	if err = c.DatabaseExec(ctx, d, member, vitessControlCommand(d, "ApplyVSchema", "--strict", "--vschema-file="+vitessConfigPath+"/vschema.json", "app"), nil, io.Discard); err != nil {
		return fmt.Errorf("Vitess routing schema could not be applied")
	}
	actual, err = c.readVitessVSchema(ctx, d, member)
	if err != nil || !vitessSchemaMatches(want, actual) {
		return fmt.Errorf("Vitess routing schema has not converged")
	}
	return nil
}

func (c *Client) verifyVitessRouting(ctx context.Context, d database.Resource) error {
	expectedIdentity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	return c.verifyVitessRoutingWithIdentity(ctx, d, expectedIdentity)
}

func (c *Client) verifyVitessRoutingWithIdentity(ctx context.Context, d database.Resource, expectedIdentity string) error {
	want, err := d.Spec.VitessVSchema()
	if err != nil {
		return err
	}
	member, err := c.vitessControlMemberWithIdentity(ctx, d, expectedIdentity)
	if err != nil {
		return err
	}
	actual, err := c.readVitessVSchema(ctx, d, member)
	if err != nil || !vitessSchemaMatches(want, actual) {
		return fmt.Errorf("Vitess native routing differs from its reviewed schema")
	}
	return nil
}
