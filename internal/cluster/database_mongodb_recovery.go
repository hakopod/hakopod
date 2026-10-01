package cluster

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mongodbObservedPrimary(o database.Observation) (database.Member, error) {
	for _, member := range o.Members {
		if member.Name == o.Primary && member.Role == "primary" {
			return member, nil
		}
	}
	return database.Member{}, fmt.Errorf("MongoDB primary is unavailable")
}

func (c *Client) dumpMongoDBDatabase(ctx context.Context, d database.Resource, o database.Observation, out io.Writer) error {
	member, err := mongodbObservedPrimary(o)
	if err != nil {
		return err
	}
	client, closeClient, err := c.mongodbClient(ctx, d, member, false)
	if err != nil {
		return err
	}
	defer closeClient()
	if err = database.CaptureMongoDBSnapshot(ctx, client, out); err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("MongoDB topology changed during backup")
	}
	return nil
}

func (c *Client) mongodbDatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	member, err := mongodbObservedPrimary(o)
	if err != nil {
		return err
	}
	client, closeClient, err := c.mongodbClient(ctx, d, member, false)
	if err != nil {
		return err
	}
	defer closeClient()
	return database.MongoDBSnapshotTargetEmpty(ctx, client)
}

// Recovery only runs against the separate target reserved by a durable job.
// Closing ingress does not revoke established TCP sessions. Replacing every
// owned member fences those sessions without granting application accounts any
// administrative privileges. Failures leave the target isolated for inspection.
func (c *Client) RestoreMongoDBDatabase(ctx context.Context, d database.Resource, o database.Observation, in io.Reader) error {
	if d.Spec.Engine != "mongodb" || !d.Spec.TLSRequired() || d.Status != "restoring" || d.Recovery == nil || d.Recovery.JobID == "" {
		return fmt.Errorf("MongoDB recovery requires an isolated managed recovery target")
	}
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	if err := c.databaseNetworkPolicy(ctx, d, func() error { return ctx.Err() }); err != nil {
		return err
	}
	if len(o.Members) != d.Spec.Members() {
		return fmt.Errorf("MongoDB recovery membership changed")
	}
	for _, member := range o.Members {
		pod, _, err := c.databaseExecTarget(ctx, d, member)
		if err != nil {
			return err
		}
		if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			return err
		}
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var fresh database.Observation
	for wait.Err() == nil {
		step, stop := context.WithTimeout(wait, 35*time.Second)
		observed, err := c.ObserveDatabase(step, d)
		stop()
		if err == nil && observed.Status == "ready" && len(observed.Members) == d.Spec.Members() {
			replaced := true
			for _, member := range observed.Members {
				for _, previous := range o.Members {
					if member.UID == previous.UID {
						replaced = false
					}
				}
			}
			if replaced {
				fresh = observed
				break
			}
		}
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	if fresh.Status != "ready" {
		return fmt.Errorf("MongoDB recovery could not verify session revocation and healthy replacement members")
	}
	member, err := mongodbObservedPrimary(fresh)
	if err != nil {
		return err
	}
	client, closeClient, err := c.mongodbClientRole(ctx, d, member, "recovery")
	if err != nil {
		return err
	}
	defer closeClient()
	if err = database.RestoreMongoDBSnapshot(ctx, client, in); err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != fresh.TopologyFingerprint {
		return fmt.Errorf("MongoDB topology changed during recovery")
	}
	// The durable job publishes readiness. Until it commits success, application
	// ingress stays closed even when all restored members are healthy.
	return nil
}
