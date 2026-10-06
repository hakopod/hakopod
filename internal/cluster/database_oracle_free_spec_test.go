package cluster

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func oracleFreeAdmissionDefaults(object *unstructured.Unstructured) {
	_ = unstructured.SetNestedStringMap(object.Object, map[string]string{"secretName": "", "secretKey": "oracle_pwd"}, "spec", "adminPassword")
	unstructured.RemoveNestedField(object.Object, "spec", "dataguard", "prereqs", "enabled")
	endpoint := object.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
	unstructured.RemoveNestedField(endpoint, "tcp", "enabled")
}

func TestOracleFreeSpecMatchesAdmittedDefaults(t *testing.T) {
	wanted, err := oracleFreeObject(oracleFixture(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*unstructured.Unstructured){
		"unchanged": func(*unstructured.Unstructured) {},
		"legacy secret default": func(object *unstructured.Unstructured) {
			_ = unstructured.SetNestedStringMap(object.Object, map[string]string{"secretName": "", "secretKey": "oracle_pwd"}, "spec", "adminPassword")
		},
		"omitted dataguard false": func(object *unstructured.Unstructured) {
			unstructured.RemoveNestedField(object.Object, "spec", "dataguard", "prereqs", "enabled")
		},
		"omitted tcp false": func(object *unstructured.Unstructured) {
			endpoint := object.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
			unstructured.RemoveNestedField(endpoint, "tcp", "enabled")
		},
		"observed controller round trip": oracleFreeAdmissionDefaults,
	} {
		t.Run(name, func(t *testing.T) {
			current := wanted.DeepCopy()
			change(current)
			beforeCurrent, beforeWanted := current.DeepCopy(), wanted.DeepCopy()
			if !oracleFreeSpecMatches(current, wanted) || !oracleFreeSpecMatches(wanted, current) {
				t.Fatal("rejected the observed controller representation")
			}
			if !reflect.DeepEqual(current.Object, beforeCurrent.Object) || !reflect.DeepEqual(wanted.Object, beforeWanted.Object) {
				t.Fatal("comparison changed a resource")
			}
		})
	}
}

func TestOracleFreeSpecMatchesRejectsPolicyChanges(t *testing.T) {
	wanted, err := oracleFreeObject(oracleFixture(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*unstructured.Unstructured){
		"legacy secret name": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "another-secret", "spec", "adminPassword", "secretName")
		},
		"legacy secret key": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "admin", "spec", "adminPassword", "secretKey")
		},
		"legacy extra field": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, false, "spec", "adminPassword", "keepSecret")
		},
		"legacy null": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, nil, "spec", "adminPassword")
		},
		"dataguard enabled": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, true, "spec", "dataguard", "prereqs", "enabled")
		},
		"dataguard wrong type": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "false", "spec", "dataguard", "prereqs", "enabled")
		},
		"dataguard missing policy": func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "spec", "dataguard", "prereqs")
		},
		"plaintext enabled": func(o *unstructured.Unstructured) {
			endpoint := o.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
			_ = unstructured.SetNestedField(endpoint, true, "tcp", "enabled")
		},
		"plaintext wrong type": func(o *unstructured.Unstructured) {
			endpoint := o.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
			_ = unstructured.SetNestedField(endpoint, "false", "tcp", "enabled")
		},
		"plaintext missing policy": func(o *unstructured.Unstructured) {
			endpoint := o.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
			delete(endpoint, "tcp")
		},
		"public endpoint": func(o *unstructured.Unstructured) {
			endpoint := o.Object["spec"].(map[string]any)["services"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
			endpoint["type"] = "LoadBalancer"
		},
		"image": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "untrusted:latest", "spec", "image", "pullFrom")
		},
		"storage": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "another-class", "spec", "persistence", "oradata", "storageClass")
		},
		"unknown false field": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, false, "spec", "unknownPolicy")
		},
		"missing spec": func(o *unstructured.Unstructured) { delete(o.Object, "spec") },
		"pod policy": func(o *unstructured.Unstructured) {
			annotations := o.GetAnnotations()
			annotations[oracleFreePodPolicy] += " "
			o.SetAnnotations(annotations)
		},
	} {
		t.Run(name, func(t *testing.T) {
			current := wanted.DeepCopy()
			oracleFreeAdmissionDefaults(current)
			change(current)
			if oracleFreeSpecMatches(current, wanted) {
				t.Fatal("accepted a policy change")
			}
		})
	}
}
