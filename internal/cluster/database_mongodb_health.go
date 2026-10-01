package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type mongodbReplicaView struct {
	Set     string `bson:"set"`
	MyState int    `bson:"myState"`
	Members []struct {
		Name          string  `bson:"name"`
		Health        float64 `bson:"health"`
		State         int     `bson:"state"`
		Self          bool    `bson:"self"`
		ConfigVersion int64   `bson:"configVersion"`
	} `bson:"members"`
}

func verifyMongoDBReplicaView(view mongodbReplicaView, d database.Resource, member database.Member, members []database.Member) (string, string, error) {
	if view.Set != "database" || len(view.Members) != d.Spec.Members() || (view.MyState != 1 && view.MyState != 2) {
		return "", "", fmt.Errorf("MongoDB replica set has not converged")
	}
	expected := map[string]string{}
	for _, m := range members {
		expected[mongodbMemberHost(d, m)+":27017"] = m.Name
	}
	seen := map[string]bool{}
	primary, role := "", ""
	version := int64(-1)
	for _, m := range view.Members {
		name := expected[m.Name]
		if name == "" || seen[name] || m.Health != 1 || (m.State != 1 && m.State != 2) || m.ConfigVersion < 1 {
			return "", "", fmt.Errorf("MongoDB replica membership does not match its healthy owned pods")
		}
		if version != -1 && version != m.ConfigVersion {
			return "", "", fmt.Errorf("MongoDB members disagree about their replica configuration")
		}
		version, seen[name] = m.ConfigVersion, true
		if m.State == 1 {
			if primary != "" {
				return "", "", fmt.Errorf("MongoDB has multiple write primaries")
			}
			primary = name
		}
		if m.Self {
			if name != member.Name || m.State != view.MyState || role != "" {
				return "", "", fmt.Errorf("MongoDB local member identity changed")
			}
			role = "replica"
			if m.State == 1 {
				role = "primary"
			}
		}
	}
	if primary == "" || role == "" {
		return "", "", fmt.Errorf("MongoDB primary or local replica role is unavailable")
	}
	return primary, role, nil
}

func (c *Client) observeMongoDBDatabase(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation) error {
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	if phase != "Running" {
		return fmt.Errorf("waiting for MongoDB controller reconciliation")
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	members := append([]database.Member(nil), o.Members...)
	primaries := make([]string, len(members))
	for i, member := range members {
		group.Go(func() error {
			client, closeClient, err := c.mongodbClient(step, d, member, true)
			if err != nil {
				return err
			}
			defer closeClient()
			var view mongodbReplicaView
			if err = client.Database("admin").RunCommand(step, bson.D{{Key: "replSetGetStatus", Value: 1}}, options.RunCmd().SetReadPreference(readpref.Nearest())).Decode(&view); err != nil {
				return fmt.Errorf("MongoDB native replication health is unavailable")
			}
			primary, role, err := verifyMongoDBReplicaView(view, d, member, members)
			if err != nil {
				return err
			}
			primaries[i], o.Members[i].Role = primary, role
			var config struct {
				Parsed struct {
					Net struct {
						TLS struct {
							Mode              string `bson:"mode"`
							DisabledProtocols string `bson:"disabledProtocols"`
						} `bson:"tls"`
						MaxIncomingConnections int `bson:"maxIncomingConnections"`
					} `bson:"net"`
				} `bson:"parsed"`
			}
			if err = client.Database("admin").RunCommand(step, bson.D{{Key: "getCmdLineOpts", Value: 1}}, options.RunCmd().SetReadPreference(readpref.Nearest())).Decode(&config); err != nil {
				return fmt.Errorf("MongoDB native transport configuration is unavailable")
			}
			if config.Parsed.Net.TLS.Mode != "requireTLS" || config.Parsed.Net.TLS.DisabledProtocols != "TLS1_0,TLS1_1" || config.Parsed.Net.MaxIncomingConnections != 200 {
				return fmt.Errorf("MongoDB native transport does not match its required policy")
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for _, primary := range primaries {
		if primary == "" || o.Primary != "" && o.Primary != primary {
			return fmt.Errorf("MongoDB members disagree about their primary")
		}
		o.Primary = primary
	}
	identities := make([]string, len(o.Members))
	for i, member := range o.Members {
		identities[i] = member.Name + ":" + member.UID + ":" + member.Role
	}
	slices.Sort(identities)
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "\n"))))
	o.Endpoints = []database.Endpoint{{Purpose: "cluster", Host: "database-svc." + DatabaseNamespace(d.ID) + ".svc.cluster.local", Port: 27017}}
	return nil
}

func (c *Client) verifyMongoDBTLS(ctx context.Context, d database.Resource, o *database.Observation) error {
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, member := range o.Members {
		group.Go(func() error {
			_, closeClient, err := c.mongodbClient(step, d, member, false)
			if err != nil {
				return err
			}
			defer closeClient()
			plain := mongodbDialer{dial: func(dial context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != "database:27017" {
					return nil, fmt.Errorf("unexpected MongoDB plaintext probe target")
				}
				return c.mongodbStream(dial, step, d, member)
			}}
			probe, err := mongo.Connect(options.Client().SetHosts([]string{"database:27017"}).SetDirect(true).SetDialer(plain).SetMaxPoolSize(1).SetConnectTimeout(2 * time.Second).SetServerSelectionTimeout(2 * time.Second).SetRetryReads(false))
			if err != nil {
				return fmt.Errorf("MongoDB plaintext rejection probe could not be configured")
			}
			defer probe.Disconnect(step)
			if probe.Ping(step, readpref.Nearest()) == nil {
				return fmt.Errorf("MongoDB accepted a plaintext client")
			}
			return nil
		})
	}
	return group.Wait()
}
