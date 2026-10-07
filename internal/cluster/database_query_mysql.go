package cluster

import (
	"context"
	"database/sql"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	"io"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"net"
	"time"
)

func (c *Client) mysqlQueryClient(ctx context.Context, d database.Resource, m database.Member) (*sql.DB, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return nil, queryUnavailable()
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || !postgresQueryCredentialOwned(secret, d, ns) {
		return nil, queryUnavailable()
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return nil, queryUnavailable()
	}
	host := "database." + ns.Name + ".svc"
	identity, err := redisTLSConfig(trust, host)
	if err != nil {
		return nil, queryUnavailable()
	}
	settings := mysql.NewConfig()
	settings.User = "app"
	settings.Passwd = string(secret.Data["password"])
	settings.DBName = "app"
	settings.Net = "tcp"
	settings.Addr = net.JoinHostPort(host, "3306")
	settings.TLS = identity
	settings.Timeout = 5 * time.Second
	settings.ReadTimeout = 20 * time.Second
	settings.WriteTimeout = 5 * time.Second
	settings.MaxAllowedPacket = database.QueryMaxBytes
	settings.Logger = log.New(io.Discard, "", 0)
	settings.DialFunc = func(step context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != settings.Addr {
			return nil, queryUnavailable()
		}
		// The driver cancels its dial context after the handshake. The relay
		// must live for the bounded query context, then close with the connection.
		if err := step.Err(); err != nil {
			return nil, err
		}
		return c.queryOwnedRelay(ctx, d, m, 3306)
	}
	connector, err := mysql.NewConnector(settings)
	if err != nil {
		return nil, queryUnavailable()
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}
