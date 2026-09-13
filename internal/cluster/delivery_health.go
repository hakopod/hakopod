package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Ready pods are not enough when a declared listener, certificate or identity
// disappears. These bounded reads never probe external SMTP/AWS or exec pods.
func (c *Client) serviceDeliveryHealthy(ctx context.Context, t Target, name string, current *appsv1.Deployment) (bool, string, error) {
	svc := t.Spec.Services[name]
	notes := []string{}
	if len(svc.PublicTCP) > 0 {
		listeners, err := c.ObservePublicTCP(ctx, t, name)
		if err != nil {
			return false, "", fmt.Errorf("observe public TCP configuration: %w", err)
		}
		if len(listeners) != len(svc.PublicTCP) {
			return false, "Public TCP listener configuration is missing.", nil
		}
		for _, listener := range listeners {
			if listener.Status != "configured" {
				return false, fmt.Sprintf("Public TCP listener %d: %s", listener.Port, listener.Message), nil
			}
		}
		notes = append(notes, "Public TCP is configured; external reachability and STARTTLS remain unverified.")
	}
	for _, mount := range svc.CertificateMounts {
		secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, mount.Certificate, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "A mounted backend certificate is missing; upload and deploy a replacement.", nil
		}
		if err != nil {
			return false, "", fmt.Errorf("observe backend certificate: %w", err)
		}
		if err := checkBackendCertificate(secret, t, name, mount.Hostname); err != nil {
			return false, "Mounted backend certificate is unavailable: " + err.Error(), nil
		}
	}
	if len(svc.CertificateMounts) > 0 && !backendMountsPrepared(current.Spec.Template.Spec, svc) {
		return false, "Backend certificate mounts differ from this revision.", nil
	}
	if svc.AWSIdentity != "" {
		identity, err := c.AWSIdentityStatus(ctx, t, name)
		if err != nil {
			return false, "", fmt.Errorf("observe AWS workload identity: %w", err)
		}
		if identity == nil {
			return false, "AWS workload identity is unavailable.", nil
		}
		if identity.Status != "prepared" {
			return false, identity.Message, nil
		}
		if !awsIdentityPrepared(current.Spec.Template.Spec, identity) {
			return false, "AWS workload token or credential settings differ from the approved identity.", nil
		}
		notes = append(notes, identity.Message)
	}
	return true, strings.Join(notes, " "), nil
}

func backendMountsPrepared(pod corev1.PodSpec, svc spec.Service) bool {
	if len(pod.Containers) == 0 {
		return false
	}
	expected := corev1.PodSpec{Containers: []corev1.Container{{}}}
	applyBackendCertificateMounts(svc, &expected)
	for _, wanted := range expected.Volumes {
		found := false
		for _, volume := range pod.Volumes {
			found = found || reflect.DeepEqual(volume, wanted)
		}
		if !found {
			return false
		}
	}
	for _, wanted := range expected.Containers[0].VolumeMounts {
		found := false
		for _, mount := range pod.Containers[0].VolumeMounts {
			found = found || reflect.DeepEqual(mount, wanted)
		}
		if !found {
			return false
		}
	}
	return true
}

func awsIdentityPrepared(pod corev1.PodSpec, identity *AWSIdentityState) bool {
	if len(pod.Containers) == 0 || pod.ServiceAccountName != identity.ServiceAccount || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		return false
	}
	volumeReady := false
	for _, volume := range pod.Volumes {
		p := volume.Projected
		if volume.Name != "hakopod-aws-identity" || p == nil || p.DefaultMode == nil || *p.DefaultMode != 0440 || len(p.Sources) != 1 {
			continue
		}
		token := p.Sources[0].ServiceAccountToken
		volumeReady = token != nil && token.Audience == awsIdentityAudience && token.Path == "token" && token.ExpirationSeconds != nil && *token.ExpirationSeconds == 3600
	}
	mountReady := false
	for _, mount := range pod.Containers[0].VolumeMounts {
		mountReady = mountReady || mount.Name == "hakopod-aws-identity" && mount.MountPath == spec.AWSIdentityTokenDirectory && mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == ""
	}
	if !volumeReady || !mountReady {
		return false
	}
	wanted := map[string]string{"AWS_ROLE_ARN": identity.RoleARN, "AWS_REGION": identity.Region, "AWS_DEFAULT_REGION": identity.Region, "AWS_STS_REGIONAL_ENDPOINTS": "regional", "AWS_WEB_IDENTITY_TOKEN_FILE": spec.AWSIdentityTokenDirectory + "/token", "AWS_EC2_METADATA_DISABLED": "true", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null"}
	for key, value := range wanted {
		found := false
		for _, env := range pod.Containers[0].Env {
			if env.Name == key {
				if found || env.ValueFrom != nil || env.Value != value {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
