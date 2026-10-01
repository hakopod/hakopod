package cluster

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// VitessBackupStorage comes only from trusted operator configuration. A
// destination prefix does not reduce an S3 key's actual permissions. The
// resolver must approve credentials dedicated to this database's native data.
type VitessBackupStorage struct {
	DatabaseID            string
	Dedicated             bool
	Destination           backup.Destination
	Credentials           backup.Credentials
	ApprovedEndpointCIDRs []netip.Prefix
}

type VitessBackupResolver func(context.Context, database.Resource) (VitessBackupStorage, error)

// SetVitessBackupResolver configures native recovery before any workers start.
// It is initialization-only and must not run concurrently with client use.
func (c *Client) SetVitessBackupResolver(resolve VitessBackupResolver) error {
	if resolve == nil {
		return fmt.Errorf("Vitess native backup resolver is required")
	}
	if c.options.VitessBackup != nil {
		return fmt.Errorf("Vitess native backup resolver is already configured")
	}
	c.options.VitessBackup = resolve
	return nil
}

func (c *Client) vitessBackupStorage(ctx context.Context, d database.Resource) (VitessBackupStorage, error) {
	if c.options.VitessBackup == nil {
		return VitessBackupStorage{}, fmt.Errorf("Vitess native backup storage is not configured by the operator")
	}
	storage, err := c.options.VitessBackup(ctx, d)
	if err != nil {
		return VitessBackupStorage{}, fmt.Errorf("Vitess native backup destination could not be resolved")
	}
	if err = storage.Validate(d); err != nil {
		return VitessBackupStorage{}, err
	}
	return storage, nil
}

func (s VitessBackupStorage) prefix() string {
	return strings.TrimSuffix(s.Destination.Prefix, "/") + "/vitess-reseed/" + s.DatabaseID
}

func (s VitessBackupStorage) Validate(d database.Resource) error {
	if d.Spec.Vitess == nil || s.DatabaseID != d.ID || !s.Dedicated || s.Destination.ID != d.Spec.Vitess.BackupDestinationID || s.Destination.Revision != d.Spec.Vitess.BackupDestinationRevision || s.Destination.Project != d.Project || s.Destination.Environment != d.Environment {
		return fmt.Errorf("Vitess native backup destination approval or scope changed")
	}
	input := backup.DestinationInput{Name: s.Destination.Name, Endpoint: s.Destination.Endpoint, Region: s.Destination.Region, Bucket: s.Destination.Bucket, Prefix: s.Destination.Prefix, PathStyle: s.Destination.PathStyle, AccessKeyID: s.Credentials.AccessKeyID, SecretAccessKey: s.Credentials.SecretAccessKey, SessionToken: s.Credentials.SessionToken}
	if err := input.Validate(); err != nil {
		return fmt.Errorf("Vitess native recovery requires a valid HTTPS destination")
	}
	if s.Credentials.AccessKeyID == "" || s.Credentials.SecretAccessKey == "" || strings.ContainsAny(s.Credentials.AccessKeyID+s.Credentials.SecretAccessKey+s.Credentials.SessionToken, "\r\n\x00") {
		return fmt.Errorf("Vitess native backup credentials are unavailable or invalid")
	}
	if len(s.prefix()) > 256 || len(s.ApprovedEndpointCIDRs) < 1 || len(s.ApprovedEndpointCIDRs) > 16 {
		return fmt.Errorf("Vitess native backup prefix or endpoint allocation exceeds its bound")
	}
	for _, cidr := range s.ApprovedEndpointCIDRs {
		if !cidr.IsValid() || cidr.Bits() != cidr.Addr().BitLen() || cidr.Addr().Is4In6() || cidr.Addr().IsUnspecified() || cidr.Addr().IsMulticast() || cidr.Addr().IsLoopback() || cidr.Addr().IsLinkLocalUnicast() {
			return fmt.Errorf("Vitess native backup endpoint allocation is unsafe")
		}
	}
	return nil
}

func applyVitessBackupStorage(object *unstructured.Unstructured, d database.Resource, storage VitessBackupStorage) error {
	if err := storage.Validate(d); err != nil {
		return err
	}
	spec := object.Object["spec"].(map[string]any)
	strategies := make([]any, 0, d.Spec.Shards)
	for i, shard := range d.Spec.VitessShardNames() {
		strategies = append(strategies, map[string]any{"name": fmt.Sprintf("shard-%d", i), "keyspace": "app", "shard": shard, "extraFlags": map[string]any{"min_retention_time": "72h", "min_retention_count": "2", "min_backup_interval": "30m"}})
	}
	spec["backup"] = map[string]any{"engine": "builtin", "locations": []any{map[string]any{"s3": map[string]any{"region": storage.Destination.Region, "bucket": storage.Destination.Bucket, "endpoint": storage.Destination.Endpoint, "forcePathStyle": storage.Destination.PathStyle, "keyPrefix": strings.TrimPrefix(storage.prefix(), "/"), "authSecret": vitessSecretSource("database-vitess-native-backup", "credentials"), "minPartSize": int64(5 << 20)}}}, "subcontroller": map[string]any{"serviceAccountName": "database-vitess-operator"}, "schedules": []any{map[string]any{"name": "member-recovery", "schedule": "0 * * * *", "suspend": false, "strategies": strategies, "resources": vitessResources(d.Spec.CPU, d.Spec.Memory), "concurrencyPolicy": "Forbid", "successfulJobsHistoryLimit": int64(0), "failedJobsHistoryLimit": int64(0), "jobTimeoutMinute": int64(60), "startingDeadlineSeconds": int64(300), "allowedMissedRun": int64(2)}}}
	return nil
}

func (c *Client) prepareVitessBackupStorage(ctx context.Context, d database.Resource, storage VitessBackupStorage, before func() error) error {
	if err := storage.Validate(d); err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess native backup namespace ownership changed")
	}
	if err = c.prepareVitessBackupNetwork(ctx, d, ns.UID, storage, before); err != nil {
		return err
	}
	// Only the three AWS credential fields are serialized. The user archive
	// decryption identity remains in the management process.
	credentials := "[default]\naws_access_key_id = " + storage.Credentials.AccessKeyID + "\naws_secret_access_key = " + storage.Credentials.SecretAccessKey + "\n"
	if storage.Credentials.SessionToken != "" {
		credentials += "aws_session_token = " + storage.Credentials.SessionToken + "\n"
	}
	data := map[string][]byte{"credentials": []byte(credentials)}
	api := c.kube.CoreV1().Secrets(ns.Name)
	old, err := api.Get(ctx, "database-vitess-native-backup", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-vitess-native-backup"), Data: data}
		secret.Annotations = map[string]string{"hakopod.io/destination-id": storage.Destination.ID, "hakopod.io/destination-revision": strconv.FormatInt(storage.Destination.Revision, 10)}
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(old, d, ns.UID); err != nil {
		return err
	}
	if old.Annotations["hakopod.io/destination-id"] != storage.Destination.ID || old.Annotations["hakopod.io/destination-revision"] != strconv.FormatInt(storage.Destination.Revision, 10) {
		return fmt.Errorf("Vitess native backup credential revision changed")
	}
	if !reflect.DeepEqual(old.Data, data) {
		return fmt.Errorf("Vitess native backup credentials changed without a reviewed destination revision")
	}
	return nil
}

func (s VitessBackupStorage) endpointPort() (int, error) {
	endpoint, err := url.Parse(s.Destination.Endpoint)
	if err != nil || endpoint.Scheme != "https" {
		return 0, fmt.Errorf("Vitess backup endpoint must use HTTPS")
	}
	if endpoint.Port() == "" {
		return 443, nil
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("Vitess backup endpoint port is invalid")
	}
	return port, nil
}
