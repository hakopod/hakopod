package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const automaticCertificateKey = "hakopod.io/automatic-certificate"

func automaticCertificateName(service, hostname string, certificate []byte) string {
	hash := sha256.New()
	hash.Write([]byte(service + "\x00" + hostname + "\x00"))
	hash.Write(certificate)
	return fmt.Sprintf("hp-auto-cert-%x", hash.Sum(nil)[:20])
}

// Resolution is read-only unless explicitly applying a validated snapshot.
func (c *Client) resolveBackendCertificates(ctx context.Context, t Target, service string, svc spec.Service, persist bool) (spec.Service, error) {
	result := svc
	result.CertificateMounts = append([]spec.CertificateMount(nil), svc.CertificateMounts...)
	sources := make(map[int]*corev1.Secret)
	for i, mount := range result.CertificateMounts {
		if t.ApplicationID == "" {
			return svc, fmt.Errorf("certificate references require an existing application")
		}
		if mount.Source == "ingress" {
			mode, err := ParseDeploymentMode(c.options.DeploymentMode)
			if err != nil || mode != DeploymentSelfHosted {
				return svc, fmt.Errorf("automatic backend certificates are available only on self-hosted installations")
			}
			covered := false
			hosts, err := c.serviceHostnames(ctx, t, service)
			if err != nil {
				return svc, err
			}
			for _, host := range hosts {
				covered = covered || host == mount.Hostname
			}
			if !covered || (svc.TLS == nil && c.options.TLSIssuer == "") {
				return svc, fmt.Errorf("automatic certificate hostname must remain on this service's configured TLS ingress")
			}
			secret, err := c.backendIngressSource(ctx, t, service, mount.Hostname)
			if err != nil {
				return svc, err
			}
			result.CertificateMounts[i].Certificate = automaticCertificateName(service, mount.Hostname, secret.Data[corev1.TLSCertKey])
			sources[i] = secret
			continue
		}
		secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, mount.Certificate, metav1.GetOptions{})
		if err != nil {
			return svc, fmt.Errorf("backend certificate is unavailable; upload a certificate for this service first")
		}
		if err = checkBackendCertificate(secret, t, service, mount.Hostname); err != nil {
			return svc, err
		}
	}
	if persist && len(sources) > 0 {
		keep := map[string]bool{}
		for _, mount := range result.CertificateMounts {
			keep[mount.Certificate] = true
		}
		for i, mount := range result.CertificateMounts {
			if source := sources[i]; source != nil {
				if err := c.ensureAutomaticCertificate(ctx, t, service, mount.Hostname, source, keep); err != nil {
					return svc, err
				}
			}
		}
	}
	return result, nil
}

func (c *Client) ensureAutomaticCertificate(ctx context.Context, t Target, service, hostname string, source *corev1.Secret, keep map[string]bool) error {
	name := automaticCertificateName(service, hostname, source.Data[corev1.TLSCertKey])
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if current.Labels[automaticCertificateKey] != "true" || automaticCertificateName(service, hostname, current.Data[corev1.TLSCertKey]) != name {
			return fmt.Errorf("automatic certificate ownership mismatch")
		}
		return checkBackendCertificate(current, t, service, hostname)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("automatic certificate storage is unavailable")
	}
	// Keep old versions while any current or historical workload refers to them.
	if err = c.pruneAutomaticCertificates(ctx, t, service, keep); err != nil {
		return err
	}
	labels := labelsFor(t, service)
	labels[backendCertificateKey], labels[automaticCertificateKey] = "true", "true"
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labels, Annotations: map[string]string{backendCertificateHostname: hostname, backendCertificateSource: "ingress-auto"}}, Immutable: ptr(true), Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: source.Data[corev1.TLSCertKey], corev1.TLSPrivateKeyKey: source.Data[corev1.TLSPrivateKeyKey]}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, err = api.Get(ctx, name, metav1.GetOptions{})
		if err == nil && current.Labels[automaticCertificateKey] == "true" && automaticCertificateName(service, hostname, current.Data[corev1.TLSCertKey]) == name {
			return checkBackendCertificate(current, t, service, hostname)
		}
		return fmt.Errorf("automatic certificate changed concurrently; retry renewal")
	}
	return err
}

// API list limits are hard safety bounds: never delete after a partial list.
func (c *Client) pruneAutomaticCertificates(ctx context.Context, t Target, service string, keep map[string]bool) error {
	ns := Namespace(t.ApplicationID)
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: backendCertificateSelector(t, service) + "," + automaticCertificateKey + "=true", Limit: 64})
	if err != nil {
		return err
	}
	if secrets.Continue != "" {
		return fmt.Errorf("automatic certificate history exceeds the cleanup limit")
	}
	if len(secrets.Items) == 0 {
		return nil
	}
	used := map[string]bool{}
	remember := func(pod corev1.PodSpec) {
		for _, v := range pod.Volumes {
			if v.Secret != nil {
				used[v.Secret.SecretName] = true
			}
		}
	}
	opts := metav1.ListOptions{Limit: 256}
	// Inspect the whole application namespace, including terminating pods.
	deployments, err := c.kube.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if deployments.Continue != "" {
		return fmt.Errorf("deployment list exceeds certificate cleanup limit")
	}
	for _, d := range deployments.Items {
		remember(d.Spec.Template.Spec)
	}
	replicas, err := c.kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if replicas.Continue != "" {
		return fmt.Errorf("replica set list exceeds certificate cleanup limit")
	}
	for _, r := range replicas.Items {
		remember(r.Spec.Template.Spec)
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if pods.Continue != "" {
		return fmt.Errorf("pod list exceeds certificate cleanup limit")
	}
	for _, p := range pods.Items {
		remember(p.Spec)
	}
	retained := 0
	for _, secret := range secrets.Items {
		if used[secret.Name] || keep[secret.Name] {
			retained++
			continue
		}
		if owned(&secret, t) != nil || secret.Labels[serviceKey] != service || secret.Labels[automaticCertificateKey] != "true" {
			return fmt.Errorf("automatic certificate cleanup ownership mismatch")
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		uid, version := secret.UID, secret.ResourceVersion
		err = c.kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if retained >= 64 {
		return fmt.Errorf("automatic certificate history is full; retire unused workloads before renewing")
	}
	return nil
}

// RenewBackendCertificates rolls only certificate volumes, keeping the accepted
// application revision, images, replicas and other controller fields intact.
// The caller must hold the same per-application claim used by deployments.
func (c *Client) RenewBackendCertificates(ctx context.Context, t Target, emit func(Event), services ...string) error {
	if !spec.HasAutomaticCertificates(t.Spec) {
		return nil
	}
	mode, err := ParseDeploymentMode(c.options.DeploymentMode)
	if err != nil || mode != DeploymentSelfHosted {
		return fmt.Errorf("automatic backend certificates are available only on self-hosted installations")
	}
	names := spec.Names(t.Spec)
	if len(services) > 0 {
		names = services
	}
	for _, name := range names {
		svc := t.Spec.Services[name]
		automatic := false
		for _, m := range svc.CertificateMounts {
			automatic = automatic || m.Source == "ingress"
		}
		if !automatic {
			continue
		}
		api := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID))
		current, err := api.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = owned(current, t); err != nil {
			return err
		}
		if current.Labels[serviceKey] != name || current.Annotations["hakopod.io/revision"] != strconv.FormatInt(t.Revision, 10) {
			return fmt.Errorf("certificate renewal deferred until the accepted deployment is current")
		}
		resolved, err := c.resolveBackendCertificates(ctx, t, name, svc, false)
		if err != nil {
			return err
		}
		if backendMountsPrepared(current.Spec.Template.Spec, resolved) {
			continue
		}
		// Refuse to repair unrelated pod-template drift through the renewal path.
		expected := svc
		expected.CertificateMounts = append([]spec.CertificateMount(nil), svc.CertificateMounts...)
		for i, m := range expected.CertificateMounts {
			if m.Source != "ingress" {
				continue
			}
			found := false
			for _, v := range current.Spec.Template.Spec.Volumes {
				if v.Name == fmt.Sprintf("backend-certificate-%d", i) && v.Secret != nil {
					secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, v.Secret.SecretName, metav1.GetOptions{})
					if err != nil || owned(secret, t) != nil || secret.Labels[serviceKey] != name || secret.Labels[automaticCertificateKey] != "true" || secret.Annotations[backendCertificateHostname] != m.Hostname {
						return fmt.Errorf("current automatic certificate ownership mismatch")
					}
					expected.CertificateMounts[i].Certificate, found = secret.Name, true
				}
			}
			if !found {
				return fmt.Errorf("certificate mount is missing; redeploy the accepted revision")
			}
		}
		if !backendMountsPrepared(current.Spec.Template.Spec, expected) {
			return fmt.Errorf("certificate mount permissions changed; redeploy the accepted revision")
		}
		resolved, err = c.resolveBackendCertificates(ctx, t, name, svc, true)
		if err != nil {
			return err
		}
		for i, m := range resolved.CertificateMounts {
			if m.Source != "ingress" {
				continue
			}
			for v := range current.Spec.Template.Spec.Volumes {
				if current.Spec.Template.Spec.Volumes[v].Name == fmt.Sprintf("backend-certificate-%d", i) {
					current.Spec.Template.Spec.Volumes[v].Secret.SecretName = m.Certificate
				}
			}
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if emit != nil {
			emit(Event{Type: "certificate_renewed", Service: name, Message: "Validated ingress certificate renewed; service restart requested using the configured update strategy."})
		}
	}
	return nil
}
