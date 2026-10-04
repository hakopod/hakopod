package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/jackc/pgx/v5"
)

const SlackFeature = "slack_notifications"

const maxSlackEvents = 64

type SlackIntegration struct {
	TeamID         string   `json:"team_id"`
	TeamName       string   `json:"team_name"`
	ChannelID      string   `json:"channel_id"`
	ChannelName    string   `json:"channel_name"`
	ChannelPrivate bool     `json:"channel_private"`
	Events         []string `json:"events"`
	// Revision is allocated from the installation-wide generation counter. It
	// therefore never restarts when a disconnected integration is recreated.
	Revision     int64     `json:"revision"`
	ConnectedAt  time.Time `json:"connected_at"`
	ClientConfig []byte    `json:"-"`
	BotConfig    []byte    `json:"-"`
}

const slackColumns = "team_id,team_name,channel_id,channel_name,channel_private,events,revision,created_at,client_configuration,bot_configuration"

func scanSlack(row scanner) (SlackIntegration, error) {
	var value SlackIntegration
	err := row.Scan(&value.TeamID, &value.TeamName, &value.ChannelID, &value.ChannelName, &value.ChannelPrivate, &value.Events, &value.Revision, &value.ConnectedAt, &value.ClientConfig, &value.BotConfig)
	return value, err
}

func validSlackEvents(events []string) bool {
	if len(events) < 1 || len(events) > maxSlackEvents {
		return false
	}
	seen := map[string]bool{}
	for _, event := range events {
		if seen[event] || !slackevents.Selectable(event) {
			return false
		}
		seen[event] = true
	}
	return true
}

func (s *Store) SlackIntegration(ctx context.Context) (SlackIntegration, error) {
	return scanSlack(s.Pool.QueryRow(ctx, "SELECT "+slackColumns+" FROM slack_integration WHERE singleton"))
}

func (s *Store) SlackIntegrationGeneration(ctx context.Context) (int64, error) {
	var generation int64
	err := s.Pool.QueryRow(ctx, "SELECT generation FROM slack_integration_generation WHERE singleton").Scan(&generation)
	return generation, err
}

// nextSlackGeneration locks the monotonic installation fence before changing
// the integration. OAuth callbacks carry the generation they observed when the
// browser flow started, so an older callback cannot recreate or replace a newer
// connection after any connect, edit, or disconnect.
func nextSlackGeneration(ctx context.Context, tx pgx.Tx, expected int64) (int64, error) {
	var generation int64
	if err := tx.QueryRow(ctx, "SELECT generation FROM slack_integration_generation WHERE singleton FOR UPDATE").Scan(&generation); err != nil {
		return 0, err
	}
	if expected < 0 || generation != expected {
		return 0, ErrConflict
	}
	if err := tx.QueryRow(ctx, "UPDATE slack_integration_generation SET generation=generation+1 WHERE singleton RETURNING generation").Scan(&generation); err != nil {
		return 0, err
	}
	return generation, nil
}

func (s *Store) SaveSlackIntegration(ctx context.Context, p Principal, value SlackIntegration, expectedGeneration int64) (SlackIntegration, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() || value.TeamID == "" || len(value.TeamID) > 64 || strings.TrimSpace(value.TeamName) == "" || len(value.TeamName) > 200 || !validSlackEvents(value.Events) || len(value.ClientConfig) == 0 || len(value.BotConfig) == 0 || expectedGeneration < 0 {
		return SlackIntegration{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SlackIntegration{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, SlackFeature); err != nil {
		return SlackIntegration{}, err
	}
	revision, err := nextSlackGeneration(ctx, tx, expectedGeneration)
	if err != nil {
		return SlackIntegration{}, err
	}
	value, err = scanSlack(tx.QueryRow(ctx, `INSERT INTO slack_integration(singleton,client_configuration,bot_configuration,team_id,team_name,channel_id,channel_name,channel_private,events,revision,connected_by)
 VALUES(true,$1,$2,$3,$4,'','',false,$5,$6,$7)
 ON CONFLICT(singleton) DO UPDATE SET client_configuration=EXCLUDED.client_configuration,bot_configuration=EXCLUDED.bot_configuration,team_id=EXCLUDED.team_id,team_name=EXCLUDED.team_name,channel_id='',channel_name='',channel_private=false,events=EXCLUDED.events,revision=EXCLUDED.revision,connected_by=EXCLUDED.connected_by,updated_at=now()
 RETURNING `+slackColumns, value.ClientConfig, value.BotConfig, value.TeamID, value.TeamName, value.Events, revision, p.ID))
	if err != nil {
		return SlackIntegration{}, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'slack.connect','installation',$3)", p.ID, p.KeyID, JSON(map[string]string{"team_id": value.TeamID}))
	if err != nil {
		return SlackIntegration{}, err
	}
	return value, tx.Commit(ctx)
}

func (s *Store) SetSlackChannel(ctx context.Context, p Principal, id, name string, private bool, events []string, revision int64) (SlackIntegration, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() || id == "" || len(id) > 64 || strings.TrimSpace(name) == "" || len(name) > 200 || !validSlackEvents(events) || revision < 1 {
		return SlackIntegration{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SlackIntegration{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, SlackFeature); err != nil {
		return SlackIntegration{}, err
	}
	nextRevision, err := nextSlackGeneration(ctx, tx, revision)
	if err != nil {
		return SlackIntegration{}, err
	}
	value, err := scanSlack(tx.QueryRow(ctx, "UPDATE slack_integration SET channel_id=$1,channel_name=$2,channel_private=$3,events=$4,revision=$5,updated_at=now() WHERE singleton AND revision=$6 RETURNING "+slackColumns, id, name, private, events, nextRevision, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return SlackIntegration{}, ErrConflict
	}
	if err != nil {
		return SlackIntegration{}, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'slack.channel.configure','installation',$3)", p.ID, p.KeyID, JSON(map[string]any{"channel_id": id, "events": events}))
	if err != nil {
		return SlackIntegration{}, err
	}
	return value, tx.Commit(ctx)
}

func (s *Store) SetSlackEvents(ctx context.Context, p Principal, events []string, revision int64) (SlackIntegration, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() || !validSlackEvents(events) || revision < 1 {
		return SlackIntegration{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SlackIntegration{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, SlackFeature); err != nil {
		return SlackIntegration{}, err
	}
	nextRevision, err := nextSlackGeneration(ctx, tx, revision)
	if err != nil {
		return SlackIntegration{}, err
	}
	value, err := scanSlack(tx.QueryRow(ctx, "UPDATE slack_integration SET events=$1,revision=$2,updated_at=now() WHERE singleton AND revision=$3 RETURNING "+slackColumns, events, nextRevision, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return SlackIntegration{}, ErrConflict
	}
	if err != nil {
		return SlackIntegration{}, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'slack.events.configure','installation',$3)", p.ID, p.KeyID, JSON(map[string]any{"events": events}))
	if err != nil {
		return SlackIntegration{}, err
	}
	return value, tx.Commit(ctx)
}

func (s *Store) DeleteSlackIntegration(ctx context.Context, p Principal, revision int64) error {
	if p.CredentialType != "browser" || !p.IsAdmin() || revision < 1 {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = nextSlackGeneration(ctx, tx, revision); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, "DELETE FROM slack_integration WHERE singleton AND revision=$1", revision)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE slack_event_outbox SET status='skipped',finished_at=now(),last_error='Slack integration was disconnected.' WHERE delivery_target='self_hosted' AND status IN ('pending','sending')"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'slack.disconnect','installation')", p.ID, p.KeyID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type SlackDelivery struct {
	ID, Event, LeaseID  string
	Sequence            int64
	IntegrationRevision int64
	Payload             []byte
	Attempts            int
	CreatedAt           time.Time
}

type SlackDeliveryHistory struct {
	ID         int64      `json:"id"`
	Event      string     `json:"event"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (s *Store) SlackDeliveryHistory(ctx context.Context, p Principal, before int64, limit int) ([]SlackDeliveryHistory, int64, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() || before < 0 || limit < 1 || limit > 100 {
		return nil, 0, ErrInput
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,event_kind,status,attempts,last_error,created_at,finished_at FROM slack_event_outbox WHERE delivery_target='self_hosted' AND ($1=0 OR id<$1) ORDER BY id DESC LIMIT $2`, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []SlackDeliveryHistory{}
	var next int64
	for rows.Next() {
		var item SlackDeliveryHistory
		if err = rows.Scan(&item.ID, &item.Event, &item.Status, &item.Attempts, &item.LastError, &item.CreatedAt, &item.FinishedAt); err != nil {
			return nil, 0, err
		}
		if len(out) == limit {
			// `before` is exclusive, so the next page begins below the last
			// returned item. Using this extra row's ID would skip it.
			next = out[len(out)-1].ID
			break
		}
		out = append(out, item)
	}
	return out, next, rows.Err()
}

func (s *Store) QueueSlackTest(ctx context.Context, p Principal, revision int64) (int64, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() || revision < 1 {
		return 0, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, SlackFeature); err != nil {
		return 0, err
	}
	var channel string
	if err = tx.QueryRow(ctx, "SELECT channel_id FROM slack_integration WHERE singleton AND revision=$1 FOR SHARE", revision).Scan(&channel); errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	if channel == "" {
		return 0, fmt.Errorf("%w: choose a Slack channel before sending a test", ErrInput)
	}
	var source int64
	if err = tx.QueryRow(ctx, "INSERT INTO slack_test_requests(requested_by) VALUES($1) RETURNING id", p.ID).Scan(&source); err != nil {
		return 0, err
	}
	var id int64
	if err = tx.QueryRow(ctx, "INSERT INTO slack_event_outbox(integration_revision,event_kind,source_id,payload) VALUES($1,'test',$2,$3) RETURNING id", revision, source, JSON(map[string]any{"schema_version": 1, "event_id": source, "occurred_at": time.Now().UTC()})).Scan(&id); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'slack.test','installation')", p.ID, p.KeyID); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) ClaimSlackDelivery(ctx context.Context) (*SlackDelivery, error) {
	job := &SlackDelivery{LeaseID: NewID()}
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM slack_event_outbox WHERE delivery_target='self_hosted' AND next_attempt_at<=now() AND attempts<8 AND created_at>now()-interval '24 hours' AND (status='pending' OR status='sending' AND lease_until<now()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE slack_event_outbox d SET status='sending',attempts=d.attempts+1,lease_id=$1,lease_until=now()+interval '45 seconds' FROM candidate c WHERE d.id=c.id RETURNING d.id::text,d.id,d.integration_revision,d.event_kind,d.payload,d.attempts,d.created_at`, job.LeaseID).Scan(&job.ID, &job.Sequence, &job.IntegrationRevision, &job.Event, &job.Payload, &job.Attempts, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) PruneSlackDeliveries(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE slack_event_outbox SET status='failed',finished_at=now(),last_error='Delivery deadline or retry limit reached.',lease_id='',lease_until=NULL WHERE id IN (SELECT id FROM slack_event_outbox WHERE (status='pending' AND (attempts>=8 OR created_at<now()-interval '24 hours')) OR (status='sending' AND lease_until<now() AND (attempts>=8 OR created_at<now()-interval '24 hours')) ORDER BY id LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, "DELETE FROM slack_event_outbox WHERE status IN ('sent','failed','skipped') AND finished_at<now()-interval '30 days' AND id IN (SELECT id FROM slack_event_outbox WHERE status IN ('sent','failed','skipped') AND finished_at<now()-interval '30 days' ORDER BY id LIMIT 100)")
	return err
}

type SlackCloudDelivery struct {
	SourceEventID  int64
	Kind, SourceID string
	Payload        []byte
	Sequence       int64
	LeaseID        string
	Attempts       int
	CreatedAt      time.Time
}

type SlackCloudEventSource struct {
	ID          string
	Project     string
	Environment string
}

func (s *Store) SlackCloudEventSource(ctx context.Context) (SlackCloudEventSource, error) {
	var source SlackCloudEventSource
	err := s.Pool.QueryRow(ctx, "SELECT source_id,project,environment FROM slack_cloud_event_source WHERE singleton").Scan(&source.ID, &source.Project, &source.Environment)
	return source, err
}

func validSlackCloudSource(source, project, environment string) bool {
	return len(source) > 0 && len(source) <= 128 && strings.IndexFunc(source, unicode.IsControl) < 0 && project != "" && environment != "" && len(project) <= 80 && len(environment) <= 80 && strings.IndexFunc(project+environment, unicode.IsControl) < 0
}

func (s *Store) ConfigureSlackCloudRelay(ctx context.Context, source, project, environment string) error {
	if !validSlackCloudSource(source, project, environment) {
		return ErrInput
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, environment).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrInput
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO slack_cloud_event_source(singleton,source_id,project,environment) VALUES(true,$1,$2,$3) ON CONFLICT(singleton) DO UPDATE SET source_id=EXCLUDED.source_id,project=EXCLUDED.project,environment=EXCLUDED.environment", source, project, environment)
	return err
}

// ConfigureSlackCloudEvents is only for the embedded shared Cloud runtime,
// whose sink receives all source-bound events in-process rather than through a
// scoped relay key.
func (s *Store) ConfigureSlackCloudEvents(ctx context.Context, source string) error {
	if source == "" {
		_, err := s.Pool.Exec(ctx, "DELETE FROM slack_cloud_event_source WHERE singleton")
		return err
	}
	if len(source) > 128 || strings.IndexFunc(source, unicode.IsControl) >= 0 {
		return ErrInput
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO slack_cloud_event_source(singleton,source_id,project,environment) VALUES(true,$1,'','') ON CONFLICT(singleton) DO UPDATE SET source_id=EXCLUDED.source_id,project='',environment=''", source)
	return err
}

func (s *Store) ClaimSlackCloudEvent(ctx context.Context) (*SlackCloudDelivery, error) {
	return s.claimSlackCloudEvent(ctx, "")
}

// ClaimSlackCloudEventForSource limits a BYO relay to its configured Cloud
// source. The source is resolved from trusted server configuration, never a
// browser or agent-provided workspace field.
func (s *Store) ClaimSlackCloudEventForSource(ctx context.Context, source SlackCloudEventSource) (*SlackCloudDelivery, error) {
	if !validSlackCloudSource(source.ID, source.Project, source.Environment) {
		return nil, ErrInput
	}
	return s.claimSlackCloudEventForScope(ctx, source)
}
func (s *Store) claimSlackCloudEvent(ctx context.Context, source string) (*SlackCloudDelivery, error) {
	job := &SlackCloudDelivery{LeaseID: NewID()}
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM slack_event_outbox WHERE delivery_target='cloud' AND ($2='' OR runtime_source_id=$2) AND next_attempt_at<=now() AND attempts<8 AND created_at>now()-interval '24 hours' AND (status='pending' OR status='sending' AND lease_until<now()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE slack_event_outbox d SET status='sending',attempts=d.attempts+1,lease_id=$1,lease_until=now()+interval '45 seconds' FROM candidate c WHERE d.id=c.id RETURNING d.id,d.runtime_source_id,d.event_kind,d.source_id,d.payload,d.attempts,d.created_at`, job.LeaseID, source).Scan(&job.Sequence, &job.SourceID, &job.Kind, &job.SourceEventID, &job.Payload, &job.Attempts, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}
func (s *Store) claimSlackCloudEventForScope(ctx context.Context, source SlackCloudEventSource) (*SlackCloudDelivery, error) {
	job := &SlackCloudDelivery{LeaseID: NewID()}
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM slack_event_outbox WHERE delivery_target='cloud' AND runtime_source_id=$2 AND payload->>'project'=$3 AND payload->>'environment'=$4 AND next_attempt_at<=now() AND attempts<8 AND created_at>now()-interval '24 hours' AND (status='pending' OR status='sending' AND lease_until<now()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE slack_event_outbox d SET status='sending',attempts=d.attempts+1,lease_id=$1,lease_until=now()+interval '45 seconds' FROM candidate c WHERE d.id=c.id RETURNING d.id,d.runtime_source_id,d.event_kind,d.source_id,d.payload,d.attempts,d.created_at`, job.LeaseID, source.ID, source.Project, source.Environment).Scan(&job.Sequence, &job.SourceID, &job.Kind, &job.SourceEventID, &job.Payload, &job.Attempts, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) FinishSlackCloudEvent(ctx context.Context, job SlackCloudDelivery, outcome, message string) error {
	if !slices.Contains([]string{"sent", "failed", "skipped", "pending"}, outcome) {
		return ErrInput
	}
	if outcome == "pending" && (job.Attempts >= 8 || time.Since(job.CreatedAt) > 24*time.Hour) {
		outcome = "failed"
		message = "Cloud event delivery stopped after the retry limit or deadline."
	}
	delay := time.Duration(30*(1<<min(max(job.Attempts-1, 0), 6))) * time.Second
	result, err := s.Pool.Exec(ctx, `UPDATE slack_event_outbox SET status=$3,last_error=$4,next_attempt_at=now()+$5::interval,finished_at=CASE WHEN $3='pending' THEN NULL ELSE now() END,lease_id='',lease_until=NULL WHERE id=$1 AND lease_id=$2 AND status='sending' AND lease_until>now()`, job.Sequence, job.LeaseID, outcome, message, fmt.Sprintf("%d seconds", int(delay.Seconds())))
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// FinishSlackCloudEventForSource prevents a relay bound to one Cloud source from
// acknowledging a lease belonging to any other source.
func (s *Store) FinishSlackCloudEventForSource(ctx context.Context, source SlackCloudEventSource, job SlackCloudDelivery, outcome, message string) error {
	if !validSlackCloudSource(source.ID, source.Project, source.Environment) || job.SourceID != source.ID {
		return ErrForbidden
	}
	if !slices.Contains([]string{"sent", "failed", "skipped", "pending"}, outcome) {
		return ErrInput
	}
	if outcome == "pending" && (job.Attempts >= 8 || time.Since(job.CreatedAt) > 24*time.Hour) {
		outcome = "failed"
		message = "Cloud event delivery stopped after the retry limit or deadline."
	}
	delay := time.Duration(30*(1<<min(max(job.Attempts-1, 0), 6))) * time.Second
	result, err := s.Pool.Exec(ctx, `UPDATE slack_event_outbox SET status=$3,last_error=$4,next_attempt_at=now()+$5::interval,finished_at=CASE WHEN $3='pending' THEN NULL ELSE now() END,lease_id='',lease_until=NULL WHERE id=$1 AND lease_id=$2 AND runtime_source_id=$6 AND payload->>'project'=$7 AND payload->>'environment'=$8 AND status='sending' AND lease_until>now()`, job.Sequence, job.LeaseID, outcome, message, fmt.Sprintf("%d seconds", int(delay.Seconds())), source.ID, source.Project, source.Environment)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

func (s *Store) FinishSlackDelivery(ctx context.Context, job SlackDelivery, outcome, message string, retryAfter time.Duration) error {
	if !slices.Contains([]string{"sent", "failed", "skipped", "pending"}, outcome) {
		return ErrInput
	}
	if outcome == "pending" && (job.Attempts >= 8 || time.Since(job.CreatedAt) > 24*time.Hour) {
		outcome = "failed"
		message = "Delivery stopped after the retry limit or delivery deadline."
	}
	delay := time.Duration(30*(1<<min(max(job.Attempts-1, 0), 6))) * time.Second
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	result, err := s.Pool.Exec(ctx, `UPDATE slack_event_outbox SET status=$3,last_error=$4,next_attempt_at=now()+$5::interval,finished_at=CASE WHEN $3='pending' THEN NULL ELSE now() END,lease_id='',lease_until=NULL WHERE id=$1 AND lease_id=$2 AND status='sending' AND lease_until>now()`, job.Sequence, job.LeaseID, outcome, message, fmt.Sprintf("%d seconds", int(delay.Seconds())))
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// AckSlackCloudEventLease loads server-owned retry state before acknowledging a
// BYO relay receipt. The relay supplies no retry counters, payload, source, or
// workspace identity that could widen its authority.
func (s *Store) AckSlackCloudEventLease(ctx context.Context, source SlackCloudEventSource, sequence int64, leaseID, outcome, message string) error {
	if sequence < 1 || len(leaseID) != 32 || !validSlackCloudSource(source.ID, source.Project, source.Environment) {
		return ErrInput
	}
	job := SlackCloudDelivery{Sequence: sequence, LeaseID: leaseID, SourceID: source.ID}
	if err := s.Pool.QueryRow(ctx, `SELECT attempts,created_at FROM slack_event_outbox WHERE id=$1 AND runtime_source_id=$2 AND payload->>'project'=$4 AND payload->>'environment'=$5 AND lease_id=$3 AND status='sending' AND lease_until>now()`, sequence, source.ID, leaseID, source.Project, source.Environment).Scan(&job.Attempts, &job.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	return s.FinishSlackCloudEventForSource(ctx, source, job, outcome, message)
}
