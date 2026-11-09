package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hakopod/hakopod/internal/actions/vm"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// ActionsCapacity is one cloud account where GitHub pools can run each job on a
// single-use VM. It holds no credential: Hakopod's own AWS identity assumes
// RoleARN with the external ID, so only identifiers are stored.
type ActionsCapacity struct {
	Name             string   `json:"name" toml:"name"`
	Cloud            string   `json:"cloud" toml:"cloud"`
	Region           string   `json:"region" toml:"region"`
	RoleARN          string   `json:"role_arn" toml:"role_arn"`
	ExternalID       string   `json:"external_id,omitempty" toml:"external_id,omitempty"`
	SubnetIDs        []string `json:"subnet_ids" toml:"subnet_ids"`
	SecurityGroupIDs []string `json:"security_group_ids" toml:"security_group_ids"`
	InstanceTypes    []string `json:"instance_types" toml:"instance_types"`
	Architecture     string   `json:"architecture" toml:"architecture"`
	Spot             bool     `json:"spot" toml:"spot"`
	DiskGiB          int64    `json:"disk_gib" toml:"disk_gib"`
	MaxInstances     int64    `json:"max_instances" toml:"max_instances"`
	// Image is runs-on-full (the default), hakopod-minimal, an AMI ID or a
	// resolve:ssm: parameter holding one.
	Image string `json:"image" toml:"image"`
	// WarmPool keeps this many stopped on-demand instances ready to start
	// for a job. They count toward MaxInstances.
	WarmPool int64 `json:"warm_pool" toml:"warm_pool"`
	// Environments lists "project/environment" pairs allowed to use this
	// capacity; "*" allows every environment.
	Environments []string `json:"environments" toml:"environments"`
}

// ActionsCapacitySettings is the installation's capacity list. ExternalID is
// generated once per installation and used for every capacity that does not
// name its own; customers put it in their role's trust policy.
type ActionsCapacitySettings struct {
	ExternalID string            `json:"external_id"`
	Capacities []ActionsCapacity `json:"capacities"`
}

var (
	awsRegion        = regexp.MustCompile(`^[a-z]{2}(-[a-z]+){1,2}-[0-9]{1,2}$`)
	awsRoleARN       = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	awsExternalID    = regexp.MustCompile(`^[A-Za-z0-9+=,.@:/_-]{2,1000}$`)
	awsSubnetID      = regexp.MustCompile(`^subnet-[0-9a-f]{8,17}$`)
	awsSecurityGroup = regexp.MustCompile(`^sg-[0-9a-f]{8,17}$`)
	awsInstanceType  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}\.[a-z0-9]{1,16}$`)
	capacityImage    = regexp.MustCompile(`^(runs-on-full|hakopod-minimal|ami-[0-9a-f]{8,17}|resolve:ssm:/[A-Za-z0-9_./-]{1,200})$`)
	capacityScope    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)
)

func distinct(values []string, min, max int, valid *regexp.Regexp) bool {
	if len(values) < min || len(values) > max {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !valid.MatchString(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// normalize fills defaults and refuses anything Hakopod would have to guess.
func (c *ActionsCapacity) normalize() error {
	bad := func(field, text string) error {
		return fmt.Errorf("%w: capacity %q %s: %s", ErrInput, c.Name, field, text)
	}
	if !spec.ActionsCapacityName.MatchString(c.Name) {
		return fmt.Errorf("%w: capacity names use lowercase letters, digits and hyphens", ErrInput)
	}
	if c.Cloud != "aws" {
		return bad("cloud", "only aws is available")
	}
	if !awsRegion.MatchString(c.Region) {
		return bad("region", "use an AWS region such as eu-central-1")
	}
	if !awsRoleARN.MatchString(c.RoleARN) {
		return bad("role_arn", "use an IAM role ARN")
	}
	if c.ExternalID != "" && !awsExternalID.MatchString(c.ExternalID) {
		return bad("external_id", "use 2–1000 letters, digits or +=,.@:/_-")
	}
	if !distinct(c.SubnetIDs, 1, 10, awsSubnetID) {
		return bad("subnet_ids", "list 1–10 distinct subnet IDs")
	}
	if !distinct(c.SecurityGroupIDs, 1, 5, awsSecurityGroup) {
		return bad("security_group_ids", "list 1–5 distinct security group IDs")
	}
	if !distinct(c.InstanceTypes, 1, 20, awsInstanceType) {
		return bad("instance_types", "list 1–20 distinct instance types")
	}
	if c.Architecture != "amd64" && c.Architecture != "arm64" {
		return bad("architecture", "choose amd64 or arm64")
	}
	if c.DiskGiB == 0 {
		c.DiskGiB = 100
	}
	if c.DiskGiB < 20 || c.DiskGiB > 2000 {
		return bad("disk_gib", "choose 20–2000 GiB")
	}
	if c.MaxInstances < 1 || c.MaxInstances > 1000 {
		return bad("max_instances", "choose 1–1000")
	}
	if c.Image == "" {
		c.Image = vm.ImageRunsOnFull
	}
	if !capacityImage.MatchString(c.Image) {
		return bad("image", "use runs-on-full, hakopod-minimal, an AMI ID or resolve:ssm:/parameter")
	}
	if c.Image == vm.ImageRunsOnFull && c.DiskGiB < 30 {
		return bad("disk_gib", "RunsOn's image needs at least 30 GiB")
	}
	if c.WarmPool < 0 || c.WarmPool > 50 || c.WarmPool > c.MaxInstances {
		return bad("warm_pool", "choose 0–50 standbys, at most max_instances")
	}
	if len(c.Environments) < 1 || len(c.Environments) > 100 {
		return bad("environments", "list 1–100 project/environment pairs, or \"*\"")
	}
	for _, scope := range c.Environments {
		project, environment, ok := strings.Cut(scope, "/")
		if scope != "*" && (!ok || !capacityScope.MatchString(project) || !capacityScope.MatchString(environment)) {
			return bad("environments", "use project/environment pairs, or \"*\"")
		}
	}
	return nil
}

// Allows reports whether a pool in project/environment may use this capacity.
func (c ActionsCapacity) Allows(project, environment string) bool {
	return slices.Contains(c.Environments, "*") || slices.Contains(c.Environments, project+"/"+environment)
}

// Find returns the named capacity with the installation external ID applied.
func (v ActionsCapacitySettings) Find(name string) (ActionsCapacity, bool) {
	for _, c := range v.Capacities {
		if c.Name == name {
			if c.ExternalID == "" {
				c.ExternalID = v.ExternalID
			}
			if c.Image == "" {
				c.Image = vm.ImageRunsOnFull
			}
			return c, true
		}
	}
	return ActionsCapacity{}, false
}

func actionsCapacitySettings(ctx context.Context, q settingsQuerier) (ActionsCapacitySettings, int64, error) {
	var v ActionsCapacitySettings
	var raw []byte
	var rev int64
	err := q.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='actions_capacity'").Scan(&raw, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActionsCapacitySettings{Capacities: []ActionsCapacity{}}, 0, nil
	}
	if err != nil {
		return v, 0, err
	}
	return v, rev, json.Unmarshal(raw, &v)
}

// ActionsCapacitySettings creates the installation external ID on first read,
// so an administrator can write the trust policy before adding a capacity.
func (s *Store) ActionsCapacitySettings(ctx context.Context) (ActionsCapacitySettings, int64, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	initial := ActionsCapacitySettings{ExternalID: "hakopod-" + hex.EncodeToString(id), Capacities: []ActionsCapacity{}}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO installation_settings(name,value) VALUES('actions_capacity',$1) ON CONFLICT(name) DO NOTHING", JSON(initial)); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	return actionsCapacitySettings(ctx, s.Pool)
}

// PutActionsCapacity replaces the capacity list. The external ID stays as
// generated. A capacity cannot be removed while a pool or a running slot names
// it: its instances would be left without credentials to terminate them.
func (s *Store) PutActionsCapacity(ctx context.Context, p Principal, capacities []ActionsCapacity, expectedRevision int64) (ActionsCapacitySettings, int64, error) {
	if len(capacities) > 20 {
		return ActionsCapacitySettings{}, 0, fmt.Errorf("%w: at most 20 capacities", ErrInput)
	}
	names := map[string]bool{}
	for i := range capacities {
		if err := capacities[i].normalize(); err != nil {
			return ActionsCapacitySettings{}, 0, err
		}
		if names[capacities[i].Name] {
			return ActionsCapacitySettings{}, 0, fmt.Errorf("%w: capacity %q is listed twice", ErrInput, capacities[i].Name)
		}
		names[capacities[i].Name] = true
	}
	if _, _, err := s.ActionsCapacitySettings(ctx); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	defer tx.Rollback(ctx)
	var current ActionsCapacitySettings
	var raw []byte
	var rev int64
	if err = tx.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='actions_capacity' FOR UPDATE").Scan(&raw, &rev); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	if rev != expectedRevision {
		return ActionsCapacitySettings{}, 0, fmt.Errorf("%w: Actions capacity changed; reload it and try again", ErrConflict)
	}
	for _, old := range current.Capacities {
		if names[old.Name] {
			continue
		}
		var used string
		err = tx.QueryRow(ctx, `SELECT application_name||'/'||service FROM actions_pools WHERE config->'actions'->>'capacity'=$1
 AND (NOT removed OR EXISTS(SELECT 1 FROM actions_slots v WHERE v.application_id=actions_pools.application_id AND v.service=actions_pools.service))
 ORDER BY 1 LIMIT 1`, old.Name).Scan(&used)
		if err == nil {
			return ActionsCapacitySettings{}, 0, fmt.Errorf("%w: capacity %q is still used by %s; move that pool off it first", ErrConflict, old.Name, used)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ActionsCapacitySettings{}, 0, err
		}
	}
	next := ActionsCapacitySettings{ExternalID: current.ExternalID, Capacities: capacities}
	if err = tx.QueryRow(ctx, "UPDATE installation_settings SET value=$1,revision=revision+1,updated_at=now() WHERE name='actions_capacity' RETURNING revision", JSON(next)).Scan(&rev); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	summary := []map[string]any{}
	for _, c := range capacities {
		summary = append(summary, map[string]any{"name": c.Name, "cloud": c.Cloud, "region": c.Region, "role_arn": c.RoleARN, "spot": c.Spot, "max_instances": c.MaxInstances, "image": c.Image, "warm_pool": c.WarmPool, "environments": c.Environments})
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'settings.actions_capacity','installation',$3)", p.ID, p.KeyID, JSON(map[string]any{"revision": rev, "capacities": summary})); err != nil {
		return ActionsCapacitySettings{}, 0, err
	}
	return next, rev, tx.Commit(ctx)
}

// requireActionsCapacityTx admits active VM pools only on a capacity that
// exists and allows the release's project and environment.
func requireActionsCapacityTx(ctx context.Context, tx pgx.Tx, project, env string, next spec.Application) error {
	var settings *ActionsCapacitySettings
	for _, name := range spec.Names(next) {
		svc := next.Services[name]
		if svc.Actions == nil || svc.Actions.Capacity == "" || svc.Suspended || svc.Replicas == 0 {
			continue
		}
		if settings == nil {
			v, _, err := actionsCapacitySettings(ctx, tx)
			if err != nil {
				return err
			}
			settings = &v
		}
		if c, ok := settings.Find(svc.Actions.Capacity); !ok || !c.Allows(project, env) {
			return fmt.Errorf("%w: services.%s.actions.capacity: %q is not an Actions capacity available to %s/%s", ErrInput, name, svc.Actions.Capacity, project, env)
		}
	}
	return nil
}

// ActionsCapacity reads one capacity for the runtime, without creating the
// setting the way the administration read does.
func (s *Store) ActionsCapacity(ctx context.Context, name string) (ActionsCapacity, bool, error) {
	v, _, err := actionsCapacitySettings(ctx, s.Pool)
	if err != nil {
		return ActionsCapacity{}, false, err
	}
	c, ok := v.Find(name)
	return c, ok, nil
}

// InstallationID tags every cloud resource this installation creates.
func (s *Store) InstallationID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, "SELECT installation_id FROM installation_license WHERE singleton").Scan(&id)
	return id, err
}
