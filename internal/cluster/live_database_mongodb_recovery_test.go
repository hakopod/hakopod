package cluster

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedMongoDBRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MONGODB_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MONGODB_TEST=1")
	}
	c, ctx := liveRecoveryClient(t, 25*time.Minute)
	source, password := newMongoDBFixture(t, ctx, c, "standalone")
	health := waitMongoDBFixture(t, ctx, c, source, password)
	client := mongoDBFixtureClient(t, ctx, c, source, health)
	db := client.Database("app", options.Database().SetWriteConcern(writeconcern.Majority()))
	command := func(cmd bson.D) {
		t.Helper()
		if err := db.RunCommand(ctx, cmd).Err(); err != nil {
			t.Fatal("MongoDB recovery fixture schema failed", err)
		}
	}
	command(bson.D{{Key: "create", Value: "records"}, {Key: "validator", Value: bson.D{{Key: "active", Value: true}}}, {Key: "validationLevel", Value: "strict"}})
	command(bson.D{{Key: "createIndexes", Value: "records"}, {Key: "indexes", Value: bson.A{bson.D{{Key: "name", Value: "code_unique"}, {Key: "key", Value: bson.D{{Key: "code", Value: 1}}}, {Key: "unique", Value: true}, {Key: "hidden", Value: true}, {Key: "partialFilterExpression", Value: bson.D{{Key: "active", Value: true}}}}}}})
	command(bson.D{
		{Key: "create", Value: "recent"},
		{Key: "viewOn", Value: "records"},
		{Key: "pipeline", Value: bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "active", Value: true}}}}}},
	})
	command(bson.D{{Key: "create", Value: "events"}, {Key: "capped", Value: true}, {Key: "size", Value: int64(65536)}, {Key: "max", Value: int64(100)}})
	command(bson.D{{Key: "create", Value: "samples"}, {Key: "timeseries", Value: bson.D{{Key: "timeField", Value: "at"}, {Key: "metaField", Value: "sensor"}, {Key: "granularity", Value: "seconds"}}}, {Key: "expireAfterSeconds", Value: int64(86400)}})
	command(bson.D{{Key: "createIndexes", Value: "records"}, {Key: "indexes", Value: bson.A{bson.D{{Key: "name", Value: "expiry"}, {Key: "key", Value: bson.D{{Key: "expires", Value: 1}}}, {Key: "expireAfterSeconds", Value: int64(0)}}}}})
	value := bson.Binary{Subtype: 128, Data: []byte{0, 10, 255, 128}}
	decimal, err := bson.ParseDecimal128("123456789.123456789")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(365 * 24 * time.Hour).Truncate(time.Millisecond)
	document := bson.D{{Key: "_id", Value: 1}, {Key: "active", Value: true}, {Key: "code", Value: "stable"}, {Key: "binary", Value: value}, {Key: "amount", Value: decimal}, {Key: "stamp", Value: bson.Timestamp{T: 99, I: 7}}, {Key: "expires", Value: expires}}
	if _, err = db.Collection("records").InsertOne(ctx, document); err != nil {
		t.Fatal(err)
	}
	// A historical document may predate its current validator. Restore must
	// preserve it and still enforce that validator for future application writes.
	member, err := mongodbObservedPrimary(health)
	if err != nil {
		t.Fatal(err)
	}
	internal, closeInternal, err := c.mongodbClientRole(ctx, source, member, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeInternal)
	if _, err = internal.Database("app", options.Database().SetWriteConcern(writeconcern.Majority())).Collection("records").InsertOne(ctx, bson.D{{Key: "_id", Value: 2}, {Key: "active", Value: false}}, options.InsertOne().SetBypassDocumentValidation(true)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("events").InsertOne(ctx, bson.D{{Key: "_id", Value: 1}, {Key: "binary", Value: value}}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("samples").InsertOne(ctx, bson.D{{Key: "at", Value: expires}, {Key: "sensor", Value: "fixture"}, {Key: "value", Value: 12}}); err != nil {
		t.Fatal(err)
	}
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err = c.DumpDatabase(ctx, source, health, archive); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("records").InsertOne(ctx, bson.D{{Key: "_id", Value: 3}, {Key: "active", Value: true}, {Key: "code", Value: "after"}}); err != nil {
		t.Fatal(err)
	}
	target, targetPassword := newMongoDBFixture(t, ctx, c, "standalone")
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: source.ID}
	before := waitMongoDBFixture(t, ctx, c, target, targetPassword)
	stale := mongoDBFixtureClient(t, ctx, c, target, before)
	if err = c.RestoreMongoDBDatabase(ctx, target, before, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	probe, stop := context.WithTimeout(ctx, 10*time.Second)
	if err = stale.Ping(probe, readpref.Primary()); err == nil {
		stop()
		t.Fatal("MongoDB recovery retained an old native session")
	}
	stop()
	after := waitMongoDBFixture(t, ctx, c, target, targetPassword)
	recovered := mongoDBFixtureClient(t, ctx, c, target, after).Database("app")
	var raw bson.Raw
	if err = recovered.Collection("records").FindOne(ctx, bson.D{{Key: "_id", Value: 1}}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	expected, _ := bson.Marshal(document)
	if !bytes.Equal(raw, expected) {
		t.Fatal("MongoDB recovery changed raw BSON types or data")
	}
	for collection, want := range map[string]int64{"records": 2, "recent": 1, "events": 1, "samples": 1} {
		got, err := recovered.Collection(collection).CountDocuments(ctx, bson.D{})
		if err != nil || got != want {
			t.Fatal("MongoDB recovery changed collection or view data", collection, got, err)
		}
	}
	_, err = recovered.Collection("records").InsertOne(ctx, bson.D{{Key: "_id", Value: 4}, {Key: "active", Value: false}})
	var validation mongo.ServerError
	if !errors.As(err, &validation) || !validation.HasErrorCode(121) {
		t.Fatal("MongoDB recovery did not retain its validator")
	}
	if _, err = recovered.Collection("records").InsertOne(ctx, bson.D{{Key: "_id", Value: 4}, {Key: "active", Value: true}, {Key: "code", Value: "stable"}}); !mongo.IsDuplicateKeyError(err) {
		t.Fatal("MongoDB recovery did not retain its unique index")
	}
	var index bson.Raw
	indexes, err := recovered.Collection("records").Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer indexes.Close(ctx)
	hidden, ttl := false, false
	for indexes.Next(ctx) {
		if indexes.Decode(&index) != nil {
			t.Fatal("invalid recovered MongoDB index")
		}
		if index.Lookup("name").StringValue() == "code_unique" {
			hidden = index.Lookup("hidden").Boolean()
		}
		if index.Lookup("name").StringValue() == "expiry" {
			ttl = index.Lookup("expireAfterSeconds").AsInt64() == 0
		}
	}
	if indexes.Err() != nil || !hidden || !ttl {
		t.Fatal("MongoDB index visibility or expiry changed")
	}
	if got, err := db.Collection("records").CountDocuments(ctx, bson.D{}); err != nil || got != 3 {
		t.Fatal("MongoDB recovery changed its source")
	}
	if err = c.RestoreMongoDBDatabase(ctx, target, after, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("MongoDB recovery accepted a nonempty target")
	}
	policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(target.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+target.ID] == "true" {
				t.Fatal("MongoDB restore reopened application access before durable success")
			}
		}
	}
	stamp := time.Now().UTC()
	target.Status, target.Recovery.RestoredAt = "ready", &stamp
	if err = c.ReconcileDatabaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	policy, err = c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(target.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+target.ID] == "true" {
				t.Fatal("MongoDB recovery reopened access before inspection")
			}
		}
	}
	target.Recovery.InspectedAt = &stamp
	if err = c.ReconcileDatabaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	policy, err = c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(target.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			opened = opened || peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+target.ID] == "true"
		}
	}
	if !opened {
		t.Fatal("MongoDB recovery did not restore approved access after durable success and inspection")
	}
	t.Log("MongoDB snapshot recovered raw BSON, historical validation exceptions, unique and TTL indexes, views, capped and time-series collections into an isolated separate target; source and session fences verified")
}
