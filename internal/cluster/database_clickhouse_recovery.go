package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func clickhouseShardMembers(d database.Resource, o database.Observation) ([]database.Member, error) {
	members := append([]database.Member{}, o.Members...)
	slices.SortFunc(members, func(a, b database.Member) int { return strings.Compare(a.Name, b.Name) })
	selected := make([]database.Member, d.Spec.Shards)
	for _, m := range members {
		shard, err := strconv.Atoi(m.Shard)
		if err != nil || shard < 0 || shard >= len(selected) {
			return nil, fmt.Errorf("ClickHouse shard identity is invalid")
		}
		if selected[shard].Name == "" {
			selected[shard] = m
		}
	}
	for _, m := range selected {
		if m.UID == "" {
			return nil, fmt.Errorf("ClickHouse shard has no observed member")
		}
	}
	return selected, nil
}

func clickhouseArchiveName() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "hakopod-" + hex.EncodeToString(data) + ".zip", nil
}

func (c *Client) clickhouseDatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	for _, m := range o.Members {
		output, err := c.clickhouseQuery(ctx, d, m, "monitor", `SELECT count() FROM system.tables WHERE database='app' FORMAT TSV`)
		if err != nil {
			return err
		}
		if strings.TrimSpace(output) != "0" {
			return fmt.Errorf("ClickHouse recovery requires a separate empty database")
		}
	}
	return nil
}

func (c *Client) cleanupClickHouseRecovery(d database.Resource, members []database.Member, path string) error {
	cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	var result error
	for _, m := range members {
		if err := c.DatabaseExec(cleanup, d, m, []string{"rm", "-f", "--", path}, nil, io.Discard); err != nil {
			result = errors.Join(result, fmt.Errorf("ClickHouse recovery staging cleanup could not be verified"))
		}
	}
	return result
}

func (c *Client) dumpClickHouseDatabase(ctx context.Context, d database.Resource, o database.Observation, out io.Writer) (result error) {
	members, err := clickhouseShardMembers(d, o)
	if err != nil {
		return err
	}
	name, err := clickhouseArchiveName()
	if err != nil {
		return err
	}
	path := "/var/lib/clickhouse/backups/" + name
	defer func() { result = errors.Join(result, c.cleanupClickHouseRecovery(d, members, path)) }()
	manifest := database.ClickHouseArchive{SchemaVersion: 1, DatabaseID: d.ID, Revision: d.Revision, Version: d.Spec.Version, Mode: d.Spec.Mode, Shards: d.Spec.Shards}
	err = database.WriteClickHouseArchive(out, manifest, func(shard int) (int64, error) {
		query := "BACKUP DATABASE app TO Disk('backup', '" + name + "')"
		if err := c.DatabaseExec(ctx, d, members[shard], []string{"clickhouse-client", "--config-file=/etc/hakopod/recovery.xml", "--query", query}, nil, io.Discard); err != nil {
			return 0, err
		}
		output := &databaseBoundedWriter{limit: 64}
		if err := c.DatabaseExec(ctx, d, members[shard], []string{"stat", "-c", "%s", path}, nil, output); err != nil {
			return 0, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(output.String()), 10, 64)
		if err != nil || size > d.Spec.StorageGiB<<30 {
			return 0, fmt.Errorf("ClickHouse backup exceeded its staging capacity")
		}
		return size, nil
	}, func(shard int, w io.Writer) error {
		return c.DatabaseExec(ctx, d, members[shard], []string{"cat", path}, nil, w)
	})
	if err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("ClickHouse topology changed during backup")
	}
	return nil
}

func (c *Client) RestoreClickHouseDatabase(ctx context.Context, d database.Resource, o database.Observation, in io.Reader) (result error) {
	if d.Spec.Engine != "clickhouse" || !d.Spec.TLSRequired() {
		return fmt.Errorf("ClickHouse recovery requires verified TLS")
	}
	if err := c.DatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	members, err := clickhouseShardMembers(d, o)
	if err != nil {
		return err
	}
	name, err := clickhouseArchiveName()
	if err != nil {
		return err
	}
	path := "/var/lib/clickhouse/backups/" + name
	defer func() { result = errors.Join(result, c.cleanupClickHouseRecovery(d, members, path)) }()
	_, err = database.ReadClickHouseArchive(in, func(a database.ClickHouseArchive, shard int, size int64, r io.Reader) error {
		if a.DatabaseID == d.ID || a.Version != d.Spec.Version || a.Mode != d.Spec.Mode || a.Shards != d.Spec.Shards || size > d.Spec.StorageGiB<<30 {
			return fmt.Errorf("ClickHouse restore requires a separate matching layout with enough staging capacity")
		}
		if err := c.DatabaseExec(ctx, d, members[shard], []string{"bash", "-c", `set -eu; umask 077; set -o noclobber; cat > "$1"`, "stage-clickhouse", path}, r, io.Discard); err != nil {
			return fmt.Errorf("stage ClickHouse shard %d: %w", shard, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Staging has consumed the complete authenticated stream. Close application
	// ingress and replace data pods to revoke sessions before changing tables.
	d.Status = "restoring"
	if err = c.databaseNetworkPolicy(ctx, d, func() error { return nil }); err != nil {
		return err
	}
	for _, m := range o.Members {
		pod, _, err := c.databaseExecTarget(ctx, d, m)
		if err != nil {
			return err
		}
		if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			return err
		}
	}
	step, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	var fresh database.Observation
	for step.Err() == nil {
		fresh, err = c.ObserveDatabase(step, d)
		replaced := err == nil && fresh.Status == "ready"
		for _, old := range o.Members {
			for _, current := range fresh.Members {
				if old.UID == current.UID {
					replaced = false
				}
			}
		}
		if replaced {
			break
		}
		if sleepContext(step, 3*time.Second) != nil {
			return fmt.Errorf("ClickHouse restore sessions could not be revoked")
		}
	}
	if step.Err() != nil {
		return fmt.Errorf("ClickHouse restore sessions could not be revoked")
	}
	members, err = clickhouseShardMembers(d, fresh)
	if err != nil {
		return err
	}
	if err = c.DatabaseEmpty(ctx, d, fresh); err != nil {
		return err
	}
	for _, m := range members {
		// Keep the target database and its isolated Keeper identity. Archive SQL
		// runs with privileges limited to app; server users and sources are excluded.
		query := "RESTORE DATABASE app FROM Disk('backup', '" + name + "') SETTINGS create_database='must-exist', allow_different_database_def=true"
		if err = c.DatabaseExec(ctx, d, m, []string{"clickhouse-client", "--config-file=/etc/hakopod/recovery.xml", "--query", query}, nil, io.Discard); err != nil {
			return fmt.Errorf("restore staged ClickHouse shard: %w", err)
		}
	}
	// Client ingress reopens only after durable completion and inspection.
	return nil
}
