package cluster

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

func mongodbFixtureFault(t *testing.T, ctx context.Context, d database.Resource, members []database.Member) (func(), func()) {
	t.Helper()
	helper := os.Getenv("HAKOPOD_MONGODB_FAULT_HELPER")
	if helper == "" {
		t.Fatal("set HAKOPOD_MONGODB_FAULT_HELPER to the owned development quorum helper")
	}
	var stopped []database.Member
	resume := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, member := range stopped {
			if exec.CommandContext(cleanup, "python3", helper, "resume", d.ID, member.Name, member.UID).Run() != nil {
				t.Error("MongoDB fixture could not resume", member.Name)
			}
		}
		stopped = nil
	}
	t.Cleanup(resume)
	verify := func() {
		t.Helper()
		if len(stopped) == 0 {
			t.Fatal("MongoDB fixture has no active fault to verify")
		}
		step, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for _, member := range stopped {
			if exec.CommandContext(step, "python3", helper, "verify-paused", d.ID, member.Name, member.UID).Run() != nil {
				t.Fatal("MongoDB fault ended before its native assertion", member.Name)
			}
		}
	}
	for _, member := range members {
		stopped = append(stopped, member)
		if exec.CommandContext(ctx, "python3", helper, "pause", d.ID, member.Name, member.UID).Run() != nil {
			t.Fatal("MongoDB fixture pause failed")
		}
	}
	verify()
	return resume, verify
}

func testMongoDBFailover(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	primary, err := mongodbObservedPrimary(o)
	if err != nil {
		t.Fatal(err)
	}
	var replicas []*mongo.Client
	for _, member := range o.Members {
		if member.Role == "replica" {
			client, closeClient, err := c.mongodbClient(ctx, d, member, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeClient)
			replicas = append(replicas, client)
		}
	}
	resume, verifyFault := mongodbFixtureFault(t, ctx, d, []database.Member{primary})
	defer resume()
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for wait.Err() == nil {
		for _, client := range replicas {
			var hello struct {
				Primary bool `bson:"isWritablePrimary"`
			}
			step, stop := context.WithTimeout(wait, 3*time.Second)
			err := client.Database("admin").RunCommand(step, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
			stop()
			if err != nil || !hello.Primary {
				continue
			}
			verifyFault()
			var recovered bson.Raw
			if err = client.Database("app").Collection("scale_check").FindOne(wait, bson.D{{Key: "_id", Value: 1}}).Decode(&recovered); err != nil {
				t.Fatal("MongoDB elected primary lost committed data")
			}
			if err = mongodbFixtureMajorityWrite(wait, client, bson.D{{Key: "_id", Value: 2}, {Key: "afterFailover", Value: true}}); err != nil {
				t.Fatal("MongoDB elected primary did not accept a majority write")
			}
			verifyFault()
			resume()
			t.Log("MongoDB elected a different primary and retained majority-committed data while the former primary was paused")
			return
		}
		if sleepContext(wait, time.Second) != nil {
			break
		}
	}
	t.Fatal("MongoDB did not elect a writable primary during the bounded fault")
}

func testMongoDBQuorumLoss(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	if len(o.Members) != 3 {
		t.Fatal("MongoDB quorum fixture requires three voting members")
	}
	client := mongoDBFixtureClient(t, ctx, c, d, o)
	var replicas []database.Member
	for _, member := range o.Members {
		if member.Role == "replica" {
			replicas = append(replicas, member)
		}
	}
	if len(replicas) != 2 {
		t.Fatal("MongoDB quorum fixture has no replica majority")
	}
	resume, verifyFault := mongodbFixtureFault(t, ctx, d, replicas)
	defer resume()
	if err := client.Ping(ctx, readpref.Nearest()); err != nil {
		t.Fatal("MongoDB primary itself became unreachable")
	}
	verifyFault()
	step, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var reply struct {
		OK      float64 `bson:"ok"`
		Concern struct {
			Code int `bson:"code"`
		} `bson:"writeConcernError"`
	}
	err := client.Database("app").RunCommand(step, bson.D{{Key: "insert", Value: "scale_check"}, {Key: "documents", Value: bson.A{bson.D{{Key: "_id", Value: 99}}}}, {Key: "writeConcern", Value: bson.D{{Key: "w", Value: "majority"}, {Key: "wtimeout", Value: 5000}}}}).Decode(&reply)
	var native mongo.ServerError
	refused := errors.As(err, &native) && (native.HasErrorCode(10107) || native.HasErrorCode(11602) || native.HasErrorCode(13435) || native.HasErrorCode(64))
	if !refused && !(err == nil && reply.OK == 1 && reply.Concern.Code == 64) {
		t.Fatal("MongoDB did not prove refusal of a majority acknowledgment without quorum")
	}
	verifyFault()
	resume()
	t.Log("MongoDB primary stayed reachable but could not acknowledge a majority write with both replicas paused; the write outcome remains ambiguous")
}

func TestManagedMongoDBScalingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MONGODB_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MONGODB_TEST=1")
	}
	c, ctx := liveRecoveryClient(t, 45*time.Minute)
	d, password := newMongoDBFixture(t, ctx, c, "cluster")
	health := waitMongoDBFixture(t, ctx, c, d, password)
	client := mongoDBFixtureClient(t, ctx, c, d, health)
	if err := mongodbFixtureMajorityWrite(ctx, client, bson.D{{Key: "_id", Value: 1}, {Key: "value", Value: bson.Binary{Subtype: 128, Data: []byte{0, 10, 255, 128}}}}); err != nil {
		t.Fatal("MongoDB majority fixture write failed")
	}
	for _, replicas := range []int{4, 6, 2} {
		d.Spec.Replicas = replicas
		d.Revision++
		health = waitMongoDBFixture(t, ctx, c, d, password)
		if len(health.Members) != replicas+1 {
			t.Fatal("MongoDB resize did not converge to every voting member")
		}
		client = mongoDBFixtureClient(t, ctx, c, d, health)
		var got struct {
			Value bson.Binary `bson:"value"`
		}
		if err := client.Database("app").Collection("scale_check").FindOne(ctx, bson.D{{Key: "_id", Value: 1}}).Decode(&got); err != nil || got.Value.Subtype != 128 || string(got.Value.Data) != string([]byte{0, 10, 255, 128}) {
			t.Fatal("MongoDB resize changed committed BSON")
		}
		t.Log("Verified MongoDB voting member count", replicas+1)
	}
	testMongoDBFailover(t, ctx, c, d, health)
	health = waitMongoDBFixture(t, ctx, c, d, password)
	testMongoDBQuorumLoss(t, ctx, c, d, health)
	health = waitMongoDBFixture(t, ctx, c, d, password)
	client = mongoDBFixtureClient(t, ctx, c, d, health)
	if count, err := client.Database("app").Collection("scale_check").CountDocuments(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: bson.A{1, 2}}}}}); err != nil || count != 2 {
		t.Fatal("MongoDB recovery lost confirmed writes")
	}
	testMongoDBCredentialLogs(t, ctx, c, d)
}

func mongodbFixtureMajorityWrite(ctx context.Context, client *mongo.Client, document bson.D) error {
	step, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := client.Database("app", options.Database().SetWriteConcern(writeconcern.Majority())).Collection("scale_check").InsertOne(step, document)
	return err
}
