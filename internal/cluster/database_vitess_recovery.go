package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func vitessObservedPrimaries(d database.Resource, o database.Observation) ([]database.Member, error) {
	if d.Spec.Engine != "vitess" || o.Status != "ready" || o.Revision != d.Revision || len(o.Members) != d.Spec.Members() {
		return nil, fmt.Errorf("Vitess requires a complete current topology")
	}
	names := d.Spec.VitessShardNames()
	if len(names) != d.Spec.Shards {
		return nil, fmt.Errorf("Vitess shard layout is invalid")
	}
	primaries := make([]database.Member, len(names))
	seen := make(map[string]bool, len(o.Members))
	counts := make([]int, len(names))
	for _, m := range o.Members {
		i := slices.Index(names, m.Shard)
		if i < 0 || m.UID == "" || m.Name == "" || seen[m.UID] || !m.Ready || (m.Role != "primary" && m.Role != "replica") {
			return nil, fmt.Errorf("Vitess tablet identity, role or readiness is invalid")
		}
		seen[m.UID] = true
		counts[i]++
		if m.Role == "primary" {
			if primaries[i].UID != "" {
				return nil, fmt.Errorf("Vitess shard has multiple primaries")
			}
			primaries[i] = m
		}
	}
	for i, m := range primaries {
		if m.UID == "" || counts[i] != 1+d.Spec.Replicas {
			return nil, fmt.Errorf("Vitess shard has no verified primary or complete replica set")
		}
	}
	return primaries, nil
}

func sameVitessTabletSet(before, after []database.Member) bool {
	if len(before) != len(after) {
		return false
	}
	want := make(map[string]struct{}, len(before))
	for _, member := range before {
		want[member.Shard+"\x00"+member.Name+"\x00"+member.UID] = struct{}{}
	}
	for _, member := range after {
		key := member.Shard + "\x00" + member.Name + "\x00" + member.UID
		if _, ok := want[key]; !ok {
			return false
		}
		delete(want, key)
	}
	return len(want) == 0
}

// Recovery replaces gateways to close existing sessions. Wait only for those
// owned replacements to become ready; changes to the tablets still abort it.
func waitVitessRecoveryTopology(ctx context.Context, before database.Observation, observe func(context.Context) (database.Observation, error)) (database.Observation, error) {
	wait, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	for {
		if err := wait.Err(); err != nil {
			return database.Observation{}, fmt.Errorf("Vitess recovery gateway readiness interrupted: %w", err)
		}
		next, err := observe(wait)
		if next.Revision != before.Revision || !sameVitessTabletSet(before.Members, next.Members) {
			return database.Observation{}, fmt.Errorf("Vitess topology changed during recovery")
		}
		if err == nil {
			if next.Status != "ready" {
				return database.Observation{}, fmt.Errorf("Vitess topology changed during recovery")
			}
			return next, nil
		}
		if !errors.Is(err, errVitessGatewayNotReady) {
			return database.Observation{}, fmt.Errorf("Vitess recovery observation failed: %w", err)
		}
		if err := sleepContext(wait, time.Second); err != nil {
			return database.Observation{}, fmt.Errorf("Vitess recovery gateway readiness interrupted: %w", err)
		}
	}
}

var vitessGTIDSet = regexp.MustCompile(`^[0-9a-fA-F:-]+(?:,[0-9a-fA-F:-]+)*$`)

func vitessWaitForGTIDQuery(gtid string) (string, error) {
	gtid = strings.TrimSpace(gtid)
	if len(gtid) == 0 || len(gtid) > 64<<10 {
		return "", fmt.Errorf("Vitess recovery transaction identity is invalid")
	}
	gtid = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, gtid)
	if len(gtid) == 0 || !vitessGTIDSet.MatchString(gtid) {
		return "", fmt.Errorf("Vitess recovery transaction identity is invalid")
	}
	return "SELECT WAIT_FOR_EXECUTED_GTID_SET(CONVERT(0x" + hex.EncodeToString([]byte(gtid)) + " USING utf8mb4),30)", nil
}

func vitessLocalCommand(user, query string) []string {
	command := []string{"mysql", "--no-defaults", "--protocol=SOCKET", "--socket=/vt/socket/mysql.sock", "--user=" + user, "--batch", "--raw", "--skip-column-names", "--connect-timeout=3", "--binary-mode=1", "--local-infile=0", "--database=app"}
	if query != "" {
		command = append(command, "--execute", query)
	}
	return command
}

func (c *Client) dumpVitessDatabase(ctx context.Context, d database.Resource, o database.Observation, out io.Writer) error {
	members, err := vitessObservedPrimaries(d, o)
	if err != nil {
		return err
	}
	vschema, err := d.Spec.VitessVSchema()
	if err != nil {
		return err
	}
	manifest := database.VitessArchive{SchemaVersion: 1, DatabaseID: d.ID, Revision: d.Revision, Version: d.Spec.Version, ServerVersion: database.VitessServerVersion, MySQLVersion: database.VitessMySQLVersion, Mode: d.Spec.Mode, Shards: d.Spec.VitessShardNames(), VSchema: vschema, Consistency: "per-shard-read-lock", TopologyFingerprint: o.TopologyFingerprint}
	if d.Spec.Vitess != nil {
		manifest.Tables = d.Spec.Vitess.Tables
	}
	err = database.WriteVitessArchive(out, manifest, func(i int, writer io.Writer) error {
		version := &databaseBoundedWriter{limit: 128}
		if err := c.DatabaseExec(ctx, d, members[i], vitessLocalCommand("vt_dba", "SELECT VERSION()"), nil, version); err != nil {
			return err
		}
		if strings.TrimSpace(version.String()) != database.VitessMySQLVersion {
			return fmt.Errorf("Vitess MySQL version differs from the approved recovery contract")
		}
		command := []string{"mysqldump", "--no-defaults", "--protocol=SOCKET", "--socket=/vt/socket/mysql.sock", "--user=vt_dba", "--lock-all-tables", "--set-gtid-purged=OFF", "--no-tablespaces", "--hex-blob", "--routines", "--events", "--triggers", "--skip-add-locks", "--skip-dump-date", "--column-statistics=0", "app"}
		return c.DatabaseExec(ctx, d, members[i], command, nil, writer)
	})
	if err != nil {
		return err
	}
	after, err := c.ObserveDatabase(ctx, d)
	if err != nil || after.Status != "ready" || after.TopologyFingerprint != o.TopologyFingerprint {
		return fmt.Errorf("Vitess topology changed during capture")
	}
	return nil
}

func (c *Client) vitessDatabaseEmpty(ctx context.Context, d database.Resource, o database.Observation) error {
	members, err := vitessObservedPrimaries(d, o)
	if err != nil {
		return err
	}
	for _, m := range members {
		out := &databaseBoundedWriter{limit: 64}
		query := "SELECT (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='app')+(SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema='app')+(SELECT COUNT(*) FROM information_schema.events WHERE event_schema='app')"
		if err = c.DatabaseExec(ctx, d, m, vitessLocalCommand("vt_dba", query), nil, out); err != nil {
			return err
		}
		if strings.TrimSpace(out.String()) != "0" {
			return fmt.Errorf("Vitess recovery requires a separate empty database")
		}
	}
	return nil
}

func (c *Client) cleanupVitessRecovery(d database.Resource, members []database.Member, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result error
	for _, m := range members {
		if err := c.DatabaseExec(ctx, d, m, []string{"rm", "-f", "--", "/vt/vtdataroot/" + name}, nil, io.Discard); err != nil {
			result = errors.Join(result, fmt.Errorf("Vitess recovery staging cleanup could not be verified"))
		}
	}
	return result
}

// RestoreVitessDatabase stages and authenticates every shard before it closes
// client access, revokes gateway sessions and imports as an app-only SQL user.
func (c *Client) RestoreVitessDatabase(ctx context.Context, d database.Resource, o database.Observation, in io.Reader) (result error) {
	if d.Spec.Engine != "vitess" || !d.Spec.TLSRequired() || d.Status != "restoring" || d.Recovery == nil {
		return fmt.Errorf("Vitess recovery requires a reviewed isolated target")
	}
	if err := c.vitessDatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	members, err := vitessObservedPrimaries(d, o)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	name := "hakopod-restore-" + hex.EncodeToString(random) + ".sql"
	path := "/vt/vtdataroot/" + name
	defer func() { result = errors.Join(result, c.cleanupVitessRecovery(d, members, name)) }()
	vschema, err := d.Spec.VitessVSchema()
	if err != nil {
		return err
	}
	_, err = database.ReadVitessArchive(in, func(a database.VitessArchive, i int, reader io.Reader) error {
		if a.DatabaseID == d.ID || a.Version != d.Spec.Version || a.Mode != d.Spec.Mode || !slices.Equal(a.Shards, d.Spec.VitessShardNames()) || string(a.VSchema) != string(vschema) {
			return fmt.Errorf("Vitess restore requires a separate target with the same version, shard map and routing schema")
		}
		bounded := &io.LimitedReader{R: reader, N: (d.Spec.StorageGiB << 30) + 1}
		if err := c.DatabaseExec(ctx, d, members[i], []string{"sh", "-c", `set -eu; umask 077; set -C; cat > "$1"`, "stage-vitess", path}, bounded, io.Discard); err != nil {
			return fmt.Errorf("Vitess shard staging failed")
		}
		if bounded.N == 0 {
			return fmt.Errorf("Vitess shard archive exceeds target staging capacity")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = c.vitessNetworkPolicy(ctx, d, func() error { return nil }); err != nil {
		return err
	}
	// NetworkPolicy does not terminate established connections. Gateway Pod
	// replacement disconnects those clients while the recovery gate stays shut.
	gateways, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=gateway", Limit: 3})
	if err != nil || gateways.Continue != "" || len(gateways.Items) > 2 {
		return fmt.Errorf("Vitess gateway session inventory is unavailable")
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetLabels()[databaseOwner] != d.ID {
		return fmt.Errorf("Vitess recovery controller ownership changed")
	}
	for _, pod := range gateways.Items {
		if !c.vitessPodOwned(ctx, pod, object.GetUID()) {
			return fmt.Errorf("Vitess recovery gateway ownership changed")
		}
		if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			return err
		}
	}
	// Wait for confirmed process removal before executing input SQL. A delete
	// request alone is not evidence that old sessions have stopped.
	waitCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	for {
		gone := true
		for _, old := range gateways.Items {
			current, getErr := c.kube.CoreV1().Pods(old.Namespace).Get(waitCtx, old.Name, metav1.GetOptions{})
			if getErr == nil && current.UID == old.UID {
				gone = false
			}
			if getErr != nil && !apierrors.IsNotFound(getErr) {
				return getErr
			}
		}
		if gone {
			break
		}
		if err = sleepContext(waitCtx, time.Second); err != nil {
			return fmt.Errorf("Vitess gateway sessions could not be revoked")
		}
	}
	if err = c.vitessDatabaseEmpty(ctx, d, o); err != nil {
		return err
	}
	gtids := make([]string, len(members))
	for i, m := range members {
		role := &databaseBoundedWriter{limit: 32}
		if err = c.DatabaseExec(ctx, d, m, vitessLocalCommand("vt_dba", "SELECT @@super_read_only"), nil, role); err != nil || strings.TrimSpace(role.String()) != "0" {
			return fmt.Errorf("Vitess recovery primary changed")
		}
		command := []string{"sh", "-c", `exec mysql --no-defaults --protocol=SOCKET --socket=/vt/socket/mysql.sock --user=vt_app --database=app --binary-mode=1 --local-infile=0 --batch < "$1"`, "restore-vitess", path}
		if err = c.DatabaseExec(ctx, d, m, command, nil, io.Discard); err != nil {
			return fmt.Errorf("Vitess shard import failed")
		}
		executed := &databaseBoundedWriter{limit: 64 << 10}
		if err = c.DatabaseExec(ctx, d, m, vitessLocalCommand("vt_dba", "SELECT @@GLOBAL.gtid_executed"), nil, executed); err != nil {
			return fmt.Errorf("Vitess recovery transaction identity is unavailable")
		}
		gtids[i] = strings.TrimSpace(executed.String())
	}
	after, err := waitVitessRecoveryTopology(ctx, o, func(wait context.Context) (database.Observation, error) {
		return c.ObserveDatabase(wait, d)
	})
	if err != nil {
		return err
	}
	current, err := vitessObservedPrimaries(d, after)
	if err != nil {
		return fmt.Errorf("Vitess topology changed during recovery")
	}
	for i, member := range current {
		query, queryErr := vitessWaitForGTIDQuery(gtids[i])
		if queryErr != nil {
			return queryErr
		}
		waited := &databaseBoundedWriter{limit: 32}
		if err = c.DatabaseExec(ctx, d, member, vitessLocalCommand("vt_dba", query), nil, waited); err != nil || strings.TrimSpace(waited.String()) != "0" {
			return fmt.Errorf("Vitess recovery transaction was not durable on the current primary")
		}
	}
	final, err := c.ObserveDatabase(ctx, d)
	if err != nil || final.Status != "ready" || !sameVitessTabletSet(after.Members, final.Members) {
		return fmt.Errorf("Vitess topology changed during recovery")
	}
	finalPrimaries, err := vitessObservedPrimaries(d, final)
	if err != nil {
		return fmt.Errorf("Vitess topology changed during recovery")
	}
	for i := range current {
		if finalPrimaries[i].UID != current[i].UID {
			return fmt.Errorf("Vitess primary changed during recovery durability verification")
		}
	}
	return nil
}
