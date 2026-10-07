package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/hakopod/hakopod/internal/store"
	"golang.org/x/oauth2"
)

const slackSettingsPath = "/settings/integrations/slack"

// SlackCloudEventSink receives only committed, source-bound events from the
// trusted shared Cloud runtime. It is never enabled on a BYO node.
type SlackCloudEventSink interface {
	Enqueue(context.Context, SlackCloudEvent) (SlackCloudEventOutcome, error)
}
type SlackCloudEvent struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	SourceID    string          `json:"source_id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Payload     json.RawMessage `json:"payload"`
}
type SlackCloudEventOutcome string

const (
	SlackCloudAccepted SlackCloudEventOutcome = "accepted"
	SlackCloudRetry    SlackCloudEventOutcome = "retry"
	SlackCloudSkipped  SlackCloudEventOutcome = "skipped"
)

type SlackIntegrationStatus struct {
	Mode           string                             `json:"mode"`
	Available      bool                               `json:"available"`
	Configured     bool                               `json:"configured"`
	SetupAvailable bool                               `json:"setup_available"`
	Reason         string                             `json:"reason,omitempty"`
	Team           *SlackTeam                         `json:"team,omitempty"`
	Channel        *SlackChannel                      `json:"channel,omitempty"`
	Events         []string                           `json:"events"`
	EventCatalog   []slackevents.SlackEventDefinition `json:"event_catalog"`
	Revision       int64                              `json:"revision"`
	Manifest       *SlackManifest                     `json:"manifest,omitempty"`
}
type SlackTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SlackChannel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
}
type SlackManifest struct {
	DisplayInformation map[string]string `json:"display_information"`
	OAuthConfig        map[string]any    `json:"oauth_config"`
	Settings           map[string]any    `json:"settings"`
	Features           map[string]any    `json:"features"`
}
type SlackConnectResult struct {
	AuthorizationURL string `json:"authorization_url"`
}
type SlackChannels struct {
	Items      []SlackChannel `json:"items"`
	NextBefore string         `json:"next_before,omitempty"`
}
type SlackChannelInput struct {
	ChannelID        string `json:"channel_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type SlackEventsInput struct {
	Events           []string `json:"events"`
	ExpectedRevision int64    `json:"expected_revision"`
}
type SlackTestResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type slackClientConfiguration struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}
type slackBotConfiguration struct {
	AccessToken string `json:"access_token"`
}
type slackChallenge struct {
	IdentityID    string `json:"identity_id"`
	KeyID         string `json:"key_id"`
	Configuration []byte `json:"configuration"`
	Generation    int64  `json:"generation"`
}

func (s *Server) registerSlackRoutes(public, protected *http.ServeMux) {
	public.HandleFunc("GET /api/v1/integrations/slack/callback", s.slackCallback)
	protected.HandleFunc("GET /api/v1/integrations/slack", s.slackStatus)
	protected.HandleFunc("POST /api/v1/integrations/slack/connect", s.slackConnect)
	protected.HandleFunc("GET /api/v1/integrations/slack/channels", s.slackChannels)
	protected.HandleFunc("GET /api/v1/integrations/slack/deliveries", s.slackDeliveries)
	protected.HandleFunc("PUT /api/v1/integrations/slack/channel", s.slackChannel)
	protected.HandleFunc("PUT /api/v1/integrations/slack/events", s.slackEvents)
	protected.HandleFunc("POST /api/v1/integrations/slack/test", s.slackTest)
	protected.HandleFunc("DELETE /api/v1/integrations/slack", s.slackDisconnect)
	protected.HandleFunc("PUT /api/v1/integrations/slack/cloud-events/source", s.slackCloudBridgeSource)
	protected.HandleFunc("POST /api/v1/integrations/slack/cloud-events/claim", s.slackCloudBridgeClaim)
	protected.HandleFunc("POST /api/v1/integrations/slack/cloud-events/ack", s.slackCloudBridgeAck)
}

func (s *Server) slackStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	value := s.localSlackStatus(r.Context())
	write(w, 200, value)
}

func (s *Server) localSlackStatus(ctx context.Context) SlackIntegrationStatus {
	value := SlackIntegrationStatus{Mode: "self_hosted", Events: []string{}, EventCatalog: slackevents.Catalog()}
	item, err := s.Store.SlackIntegration(ctx)
	if err == nil {
		value.Configured, value.Revision = true, item.Revision
		value.Team = &SlackTeam{ID: item.TeamID, Name: item.TeamName}
		value.Events = append([]string(nil), item.Events...)
		if item.ChannelID != "" {
			value.Channel = &SlackChannel{ID: item.ChannelID, Name: item.ChannelName, IsPrivate: item.ChannelPrivate}
		}
	}
	if len(s.authEncryptionKey()) != 32 {
		value.Reason = "encryption_unavailable"
		return value
	}
	if err := s.Store.RequireFeatures(ctx, store.SlackFeature); err != nil {
		value.Reason = "license_required"
		return value
	}
	value.Available, value.SetupAvailable = true, true
	if !value.Configured {
		value.Manifest = s.slackManifest()
	}
	return value
}

func (s *Server) slackManifest() *SlackManifest {
	redirect, err := s.slackCallbackURL()
	if err != nil {
		return nil
	}
	return &SlackManifest{DisplayInformation: map[string]string{"name": "Hakopod"}, OAuthConfig: map[string]any{"redirect_urls": []string{redirect}, "scopes": map[string]any{"bot": []string{"chat:write", "channels:read", "groups:read"}}}, Settings: map[string]any{"org_deploy_enabled": false, "socket_mode_enabled": false, "token_rotation_enabled": false}, Features: map[string]any{"bot_user": map[string]any{"display_name": "Hakopod", "always_online": false}}}
}

// The dashboard owns the browser session cookie and proxies the callback to
// core with its authenticated server-side credential. Slack must therefore
// redirect to the dashboard path, never directly to the engine API path.
func (s *Server) slackCallbackURL() (string, error) {
	base, err := url.Parse(s.Auth.PublicURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return "", errors.New("invalid public dashboard URL")
	}
	return strings.TrimSuffix(s.Auth.PublicURL, "/") + "/api/integrations/slack/callback", nil
}

func (s *Server) requireSlackAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !installationAdministrator(who(r)) {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}
func (s *Server) requireSlackFeature(w http.ResponseWriter, r *http.Request) bool {
	if err := s.Store.RequireFeatures(r.Context(), store.SlackFeature); err != nil {
		authFailure(w, err)
		return false
	}
	return true
}

func (s *Server) slackConnect(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) || !s.requireSlackAdmin(w, r) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackFeature(w, r) {
		return
	}
	if len(s.authEncryptionKey()) != 32 {
		problem(w, 503, "slack_credentials_unavailable", "Configure the persistent authentication encryption key before connecting Slack.")
		return
	}
	var in slackClientConfiguration
	if !decode(w, r, &in) {
		return
	}
	if len(in.ClientID) < 1 || len(in.ClientID) > 256 || len(in.ClientSecret) < 1 || len(in.ClientSecret) > 4096 || strings.ContainsAny(in.ClientID+in.ClientSecret, "\r\n") {
		problem(w, 400, "invalid_slack_configuration", "Enter a valid Slack client ID and client secret.")
		return
	}
	if _, err := s.slackCallbackURL(); err != nil {
		problem(w, 400, "invalid_slack_configuration", "Configure the public dashboard URL before connecting Slack.")
		return
	}
	ciphertext, err := s.encryptAuth(store.JSON(in))
	if err != nil {
		problem(w, 503, "slack_credentials_unavailable", "Slack credentials cannot be stored securely.")
		return
	}
	generation, err := s.Store.SlackIntegrationGeneration(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	state, err := s.Store.NewChallenge(r.Context(), "slack-oauth", slackChallenge{IdentityID: who(r).ID, KeyID: who(r).KeyID, Configuration: ciphertext, Generation: generation}, 10*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, SlackConnectResult{AuthorizationURL: slackAuthorizationURL(s.slackOAuthConfig(in), state)})
}

func (s *Server) slackOAuthConfig(configuration slackClientConfiguration) oauth2.Config {
	redirect, _ := s.slackCallbackURL()
	return oauth2.Config{ClientID: configuration.ClientID, ClientSecret: configuration.ClientSecret, RedirectURL: redirect, Endpoint: oauth2.Endpoint{AuthURL: "https://slack.com/oauth/v2/authorize", TokenURL: "https://slack.com/api/oauth.v2.access", AuthStyle: oauth2.AuthStyleInHeader}, Scopes: []string{"chat:write", "channels:read", "groups:read"}}
}

func slackAuthorizationURL(config oauth2.Config, state string) string {
	return config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("scope", "chat:write,channels:read,groups:read"))
}

func (s *Server) slackCallback(w http.ResponseWriter, r *http.Request) {
	if s.CloudControlPlane || len(r.URL.Query().Get("state")) > 512 || len(r.URL.Query().Get("code")) > 4096 || r.URL.Query().Get("error") != "" {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	token, err := s.authenticateToken(r)
	if err != nil {
		authFailure(w, err)
		return
	}
	p, err := s.Store.Authenticate(r.Context(), token)
	if err != nil || p.CredentialType != "browser" || !p.IsAdmin() {
		authFailure(w, store.ErrForbidden)
		return
	}
	challenge, err := s.Store.ConsumeChallenge(r.Context(), r.URL.Query().Get("state"), "slack-oauth")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending slackChallenge
	if json.Unmarshal(challenge.Data, &pending) != nil || subtle.ConstantTimeCompare([]byte(pending.IdentityID), []byte(p.ID)) != 1 || subtle.ConstantTimeCompare([]byte(pending.KeyID), []byte(p.KeyID)) != 1 {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	if err = s.Store.RequireFeatures(r.Context(), store.SlackFeature); err != nil {
		authFailure(w, err)
		return
	}
	plain, err := s.decryptAuth(pending.Configuration)
	var configuration slackClientConfiguration
	if err != nil || json.Unmarshal(plain, &configuration) != nil {
		problem(w, 503, "slack_credentials_unavailable", "Saved Slack setup credentials cannot be opened.")
		return
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), oauth2.HTTPClient, client), 15*time.Second)
	defer cancel()
	oauthConfig := s.slackOAuthConfig(configuration)
	oauthToken, err := oauthConfig.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil || oauthToken.AccessToken == "" || oauthToken.Extra("token_type") != "bot" || !slackScopesGranted(oauthToken.Extra("scope")) {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	team, err := s.slackAuthTest(ctx, client, oauthToken.AccessToken)
	if err != nil {
		authFailure(w, err)
		return
	}
	clientCipher, err := s.encryptAuth(store.JSON(configuration))
	if err != nil {
		problem(w, 503, "slack_credentials_unavailable", "Slack credentials cannot be stored securely.")
		return
	}
	botCipher, err := s.encryptAuth(store.JSON(slackBotConfiguration{AccessToken: oauthToken.AccessToken}))
	if err != nil {
		problem(w, 503, "slack_credentials_unavailable", "Slack credentials cannot be stored securely.")
		return
	}
	if _, err = s.Store.SaveSlackIntegration(r.Context(), p, store.SlackIntegration{TeamID: team.ID, TeamName: team.Name, Events: []string{"alarm.opened", "alarm.resolved"}, ClientConfig: clientCipher, BotConfig: botCipher}, pending.Generation); err != nil {
		authFailure(w, err)
		return
	}
	http.Redirect(w, r, slackSettingsPath+"?connected=1", http.StatusSeeOther)
}

func slackScopesGranted(raw any) bool {
	scope, ok := raw.(string)
	if !ok {
		return false
	}
	granted := map[string]bool{}
	for _, item := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		granted[item] = true
	}
	return granted["chat:write"] && granted["channels:read"] && granted["groups:read"]
}

func (s *Server) slackAuthTest(ctx context.Context, client *http.Client, token string) (SlackTeam, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/auth.test", nil)
	if err != nil {
		return SlackTeam{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return SlackTeam{}, store.ErrUnauthorized
	}
	defer res.Body.Close()
	var body struct {
		OK     bool   `json:"ok"`
		TeamID string `json:"team_id"`
		Team   string `json:"team"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body) != nil || !body.OK || body.TeamID == "" || body.Team == "" {
		return SlackTeam{}, store.ErrUnauthorized
	}
	return SlackTeam{ID: body.TeamID, Name: body.Team}, nil
}

func (s *Server) slackChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackFeature(w, r) {
		return
	}
	value, err := s.localSlackChannels(r.Context(), r.URL.Query().Get("before"))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, value)
}
func (s *Server) localSlackChannels(ctx context.Context, cursor string) (SlackChannels, error) {
	if len(cursor) > 1024 {
		return SlackChannels{}, store.ErrInput
	}
	item, err := s.Store.SlackIntegration(ctx)
	if err != nil {
		return SlackChannels{}, err
	}
	token, err := s.slackToken(item)
	if err != nil {
		return SlackChannels{}, store.ErrUnauthorized
	}
	query := url.Values{"types": {"public_channel,private_channel"}, "exclude_archived": {"true"}, "limit": {"100"}, "cursor": {cursor}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/conversations.list?"+query.Encode(), nil)
	if err != nil {
		return SlackChannels{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return SlackChannels{}, store.ErrUnauthorized
	}
	defer res.Body.Close()
	var body struct {
		OK       bool `json:"ok"`
		Channels []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsPrivate bool   `json:"is_private"`
			IsMember  bool   `json:"is_member"`
		} `json:"channels"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body) != nil || !body.OK {
		return SlackChannels{}, store.ErrUnauthorized
	}
	out := SlackChannels{Items: []SlackChannel{}, NextBefore: body.ResponseMetadata.NextCursor}
	for _, channel := range body.Channels {
		if channel.IsMember && channel.ID != "" && channel.Name != "" {
			out.Items = append(out.Items, SlackChannel{ID: channel.ID, Name: channel.Name, IsPrivate: channel.IsPrivate})
		}
	}
	return out, nil
}
func (s *Server) slackToken(item store.SlackIntegration) (string, error) {
	plain, err := s.decryptAuth(item.BotConfig)
	var value slackBotConfiguration
	if err != nil || json.Unmarshal(plain, &value) != nil || len(value.AccessToken) < 1 || len(value.AccessToken) > 8192 {
		return "", errors.New("invalid Slack token")
	}
	return value.AccessToken, nil
}

func (s *Server) slackChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	var in SlackChannelInput
	if !decode(w, r, &in) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackFeature(w, r) {
		return
	}
	item, err := s.Store.SlackIntegration(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	token, err := s.slackToken(item)
	if err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	channel, err := s.slackChannelInfo(r.Context(), token, in.ChannelID)
	if err != nil {
		authFailure(w, err)
		return
	}
	saved, err := s.Store.SetSlackChannel(r.Context(), who(r), channel.ID, channel.Name, channel.IsPrivate, item.Events, in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, s.statusFromSlack(saved))
}
func (s *Server) slackChannelInfo(ctx context.Context, token, id string) (SlackChannel, error) {
	if len(id) < 1 || len(id) > 64 {
		return SlackChannel{}, store.ErrInput
	}
	q := url.Values{"channel": {id}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/conversations.info?"+q.Encode(), nil)
	if err != nil {
		return SlackChannel{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return SlackChannel{}, store.ErrUnauthorized
	}
	defer res.Body.Close()
	var body struct {
		OK      bool `json:"ok"`
		Channel struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsPrivate bool   `json:"is_private"`
			IsMember  bool   `json:"is_member"`
		} `json:"channel"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body) != nil || !body.OK || !body.Channel.IsMember || body.Channel.ID == "" || body.Channel.Name == "" {
		return SlackChannel{}, store.ErrForbidden
	}
	return SlackChannel{ID: body.Channel.ID, Name: body.Channel.Name, IsPrivate: body.Channel.IsPrivate}, nil
}
func (s *Server) statusFromSlack(item store.SlackIntegration) SlackIntegrationStatus {
	value := SlackIntegrationStatus{Mode: "self_hosted", Available: true, Configured: true, SetupAvailable: true, Team: &SlackTeam{ID: item.TeamID, Name: item.TeamName}, Events: append([]string(nil), item.Events...), EventCatalog: slackevents.Catalog(), Revision: item.Revision}
	if item.ChannelID != "" {
		value.Channel = &SlackChannel{ID: item.ChannelID, Name: item.ChannelName, IsPrivate: item.ChannelPrivate}
	}
	return value
}
func (s *Server) slackEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	var in SlackEventsInput
	if !decode(w, r, &in) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackFeature(w, r) {
		return
	}
	value, err := s.Store.SetSlackEvents(r.Context(), who(r), in.Events, in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, s.statusFromSlack(value))
}
func (s *Server) slackDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackAdmin(w, r) {
		return
	}
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		if _, err = fmt.Sscan(raw, &before); err != nil || before < 1 {
			authFailure(w, store.ErrInput)
			return
		}
	}
	items, next, err := s.Store.SlackDeliveryHistory(r.Context(), who(r), before, 25)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "next_before": next})
}
func (s *Server) slackTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if !s.requireSlackFeature(w, r) {
		return
	}
	id, err := s.Store.QueueSlackTest(r.Context(), who(r), in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, SlackTestResult{ID: fmt.Sprintf("%d", id), Status: "pending"})
}
func (s *Server) slackDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackAdmin(w, r) {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.CloudControlPlane {
		problem(w, 404, "not_found", "Cloud manages Slack integrations through its control plane.")
		return
	}
	if err := s.Store.DeleteSlackIntegration(r.Context(), who(r), in.ExpectedRevision); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
