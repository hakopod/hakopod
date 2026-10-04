package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) RunSlack(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for {
		s.deliverSlack(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) deliverSlack(parent context.Context, client *http.Client) {
	prune, cancel := context.WithTimeout(parent, 3*time.Second)
	_ = s.Store.PruneSlackDeliveries(prune)
	cancel()
	for i := 0; i < 2 && parent.Err() == nil; i++ {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		job, err := s.Store.ClaimSlackDelivery(ctx)
		if err != nil || job == nil {
			cancel()
			return
		}
		outcome, message, retry := s.sendSlack(ctx, client, *job)
		cancel()
		finish, stop := context.WithTimeout(parent, 3*time.Second)
		_ = s.Store.FinishSlackDelivery(finish, *job, outcome, message, retry)
		stop()
	}
}

func (s *Server) sendSlack(ctx context.Context, client *http.Client, job store.SlackDelivery) (string, string, time.Duration) {
	item, err := s.Store.SlackIntegration(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "skipped", "Slack integration was disconnected.", 0
		}
		return "pending", "Slack settings are temporarily unavailable; retry scheduled.", 0
	}
	if err = s.Store.RequireFeatures(ctx, store.SlackFeature); err != nil {
		if errors.Is(err, store.ErrLicenseRequired) {
			return "skipped", "Slack Pro entitlement is unavailable.", 0
		}
		return "pending", "Slack entitlement could not be checked; retry scheduled.", 0
	}
	if item.Revision != job.IntegrationRevision {
		return "skipped", "Slack integration settings changed.", 0
	}
	if job.Event != "test" && !slices.Contains(item.Events, job.Event) {
		return "skipped", "This event is no longer selected.", 0
	}
	if item.ChannelID == "" {
		return "skipped", "Slack channel is not configured.", 0
	}
	token, err := s.slackToken(item)
	if err != nil {
		return "pending", "Slack credentials are unavailable; retry scheduled.", 0
	}
	text := slackDeliveryText(job.Event, job.Payload)
	if text == "" {
		return "failed", "Invalid Slack event payload.", 0
	}
	return postSlackMessage(ctx, client, token, item.ChannelID, text)
}

// postSlackMessage is deliberately transport-only: authorization, entitlement,
// revision and event selection are checked before this call. Keeping the Slack
// protocol boundary small lets the worker be tested without a real workspace.
func postSlackMessage(ctx context.Context, client *http.Client, token, channel, text string) (string, string, time.Duration) {
	body := store.JSON(map[string]any{"channel": channel, "text": text, "mrkdwn": false, "parse": "none", "unfurl_links": false, "unfurl_media": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", strings.NewReader(string(body)))
	if err != nil {
		return "failed", "Slack request is invalid.", 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hakopod-Slack/1")
	res, err := client.Do(req)
	if err != nil {
		return "pending", "Slack connection failed; retry scheduled.", 0
	}
	defer res.Body.Close()
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&response)
	retry := slackRetryAfter(res.Header.Get("Retry-After"))
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return "pending", "Slack is temporarily unavailable; retry scheduled.", retry
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || decodeErr != nil {
		return "failed", "Slack rejected the delivery. Check the integration settings.", 0
	}
	if response.OK {
		return "sent", "", 0
	}
	if response.Error == "ratelimited" || response.Error == "rate_limited" || response.Error == "internal_error" {
		return "pending", "Slack is temporarily unavailable; retry scheduled.", retry
	}
	return "failed", "Slack rejected the delivery. Check the integration settings.", 0
}

func slackRetryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 {
		return 0
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func slackDeliveryText(event string, payload []byte) string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	switch event {
	case "alarm.opened":
		return slackAlarmText("Hakopod alarm opened", value)
	case "alarm.resolved":
		return slackAlarmText("Hakopod alarm resolved", value)
	case "audit":
		return slackText("Hakopod audit event", value["action"], value["resource"])
	case "test":
		return "Hakopod Slack test message."
	}
	definition, ok := slackevents.Definition(event)
	if !ok {
		return ""
	}
	lines := []string{"Hakopod " + definition.Label}
	lines = slackAppendField(lines, value, "Project", "project")
	lines = slackAppendField(lines, value, "Environment", "environment")
	lines = slackAppendField(lines, value, "Application", "application_name")
	lines = slackAppendField(lines, value, "Application ID", "application_id")
	lines = slackAppendField(lines, value, "Service", "service")
	lines = slackAppendField(lines, value, "Deployment", "deployment_id")
	lines = slackAppendField(lines, value, "Revision", "revision")
	if phase, ok := value["runtime_phase"].(string); ok && (phase == "deployment" || phase == "recovery") {
		lines = append(lines, "Runtime phase: "+phase)
		if phase == "recovery" {
			lines = slackAppendField(lines, value, "Recovery revision", "recovery_revision")
		}
	}
	return strings.Join(lines, "\n")
}

func slackAlarmText(title string, value map[string]any) string {
	lines := []string{title}
	for _, field := range []struct{ label, key string }{
		{"Project", "project"},
		{"Environment", "environment"},
		{"Application ID", "application_id"},
		{"Resource", "resource_name"},
		{"Alarm ID", "alarm_id"},
	} {
		lines = slackAppendField(lines, value, field.label, field.key)
	}
	return strings.Join(lines, "\n")
}

func slackAppendField(lines []string, value map[string]any, label, key string) []string {
	if text, ok := value[key].(string); ok && text != "" {
		return append(lines, label+": "+slackSafeText(text))
	}
	if number, ok := value[key].(float64); ok && number >= 0 && number <= 1_000_000_000 && math.Trunc(number) == number {
		return append(lines, label+": "+strconv.FormatInt(int64(number), 10))
	}
	return lines
}
func slackText(title string, values ...any) string {
	var lines []string
	lines = append(lines, title)
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			lines = append(lines, slackSafeText(text))
		}
	}
	return strings.Join(lines, "\n")
}
func slackSafeText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
	if len(value) > 2500 {
		return value[:2500] + "..."
	}
	return value
}
