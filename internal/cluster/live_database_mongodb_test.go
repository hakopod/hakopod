package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newMongoDBFixture(t *testing.T, ctx context.Context, c *Client, mode string) (database.Resource, []byte) {
	t.Helper()
	id, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d := mongodbUnitFixture()
	d.ID, d.Project, d.Environment, d.Spec.Mode, d.Spec.StorageGiB = hex.EncodeToString(id), "demo", "development", mode, 1
	d.Spec.Placement.NodeNames = []string{"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"}
	if nodes := os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"); nodes != "" {
		d.Spec.Placement.NodeNames = strings.Split(nodes, ",")
	}
	for _, node := range d.Spec.Placement.NodeNames {
		if node != "k3d-hakopod-dev-server-0" && node != "k3d-hakopod-database-worker-0" {
			t.Fatal("MongoDB native fixtures require the dedicated database development nodes")
		}
	}
	if mode == "standalone" {
		d.Spec.Replicas = 0
	}
	passwordText := []byte(hex.EncodeToString(password))
	if reuse := os.Getenv("HAKOPOD_MONGODB_FIXTURE_ID"); reuse != "" {
		if raw, err := hex.DecodeString(reuse); err != nil || len(raw) != 16 {
			t.Fatal("invalid development MongoDB fixture ID")
		}
		d.ID = reuse
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
			t.Fatal("MongoDB development fixture ownership changed")
		}
		passwordText = secret.Data["password"]
	}
	t.Log("Development MongoDB namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return nil })
			if err != nil {
				t.Error(err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("MongoDB deletion did not finish reclaiming its resources")
	})
	return d, passwordText
}

func waitMongoDBFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource, password []byte) database.Observation {
	t.Helper()
	var o database.Observation
	var err error
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 35*time.Second)
		err = c.ApplyDatabase(step, d, password, func() error { return nil })
		if apierrors.IsInvalid(err) {
			cancel()
			t.Fatal("MongoDB controller rejected the generated specification", err)
		}
		if err == nil {
			o, err = c.ObserveDatabase(step, d)
		}
		cancel()
		if err == nil && o.Status == "ready" {
			return o
		}
		t.Log("Waiting for MongoDB", o.Message, err)
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	t.Fatal("MongoDB did not become ready", err)
	return o
}

func mongoDBFixtureClient(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) *mongo.Client {
	t.Helper()
	for _, m := range o.Members {
		if m.Name != o.Primary || m.Role != "primary" {
			continue
		}
		client, closeClient, err := c.mongodbClient(ctx, d, m, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeClient)
		return client
	}
	t.Fatal("MongoDB fixture has no verified primary")
	return nil
}

func TestManagedMongoDBLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MONGODB_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MONGODB_TEST=1")
	}
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx := liveRecoveryClient(t, 20*time.Minute)
			d, password := newMongoDBFixture(t, ctx, c, mode)
			health := waitMongoDBFixture(t, ctx, c, d, password)
			testMongoDBCertificateRefusal(t, ctx, c, d, health)
			client := mongoDBFixtureClient(t, ctx, c, d, health)
			collection := client.Database("app", options.Database().SetWriteConcern(writeconcern.Majority())).Collection("records")
			value := bson.Binary{Subtype: 128, Data: []byte{0, 10, 255, 128}}
			if _, err := collection.InsertOne(ctx, bson.D{{Key: "_id", Value: 1}, {Key: "data", Value: value}}); err != nil {
				t.Fatal("MongoDB application write failed")
			}
			var found struct {
				Data bson.Binary `bson:"data"`
			}
			if err := collection.FindOne(ctx, bson.D{{Key: "_id", Value: 1}}).Decode(&found); err != nil || found.Data.Subtype != 128 || !bytes.Equal(found.Data.Data, value.Data) {
				t.Fatal("MongoDB changed binary application data")
			}
			if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Err(); err == nil {
				t.Fatal("MongoDB application can administer cluster status")
			}
			for _, m := range health.Members {
				if m.Role != "replica" {
					continue
				}
				replica, closeReplica, err := c.mongodbClient(ctx, d, m, false)
				if err != nil {
					t.Fatal(err)
				}
				_, err = replica.Database("app", options.Database().SetReadPreference(readpref.Secondary())).Collection("records").InsertOne(ctx, bson.D{{Key: "_id", Value: 2}})
				closeReplica()
				if err == nil {
					t.Fatal("MongoDB secondary accepted application writes")
				}
			}
			if health.TLS == nil || !health.TLS.Verified || !health.TLS.PlaintextRejected {
				t.Fatal("MongoDB did not verify native TLS")
			}
			health = testMongoDBRenewal(t, ctx, c, d, health)
			renewed := mongoDBFixtureClient(t, ctx, c, d, health)
			if err := renewed.Database("app").Collection("records").FindOne(ctx, bson.D{{Key: "_id", Value: 1}}).Decode(&found); err != nil || !bytes.Equal(found.Data.Data, value.Data) {
				t.Fatal("MongoDB renewal changed application data")
			}
			if health.EngineMetrics == nil || !health.EngineMetrics.Available {
				t.Fatal("MongoDB native monitoring was not observed")
			}
			t.Log("MongoDB native TLS, plaintext rejection, BSON fidelity, application privileges and member roles verified")
		})
	}
}
