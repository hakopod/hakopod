package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Each tablet advertises an owned Service name. Pod IPs cannot provide the
// stable hostname that MySQL checks during replication certificate validation.
func (c *Client) reconcileVitessServices(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess service namespace ownership changed")
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID {
		return fmt.Errorf("Vitess service controller ownership changed")
	}
	port := func(name string, value int32) corev1.ServicePort {
		return corev1.ServicePort{Name: name, Protocol: corev1.ProtocolTCP, Port: value, TargetPort: intstr.FromInt32(value)}
	}
	gateway := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database"), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{databaseOwner: d.ID, vitessComponentLabel: "gateway"}, Ports: []corev1.ServicePort{port("mysql", 3306)}}}
	if err = c.applyVitessService(ctx, d, ns.UID, gateway, before); err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=tablet", Limit: database.MaxMembers + 1, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) > database.MaxMembers {
		return fmt.Errorf("Vitess tablet service inventory exceeds its bound")
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		uid := pod.Labels["planetscale.com/tablet-uid"]
		if _, err := strconv.ParseUint(uid, 10, 32); err != nil || uid == "" || !c.vitessPodOwned(ctx, pod, object.GetUID()) || !vitessPodMatches(pod, d) {
			return fmt.Errorf("Vitess tablet service target changed")
		}
		service := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, pod.Name), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, PublishNotReadyAddresses: true, Selector: map[string]string{"planetscale.com/cluster": "database", "planetscale.com/tablet-uid": uid}, Ports: []corev1.ServicePort{port("mysql", 3306), port("grpc", 15999)}}}
		if err = c.applyVitessService(ctx, d, ns.UID, service, before); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) applyVitessService(ctx context.Context, d database.Resource, namespaceUID types.UID, want *corev1.Service, before func() error) error {
	api := c.kube.CoreV1().Services(want.Namespace)
	old, err := api.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if old.UID == "" || old.DeletionTimestamp != nil || old.Labels[databaseOwner] != d.ID || old.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess service ownership changed")
	}
	owned := false
	for _, owner := range old.OwnerReferences {
		if owner.UID == namespaceUID && owner.Kind == "Namespace" && owner.Name == want.Namespace {
			owned = true
		}
	}
	if !owned || old.Spec.Type != corev1.ServiceTypeClusterIP || old.Spec.ExternalName != "" || len(old.Spec.ExternalIPs) != 0 {
		return fmt.Errorf("Vitess service ownership or exposure changed")
	}
	updated := old.DeepCopy()
	updated.Spec.Selector = want.Spec.Selector
	updated.Spec.Ports = want.Spec.Ports
	updated.Spec.PublishNotReadyAddresses = want.Spec.PublishNotReadyAddresses
	if reflect.DeepEqual(old.Spec, updated.Spec) {
		return nil
	}
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}
