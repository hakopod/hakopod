package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const localTable = "app.public_endpoint_acceptance_local"
const distributedTable = "app.public_endpoint_acceptance"
const payloadHex = "0080FF0D0A41"

// Versioned rows provide ClickHouse-native replacement updates and tombstone
// deletes. FINAL verifies the visible state; no asynchronous mutation setting
// is assumed. The fixed names belong only to this disposable acceptance DB.
func dataCheck(ctx context.Context, o options) error {
	table := localTable
	cluster := o.shards > 1 || o.replicas > 1
	if cluster {
		table = distributedTable
	}
	run := func(sql string) error { _, err := query(ctx, o, sql); return err }
	poll := func(sql, want string) error {
		until := time.Now().Add(45 * time.Second)
		for {
			out, err := query(ctx, o, sql)
			if err == nil && strings.TrimSpace(out) == want {
				return nil
			}
			if time.Now().After(until) {
				return errors.New("data did not converge")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	engine := "ReplacingMergeTree(version, deleted)"
	if cluster {
		engine = "ReplicatedReplacingMergeTree(version, deleted)"
	}
	if o.check == "crud" {
		if cluster {
			if err := run("DROP TABLE IF EXISTS " + distributedTable + " SYNC"); err != nil {
				return err
			}
		}
		if err := run("DROP TABLE IF EXISTS " + localTable + " SYNC"); err != nil {
			return err
		}
		if err := run("CREATE TABLE " + localTable + " (id UInt64, version UInt64, deleted UInt8, payload String) ENGINE=" + engine + " ORDER BY id"); err != nil {
			return err
		}
		if cluster {
			if err := run(fmt.Sprintf("CREATE TABLE %s AS %s ENGINE=Distributed(managed, app, public_endpoint_acceptance_local, id %% %d)", distributedTable, localTable, o.shards)); err != nil {
				return err
			}
		}
		// Replicated database DDL can reach other public backend members later.
		if err := poll("SELECT count() FROM "+table, "0"); err != nil {
			return err
		}
		rows := []string{}
		for id := 0; id < o.shards; id++ {
			rows = append(rows, fmt.Sprintf("(%d,1,0,'initial')", id))
		}
		settings := ""
		if cluster {
			settings = " SETTINGS distributed_foreground_insert=1"
		}
		if err := run("INSERT INTO " + table + settings + " VALUES " + strings.Join(rows, ",")); err != nil {
			return err
		}
		rows = nil
		for id := 0; id < o.shards; id++ {
			rows = append(rows, fmt.Sprintf("(%d,2,0,unhex('%s'))", id, payloadHex), fmt.Sprintf("(%d,1,0,'scratch')", id+o.shards), fmt.Sprintf("(%d,2,1,'')", id+o.shards))
		}
		if err := run("INSERT INTO " + table + settings + " VALUES " + strings.Join(rows, ",")); err != nil {
			return err
		}
	}
	want := []string{}
	for id := 0; id < o.shards; id++ {
		want = append(want, fmt.Sprintf("%d\t%s", id, payloadHex))
	}
	if err := poll("SELECT id,hex(payload) FROM "+table+" FINAL WHERE deleted=0 ORDER BY id FORMAT TSV", strings.Join(want, "\n")); err != nil {
		return err
	}
	// A restored database must also accept fresh versioned writes. Keep these
	// tombstoned, so later transport and node-replacement checks see the same data.
	if o.check == "data-preserved" {
		settings := ""
		if cluster {
			settings = " SETTINGS distributed_foreground_insert=1"
		}
		rows := []string{}
		for id := 0; id < o.shards; id++ {
			rows = append(rows, fmt.Sprintf("(%d,1,0,'restored-write')", id+2*o.shards), fmt.Sprintf("(%d,2,1,'')", id+2*o.shards))
		}
		if err := run("INSERT INTO " + table + settings + " VALUES " + strings.Join(rows, ",")); err != nil {
			return err
		}
		return poll("SELECT id,hex(payload) FROM "+table+" FINAL WHERE deleted=0 ORDER BY id FORMAT TSV", strings.Join(want, "\n"))
	}
	return nil
}
