package cluster

import (
	"context"
	_ "embed"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//go:embed database_oracle_enterprise_tcps.sh
var oracleEnterpriseTCPSScript string

//go:embed database_oracle_wallet.py
var oracleEnterpriseWalletScript string

func (c *Client) prepareOracleEnterpriseBootstrap(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	desired := &corev1.ConfigMap{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-oracle-bootstrap"), Immutable: ptr(true), Data: map[string]string{"config-tcps.sh": oracleEnterpriseTCPSScript, "wallet.py": oracleEnterpriseWalletScript}}
	api := c.kube.CoreV1().ConfigMaps(ns.Name)
	current, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil || !mongodbSupportOwned(current, d, ns.UID) || current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, desired.Data) {
		return fmt.Errorf("Oracle bootstrap policy identity changed")
	}
	return nil
}
