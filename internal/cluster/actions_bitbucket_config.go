package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const bitbucketActionsProviderLabel = "hakopod.io/actions-provider"
const bitbucketActionsSlotLabel = "hakopod.io/actions-slot"
const bitbucketActionsBindingAnnotation = "hakopod.io/actions-registration"

var bitbucketActionsID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var bitbucketActionsService = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// BitbucketActionsRuntime is installation-owned runtime selection. Building a
// candidate does not qualify its native protocol or enable provider admission.
// Bitbucket reuses one repository's sandbox; it has no documented run-once mode.
type BitbucketActionsRuntime struct {
	ManagerImage string
	Architecture string
}

func ValidateBitbucketActionsRuntime(runtime BitbucketActionsRuntime) error {
	if runtime.Architecture != "amd64" && runtime.Architecture != "arm64" {
		return errors.New("Bitbucket requires an explicit native Linux architecture")
	}
	ref, err := parseReference(runtime.ManagerImage)
	if runtime.ManagerImage == "" || err != nil || ValidateReadinessProbeImage(runtime.ManagerImage) != nil || !strings.HasPrefix(ref.reference, "sha256:") {
		return errors.New("Bitbucket requires an installation-selected digest-pinned manager image")
	}
	return nil
}

func validateBitbucketActions(t Target, service, id string, s spec.Service, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) error {
	if !bitbucketActionsID.MatchString(t.ApplicationID) || !bitbucketActionsID.MatchString(id) || !bitbucketActionsService.MatchString(service) || t.Revision <= 0 || s.Actions == nil {
		return errors.New("Bitbucket runner slot identity is invalid")
	}
	if current, ok := t.Spec.Services[service]; !ok || !reflect.DeepEqual(current, s) {
		return errors.New("Bitbucket runner service does not match its original application revision")
	}
	if err := registration.Validate(s.Actions.ProviderTarget(), "hakopod-"+id); err != nil {
		return err
	}
	if s.Image != runtime.ManagerImage {
		return errors.New("Bitbucket manager image does not match its immutable service revision")
	}
	if s.Architecture != runtime.Architecture || spec.ActionsWorkspaceGiB(s.Actions) < 4 || spec.ActionsWorkspaceGiB(s.Actions) > 16 {
		return errors.New("Bitbucket requires matching architecture and 4–16 GiB of temporary storage")
	}
	if s.Actions.Cache != nil {
		return errors.New("Bitbucket uses its native repository cache; GitLab storage credentials do not apply")
	}
	profile := spec.EffectiveResources(s)
	for _, bound := range []struct{ value, minimum, maximum string }{
		{profile.CPURequest, "1", "64"}, {profile.CPULimit, "1", "64"},
		{profile.MemoryRequest, "8Gi", "256Gi"}, {profile.MemoryLimit, "8Gi", "256Gi"},
	} {
		value, err := resource.ParseQuantity(bound.value)
		if err != nil || value.Cmp(resource.MustParse(bound.minimum)) < 0 || value.Cmp(resource.MustParse(bound.maximum)) > 0 {
			return errors.New("Bitbucket requires at least one CPU and 8 GiB of memory including its private Docker daemon")
		}
	}
	requestCPU, requestMemory := resource.MustParse(profile.CPURequest), resource.MustParse(profile.MemoryRequest)
	if requestCPU.Cmp(resource.MustParse(profile.CPULimit)) > 0 || requestMemory.Cmp(resource.MustParse(profile.MemoryLimit)) > 0 {
		return errors.New("Bitbucket resource requests must fit within their limits")
	}
	return ValidateBitbucketActionsRuntime(runtime)
}

func bitbucketActionsMetadata(t Target, service, id string, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) metav1.ObjectMeta {
	s := t.Spec.Services[service]
	encoded, _ := json.Marshal(struct {
		ApplicationID, Project, Environment, Service, SlotID, RunnerID string
		Revision                                                       int64
		Target                                                         actions.BitbucketTarget
		Resources                                                      spec.Profile
		WorkspaceGiB                                                   int64
		Runtime                                                        BitbucketActionsRuntime
	}{t.ApplicationID, t.Project, t.Environment, service, id, registration.RunnerID, t.Revision, registration.Target, spec.EffectiveResources(s), spec.ActionsWorkspaceGiB(s.Actions), runtime})
	digest := sha256.Sum256(encoded)
	labels := labelsFor(t, service)
	labels[bitbucketActionsProviderLabel], labels[bitbucketActionsSlotLabel] = "bitbucket", id
	return metav1.ObjectMeta{Name: "actions-" + id, Namespace: Namespace(t.ApplicationID), Labels: labels, Annotations: map[string]string{
		bitbucketActionsBindingAnnotation: hex.EncodeToString(digest[:]), "hakopod.io/actions-runner-id": registration.RunnerID, "hakopod.io/actions-lifecycle": "dedicated",
	}}
}

func bitbucketActionsSecret(t Target, service, id string, s spec.Service, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) (*corev1.Secret, error) {
	if err := validateBitbucketActions(t, service, id, s, registration, runtime); err != nil {
		return nil, err
	}
	return &corev1.Secret{ObjectMeta: bitbucketActionsMetadata(t, service, id, registration, runtime), Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
		"oauth_client_id": []byte(registration.OAuthClientID), "oauth_client_secret": []byte(registration.OAuthSecret),
	}}, nil
}
