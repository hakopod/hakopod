package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"sort"
	"time"
)

type WorkloadSecret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func secretScope(project, environment, application string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(project+"/"+environment+"/"+application)))[:32]
}
func workloadSecretName(project, environment, application, name string) string {
	return "env-" + secretScope(project, environment, application) + "-" + name
}

func (c *Client) ListWorkloadSecrets(ctx context.Context, project, environment, application string) ([]WorkloadSecret, error) {
	result := []WorkloadSecret{}
	if err := c.platformNamespace(ctx, false); err != nil {
		if apierrors.IsNotFound(err) {
			return result, nil
		}
		return nil, err
	}
	list, err := c.kube.CoreV1().Secrets(PlatformNamespace).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod,hakopod.io/secret-scope=" + secretScope(project, environment, application), Limit: 101})
	if err != nil {
		return nil, err
	}
	if list.Continue != "" || len(list.Items) > 100 {
		return nil, fmt.Errorf("application secret limit exceeded")
	}
	for _, s := range list.Items {
		stamp, _ := time.Parse(time.RFC3339, s.Annotations["hakopod.io/updated-at"])
		if stamp.IsZero() {
			stamp = s.CreationTimestamp.Time
		}
		result = append(result, WorkloadSecret{Name: s.Labels["hakopod.io/secret-name"], UpdatedAt: stamp})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (c *Client) PutWorkloadSecret(ctx context.Context, project, environment, application, name, value string) error {
	items, err := c.ListWorkloadSecrets(ctx, project, environment, application)
	if err != nil {
		return err
	}
	exists := false
	for _, i := range items {
		exists = exists || i.Name == name
	}
	if !exists && len(items) >= 100 {
		return fmt.Errorf("at most 100 application secrets")
	}
	return c.PutPlatformSecret(ctx, workloadSecretName(project, environment, application, name), corev1.SecretTypeOpaque, map[string][]byte{"value": []byte(value)}, map[string]string{"hakopod.io/secret-scope": secretScope(project, environment, application), "hakopod.io/secret-name": name})
}
func (c *Client) DeleteWorkloadSecret(ctx context.Context, project, environment, application, name string) error {
	return c.DeletePlatformSecret(ctx, workloadSecretName(project, environment, application, name))
}

func (c *Client) prepareWorkloadSecrets(ctx context.Context, t Target, name string, s spec.Service) error {
	if err := c.prepareDatabaseTrust(ctx, t, name); err != nil {
		return err
	}
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	if len(s.Secrets) == 0 && len(s.Bindings) == 0 {
		current, err := api.Get(ctx, name+"-environment", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = owned(current, t); err != nil {
			return err
		}
		if current.Labels[serviceKey] != name {
			return fmt.Errorf("refusing to clear another service's environment Secret")
		}
		if len(current.Data) == 0 {
			return nil
		}
		if used, err := c.legacyEnvironmentInUse(ctx, t, current); err != nil || used {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		current.Data = map[string][]byte{}
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
		return err
	}
	data, err := c.workloadEnvironmentData(ctx, t, name, s)
	if err != nil {
		return err
	}
	wanted := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-environment", Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name)}, Type: corev1.SecretTypeOpaque, Data: data}
	current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = owned(current, t); err != nil {
		return err
	}
	if current.Labels[serviceKey] != name || current.DeletionTimestamp != nil || current.Type != corev1.SecretTypeOpaque {
		return fmt.Errorf("service environment Secret ownership changed")
	}
	if reflect.DeepEqual(current.Data, wanted.Data) {
		return nil
	}
	// Pre-snapshot ReplicaSets and Jobs still reference this mutable name.
	// Leave their values intact; new templates use the resolved snapshot.
	if used, err := c.legacyEnvironmentInUse(ctx, t, current); err != nil || used {
		return err
	}
	wanted.ResourceVersion = current.ResourceVersion
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Update(ctx, wanted, metav1.UpdateOptions{})
	return err
}

// Resolve values without publishing them through a mutable Secret. The caller
// can pin them together with public trust in one workload template update.
func (c *Client) workloadEnvironmentData(ctx context.Context, t Target, name string, s spec.Service) (map[string][]byte, error) {
	data := map[string][]byte{}
	total := 0
	for key, reference := range s.Secrets {
		if t.secretValues != nil {
			value, found := t.secretValues[name][key]
			if !found {
				return nil, fmt.Errorf("secret snapshot is incomplete")
			}
			data[key] = value
		} else {
			// Internal native-secret helpers can run outside Deploy; external
			// reads require the complete preflight snapshot.
			if reference.Provider != "" {
				return nil, fmt.Errorf("external secret snapshot is unavailable")
			}
			secret, err := c.GetPlatformSecret(ctx, workloadSecretName(t.Project, t.Environment, t.Spec.Name, reference.Ref))
			if err != nil {
				return nil, fmt.Errorf("secret reference %s is unavailable for this application", reference.Ref)
			}
			data[key] = secret.Data["value"]
		}
		total += len(key) + len(data[key])
		if total > 512<<10 {
			return nil, fmt.Errorf("service secret values exceed 512 KiB")
		}
	}
	for key, binding := range s.Bindings {
		if binding.ManagedDatabase != "" || binding.ExternalDatabase != "" {
			value, ok := t.databaseConnections[name][key]
			if !ok {
				return nil, fmt.Errorf("managed database connection snapshot is incomplete")
			}
			data[key] = []byte(value.URL)
			total += len(key) + len(data[key])
			profile := spec.DatabaseClientProfile(s, key)
			if binding.ManagedDatabase != "" && profile != "" && value.CA == "" {
				return nil, fmt.Errorf("managed database client trust is unavailable")
			}
			if binding.ManagedDatabase != "" && value.CA != "" && profile == spec.DatabaseClientInfisicalPostgresV1 {
				if _, exists := data["DB_ROOT_CERT"]; exists {
					return nil, fmt.Errorf("database client profile environment conflicts with DB_ROOT_CERT")
				}
				data["DB_ROOT_CERT"] = []byte(base64.StdEncoding.EncodeToString([]byte(value.CA)))
				total += len("DB_ROOT_CERT") + len(data["DB_ROOT_CERT"])
			}
			continue
		}
		var password []byte
		if binding.Password != nil {
			var ok bool
			password, ok = t.secretValues[name][spec.BindingSecretKey(key)]
			if !ok {
				return nil, fmt.Errorf("binding secret snapshot is incomplete")
			}
		}
		data[key] = []byte(spec.BindingURL(binding, t.Spec.Services[binding.Service], password))
		total += len(key) + len(data[key])
	}
	if total > 512<<10 {
		return nil, fmt.Errorf("service secret values exceed 512 KiB")
	}
	return data, nil
}

func (c *Client) legacyEnvironmentInUse(ctx context.Context, t Target, secret *corev1.Secret) (bool, error) {
	err := c.checkRetiredSecretConsumers(ctx, Namespace(t.ApplicationID), nil, map[string]corev1.Secret{secret.Name: *secret})
	if errors.Is(err, errRetiredSecretReferenced) || errors.Is(err, errRetiredAccountSecretReferenced) || errors.Is(err, errRetiredConsumerTerminating) {
		return true, nil
	}
	return false, err
}
