package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"reflect"
	"strings"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) reconcileMyDuckPublicNames(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MyDuck public endpoint namespace ownership changed")
	}
	set, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || set.UID == "" || set.DeletionTimestamp != nil || set.Labels[databaseOwner] != d.ID || set.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MyDuck public endpoint workload ownership changed")
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareMyDuckSecurity(ctx, d, before); err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	if _, err = database.VerifyServerCertificate(certificate, ca, databaseIdentityNames(d), time.Now()); err != nil {
		return fmt.Errorf("MyDuck public certificate names are pending: %w", err)
	}
	return nil
}

func myduckPublicEndpointTLSConfig(trust database.PublicTrust, host, fingerprint string) (*tls.Config, error) {
	config, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, err
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || fmt.Sprintf("%x", sha256.Sum256(state.PeerCertificates[0].Raw)) != fingerprint {
			return fmt.Errorf("MyDuck has not loaded its current public certificate")
		}
		return nil
	}
	return config, nil
}

func myduckMySQLPublicEndpointClient(ctx context.Context, host, address string, port int, password []byte, tlsConfig *tls.Config) (*sql.DB, error) {
	config := mysqlclient.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = "root", string(password), "tcp", net.JoinHostPort(host, fmt.Sprint(port)), "app"
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 3*time.Second, 3*time.Second, 3*time.Second
	config.AllowNativePasswords = true
	config.DialFunc = func(step context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(step, "tcp", net.JoinHostPort(address, fmt.Sprint(port)))
	}
	config.TLS = tlsConfig
	connector, err := mysqlclient.NewConnector(config)
	if err != nil {
		return nil, err
	}
	client := sql.OpenDB(connector)
	client.SetMaxOpenConns(1)
	client.SetMaxIdleConns(0)
	client.SetConnMaxLifetime(10 * time.Second)
	return client, nil
}

func myduckPostgresPublicEndpointConfig(host, address string, port int, password []byte, tlsConfig *tls.Config) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d user=postgres dbname=app connect_timeout=3", host, port))
	if err != nil {
		return nil, err
	}
	config.Password = string(password)
	config.TLSConfig = tlsConfig
	config.Fallbacks = nil
	config.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(address, fmt.Sprint(port)))
	}
	return config, nil
}

func (c *Client) verifyMyDuckPublicEndpointBackend(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || len(d.Observation.Members) != 1 || (route.Protocol != "mysql" && route.Protocol != "postgresql") {
		return fmt.Errorf("MyDuck public endpoint probe route is unavailable")
	}
	if _, err = database.NormalizePublicEndpointNames([]string{endpoint.Allocation.Host}); err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MyDuck public endpoint namespace ownership changed")
	}
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, d.Observation.Members[0].Name, metav1.GetOptions{})
	if err != nil || pod.UID == "" || string(pod.UID) != d.Observation.Members[0].UID || pod.DeletionTimestamp != nil || !databasePodMatches(*pod, d) {
		return fmt.Errorf("MyDuck public endpoint member ownership changed")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil || !databasePodPolicyMatches(*pod, policy) {
		return fmt.Errorf("MyDuck public endpoint runtime policy changed")
	}
	set, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || !c.databasePodOwned(ctx, *pod, set.UID) || set.Annotations[myduckColdAnnotation] != "" {
		return fmt.Errorf("MyDuck public endpoint workload changed")
	}
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, route.BackendService, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(service, d, ns.UID) || service.Spec.PublishNotReadyAddresses || !reflect.DeepEqual(service.Spec.Selector, map[string]string{databaseOwner: d.ID, managedBy: "hakopod", myduckMemberLabel: "true"}) {
		return fmt.Errorf("MyDuck public endpoint selector changed")
	}
	address, err := databasePublicEndpointBackendServiceAddress(service, d, route)
	if err != nil {
		return err
	}
	trust, certificate, err := c.databaseCertificates(ctx, d)
	if err != nil {
		return err
	}
	_, ca, err := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if err != nil {
		return err
	}
	issued, err := database.VerifyServerCertificate(certificate, ca, []string{endpoint.Allocation.Host}, time.Now())
	if err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || databaseIdentityOwned(secret, d, ns.UID) != nil || len(secret.Data["password"]) < 32 || len(secret.Data["password"]) > 128 || strings.ContainsAny(string(secret.Data["password"]), "\r\n\x00") {
		return fmt.Errorf("MyDuck public endpoint probe credentials are unavailable")
	}
	config, err := myduckPublicEndpointTLSConfig(trust, endpoint.Allocation.Host, issued.Fingerprint)
	if err != nil {
		return err
	}
	step, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if route.Protocol == "mysql" {
		client, err := myduckMySQLPublicEndpointClient(step, endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], config)
		if err != nil {
			return err
		}
		var value int
		err = client.QueryRowContext(step, "SELECT 1").Scan(&value)
		_ = client.Close()
		if err != nil || value != 1 {
			return fmt.Errorf("MyDuck MySQL public hostname or authentication verification failed")
		}
		plain, err := myduckMySQLPublicEndpointClient(step, endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], nil)
		if err == nil {
			err = plain.PingContext(step)
			_ = plain.Close()
		}
		if err == nil {
			return fmt.Errorf("MyDuck MySQL public route did not prove plaintext refusal")
		}
		return nil
	}
	pgConfig, err := myduckPostgresPublicEndpointConfig(endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], config)
	if err != nil {
		return err
	}
	connection, err := pgx.ConnectConfig(step, pgConfig)
	if err != nil {
		return fmt.Errorf("MyDuck PostgreSQL public hostname or authentication verification failed")
	}
	var value int
	err = connection.QueryRow(step, "SELECT 1").Scan(&value)
	_ = connection.Close(step)
	if err != nil || value != 1 {
		return fmt.Errorf("MyDuck PostgreSQL public identity verification failed")
	}
	plainConfig, err := myduckPostgresPublicEndpointConfig(endpoint.Allocation.Host, address, route.BackendPort, secret.Data["password"], nil)
	if err != nil {
		return err
	}
	plain, err := pgx.ConnectConfig(step, plainConfig)
	if err == nil {
		_ = plain.Close(step)
		return fmt.Errorf("MyDuck PostgreSQL public route did not prove plaintext refusal")
	}
	return nil
}
