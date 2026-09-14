package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) registerAlarmRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alarms", s.alarms)
	mux.HandleFunc("GET /api/v1/alarm-settings", s.alarmSettings)
	mux.HandleFunc("PUT /api/v1/alarm-settings", s.putAlarmSettings)
	mux.HandleFunc("POST /api/v1/alarms/{id}/acknowledge", s.acknowledgeAlarm)
	mux.HandleFunc("POST /api/v1/alarms/{id}/read", s.readAlarm)
}

func alarmRequestScope(r *http.Request) (store.AlarmScope, error) {
	q := r.URL.Query()
	scope := store.AlarmScope{Project: q.Get("project"), Environment: q.Get("environment"), ApplicationID: q.Get("application_id")}
	if (scope.Project != "" && !slug.MatchString(scope.Project)) || (scope.Environment != "" && !slug.MatchString(scope.Environment)) || len(scope.ApplicationID) > 64 {
		return scope, fmt.Errorf("%w: invalid alarm scope", store.ErrInput)
	}
	if scope.Project == "" && (scope.Environment != "" || scope.ApplicationID != "") {
		return scope, fmt.Errorf("%w: project is required for an environment or application filter", store.ErrInput)
	}
	return scope, nil
}

func (s *Server) alarms(w http.ResponseWriter, r *http.Request) {
	scope, err := alarmRequestScope(r)
	if err != nil {
		failure(w, err)
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			failure(w, fmt.Errorf("%w: limit must be 1–50", store.ErrInput))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.Store.Alarms(ctx, who(r), scope, r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) alarmEmailAvailable(ctx context.Context) bool {
	return s.smtpMailAvailable(ctx)
}

func (s *Server) alarmSettings(w http.ResponseWriter, r *http.Request) {
	scope, err := alarmRequestScope(r)
	if err != nil {
		failure(w, err)
		return
	}
	settings, err := s.Store.AlarmSettings(r.Context(), who(r), scope)
	if err != nil {
		failure(w, err)
		return
	}
	settings.EmailAvailable = s.alarmEmailAvailable(r.Context())
	write(w, 200, settings)
}

func (s *Server) putAlarmSettings(w http.ResponseWriter, r *http.Request) {
	scope, err := alarmRequestScope(r)
	if err != nil {
		failure(w, err)
		return
	}
	var input struct {
		Enabled          *bool  `json:"enabled"`
		HoldSeconds      *int   `json:"hold_seconds"`
		EmailEnabled     *bool  `json:"email_enabled"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil || input.HoldSeconds == nil || input.EmailEnabled == nil || input.ExpectedRevision == nil {
		failure(w, fmt.Errorf("%w: enabled, hold_seconds, email_enabled and expected_revision are required", store.ErrInput))
		return
	}
	// Check authority before disclosing transport readiness or accepting a toggle.
	if err = s.Store.ValidateAlarmSettingsScope(r.Context(), who(r), scope, true); err != nil {
		failure(w, err)
		return
	}
	if *input.EmailEnabled && !s.alarmEmailAvailable(r.Context()) {
		failure(w, fmt.Errorf("%w: email delivery is unavailable; an installation administrator must configure and enable SMTP delivery first", store.ErrInput))
		return
	}
	settings, err := s.Store.PutAlarmSettings(r.Context(), who(r), scope, store.AlarmSettingsInput{Enabled: *input.Enabled, HoldSeconds: *input.HoldSeconds, EmailEnabled: *input.EmailEnabled, ExpectedRevision: *input.ExpectedRevision})
	if err != nil {
		failure(w, err)
		return
	}
	settings.EmailAvailable = s.alarmEmailAvailable(r.Context())
	write(w, 200, settings)
}

func (s *Server) acknowledgeAlarm(w http.ResponseWriter, r *http.Request) { s.markAlarm(w, r, true) }
func (s *Server) readAlarm(w http.ResponseWriter, r *http.Request)        { s.markAlarm(w, r, false) }
func (s *Server) markAlarm(w http.ResponseWriter, r *http.Request, acknowledge bool) {
	var input struct {
		ExpectedEventID *int64 `json:"expected_event_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &input) {
		return
	}
	expected := []int64{}
	if input.ExpectedEventID != nil {
		expected = append(expected, *input.ExpectedEventID)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	item, err := s.Store.ReadAlarm(ctx, who(r), r.PathValue("id"), acknowledge, expected...)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, item)
}

// RunAlarms performs bounded direct observations and durable mail work in the
// existing process. There is no informer cache, application goroutine, or
// dependency on an open dashboard. A database lease coordinates API replicas.
func (s *Server) RunAlarms(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.evaluateAlarms(ctx)
		s.deliverAlarmEmails(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateAlarms(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	lease, err := s.Store.ClaimAlarmEvaluation(ctx)
	if err != nil || lease == nil {
		return
	}
	defer func() {
		finish, stop := context.WithTimeout(parent, 3*time.Second)
		defer stop()
		_ = s.Store.AdvanceAlarmEvaluation(finish, lease, false, true)
	}()
	email := s.alarmEmailAvailable(ctx)
	if lease.NodesDue {
		observing, stop := context.WithTimeout(ctx, 5*time.Second)
		var nodes []cluster.AlarmNode
		var err error
		if s.Cluster != nil {
			nodes, err = s.Cluster.AlarmNodes(observing)
		} else {
			err = errors.New("cluster observation unavailable")
		}
		stop()
		if err == nil {
			at := time.Now().UTC()
			current := make([]string, 0, len(nodes))
			for _, node := range nodes {
				current = append(current, node.Name)
				if err = s.Store.ApplyAlarmObservations(ctx, alarmNodeObservations(node, at), email); err != nil {
					return
				}
			}
			if err = s.Store.RemovedAlarmResources(ctx, "node", "", 0, current, at, email); err != nil {
				return
			}
		} else if err = s.Store.UnknownNodeAlarms(ctx, time.Now().UTC()); err != nil {
			return
		}
		if err = s.Store.AdvanceAlarmEvaluation(ctx, lease, true, false); err != nil {
			return
		}
	}
	for n := 0; n < 25 && ctx.Err() == nil; n++ {
		app, err := s.Store.NextAlarmApplication(ctx, lease.Cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			lease.Cursor = ""
			return
		}
		if err != nil {
			return
		}
		observation, observationErr := storedAlarmObservation(app, time.Now().UTC())
		at := observation.ObservedAt
		if observationErr != nil {
			at = time.Now().UTC()
		}
		if err = s.Store.ApplyAlarmObservations(ctx, applicationAlarmObservations(app, observation, observationErr, at), email, app.Revision); err != nil {
			return
		}
		if observationErr == nil {
			services := make([]string, 0, len(app.Spec.Services))
			for name := range app.Spec.Services {
				services = append(services, app.ID+"/"+name)
			}
			if err = s.Store.RemovedAlarmResources(ctx, "service", app.ID, app.Revision, services, at, email); err != nil {
				return
			}
		}
		lease.Cursor = app.ID
		if err = s.Store.AdvanceAlarmEvaluation(ctx, lease, false, false); err != nil {
			return
		}
	}
}

func storedAlarmObservation(app store.Application, now time.Time) (cluster.Observation, error) {
	var observation cluster.Observation
	err := json.Unmarshal(app.Observed, &observation)
	if err != nil || observation.ObservedAt.IsZero() || observation.ObservedAt.After(now.Add(5*time.Second)) || now.Sub(observation.ObservedAt) > 120*time.Second || observation.Revision != app.Revision || observation.Status == "unknown" || observation.Status == "" {
		return observation, errors.New("a fresh observation of the current revision is unavailable")
	}
	return observation, nil
}

func applicationAlarmObservations(app store.Application, current cluster.Observation, observationErr error, at time.Time) []store.AlarmObservation {
	scope := store.AlarmScope{Project: app.Project, Environment: app.Environment, ApplicationID: app.ID}
	result := make([]store.AlarmObservation, 0, len(app.Spec.Services)+1)
	known := make(map[string]cluster.ServiceStatus, len(current.Services))
	for _, item := range current.Services {
		known[item.Name] = item
	}
	appHealth := "healthy"
	ready := 0
	firstProblem := ""
	for _, name := range spec.Names(app.Spec) {
		service, found := known[name]
		health := "unknown"
		message := "Current service readiness is unavailable."
		if observationErr == nil && found {
			health = "unhealthy"
			message = fmt.Sprintf("Service %s is not ready (%d/%d replicas).", name, service.Ready, service.Desired)
			if service.Status == "ready" && service.Ready >= service.Desired {
				health = "healthy"
				ready++
				message = fmt.Sprintf("Service %s is ready (%d/%d replicas).", name, service.Ready, service.Desired)
			} else if service.Message != "" {
				message += " " + service.Message
			}
		}
		if health != "healthy" && firstProblem == "" {
			firstProblem = message
		}
		if health == "unknown" {
			appHealth = "unknown"
		} else if health == "unhealthy" && appHealth != "unknown" {
			appHealth = "unhealthy"
		}
		result = append(result, store.AlarmObservation{AlarmScope: scope, Rule: "service-not-ready", ResourceType: "service", ResourceID: app.ID + "/" + name, ResourceName: name, Service: name, Health: health, Summary: message, At: at})
	}
	if len(app.Spec.Services) == 0 || observationErr != nil {
		appHealth = "unknown"
	}
	summary := fmt.Sprintf("Application %s is ready (%d/%d services).", app.Name, ready, len(app.Spec.Services))
	if appHealth == "unhealthy" {
		summary = fmt.Sprintf("Application %s is not ready (%d/%d services). %s", app.Name, ready, len(app.Spec.Services), firstProblem)
	} else if appHealth == "unknown" {
		summary = "Current readiness for application " + app.Name + " is unavailable."
	}
	result = append(result, store.AlarmObservation{AlarmScope: scope, Rule: "application-not-ready", ResourceType: "application", ResourceID: app.ID, ResourceName: app.Name, Health: appHealth, Summary: summary, At: at})
	return result
}

func alarmNodeObservations(node cluster.AlarmNode, at time.Time) []store.AlarmObservation {
	rules := []struct{ condition, rule string }{{"Ready", "node-not-ready"}, {"DiskPressure", "node-disk-pressure"}, {"MemoryPressure", "node-memory-pressure"}, {"PIDPressure", "node-pid-pressure"}}
	result := make([]store.AlarmObservation, 0, 4)
	for _, rule := range rules {
		health, message := "unknown", "Node condition "+rule.condition+" is unavailable."
		for _, condition := range node.Conditions {
			if condition.Type != rule.condition {
				continue
			}
			if rule.condition == "Ready" {
				if condition.Status == "True" {
					health, message = "healthy", "Node "+node.Name+" is ready."
				} else if condition.Status == "False" || condition.Status == "Unknown" {
					health, message = "unhealthy", "Node "+node.Name+" is not ready."
				}
			} else if condition.Status == "True" {
				health, message = "unhealthy", "Node "+node.Name+" reports "+rule.condition+"."
			} else if condition.Status == "False" {
				health, message = "healthy", "Node "+node.Name+" does not report "+rule.condition+"."
			}
			if health == "unhealthy" && strings.Trim(condition.Message, ": ") != "" {
				message += " " + condition.Message
			}
			break
		}
		result = append(result, store.AlarmObservation{Rule: rule.rule, ResourceType: "node", ResourceID: node.Name, ResourceName: node.Name, Health: health, Summary: message, At: at})
	}
	return result
}
