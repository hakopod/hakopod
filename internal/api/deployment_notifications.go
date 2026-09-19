package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type notificationSecret struct {
	Purpose       string `json:"purpose"`
	ApplicationID string `json:"application_id"`
	TargetID      string `json:"target_id"`
	Destination   string `json:"destination"`
	SigningSecret string `json:"signing_secret,omitempty"`
}

func (s *Server) registerNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/applications/{id}/notifications", s.notificationSettings)
	mux.HandleFunc("POST /api/v1/applications/{id}/notifications", s.saveNotification)
	mux.HandleFunc("PUT /api/v1/applications/{id}/notifications/{target}", s.saveNotification)
	mux.HandleFunc("DELETE /api/v1/applications/{id}/notifications/{target}", s.deleteNotification)
	mux.HandleFunc("POST /api/v1/applications/{id}/notifications/{target}/test", s.testNotification)
}
func (s *Server) notificationSettings(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	targets, err := s.Store.NotificationTargets(r.Context(), who(r), a)
	if err != nil {
		failure(w, err)
		return
	}
	history, err := s.Store.NotificationHistory(r.Context(), who(r), a)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": targets, "deliveries": history, "email_available": s.smtpMailAvailable(r.Context()), "encryption_ready": len(s.authEncryptionKey()) == 32})
}
func (s *Server) saveNotification(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Name             string   `json:"name"`
		Kind             string   `json:"kind"`
		Enabled          bool     `json:"enabled"`
		Events           []string `json:"events"`
		Destination      string   `json:"destination"`
		SigningSecret    string   `json:"signing_secret"`
		ExpectedRevision int64    `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("target")
	var old store.NotificationTarget
	if id == "" {
		if in.ExpectedRevision != 0 {
			failure(w, store.ErrConflict)
			return
		}
		id = store.NewID()
	} else {
		var err error
		old, err = s.Store.NotificationTarget(r.Context(), a.ID, id)
		if err != nil {
			failure(w, err)
			return
		}
		if old.Revision != in.ExpectedRevision {
			failure(w, store.ErrConflict)
			return
		}
	}
	secret := notificationSecret{Purpose: "deployment-notification", ApplicationID: a.ID, TargetID: id, Destination: strings.TrimSpace(in.Destination), SigningSecret: in.SigningSecret}
	if old.ID != "" {
		plain, err := s.decryptAuth(old.Destination)
		if err != nil || json.Unmarshal(plain, &secret) != nil || secret.Purpose != "deployment-notification" || secret.ApplicationID != a.ID || secret.TargetID != id {
			problem(w, 503, "notification_credentials_unavailable", "Saved destination cannot be decrypted.")
			return
		}
		if in.Destination != "" {
			secret.Destination = strings.TrimSpace(in.Destination)
		}
		if in.SigningSecret != "" {
			secret.SigningSecret = in.SigningSecret
		}
		if old.Kind != in.Kind && in.Destination == "" {
			problem(w, 400, "invalid_notification", "Enter a destination when changing the channel type.")
			return
		}
	}
	if err := validateNotificationDestination(in.Kind, secret.Destination, secret.SigningSecret); err != nil {
		problem(w, 400, "invalid_notification", err.Error())
		return
	}
	if in.Kind == "email" && in.Enabled && !s.smtpMailAvailable(r.Context()) {
		problem(w, 400, "smtp_unavailable", "Configure installation SMTP before enabling email notifications.")
		return
	}
	if in.Kind != "webhook" {
		secret.SigningSecret = ""
	}
	encrypted, err := s.encryptAuth(store.JSON(secret))
	if err != nil {
		problem(w, 503, "notification_credentials_unavailable", "Configure the installation encryption key before saving notifications.")
		return
	}
	n, err := s.Store.PutNotificationTarget(r.Context(), who(r), a, store.NotificationTarget{ID: id, ApplicationID: a.ID, Name: in.Name, Kind: in.Kind, Enabled: in.Enabled, Events: in.Events, Destination: encrypted}, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	write(w, status, n)
}
func (s *Server) deleteNotification(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.DeleteNotificationTarget(r.Context(), who(r), a, r.PathValue("target"), in.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.Store.TestNotification(r.Context(), who(r), a, r.PathValue("target"), in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]string{"id": id, "status": "pending"})
}
func validateNotificationDestination(kind, destination, secret string) error {
	if kind == "email" {
		email, err := store.NormalizeEmail(destination)
		if err != nil || email != destination {
			return errors.New("Enter one valid email address.")
		}
		return nil
	}
	u, err := url.Parse(destination)
	if err != nil || len(destination) > 2048 || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return errors.New("Use a public HTTPS webhook URL on port 443 without user credentials or a fragment.")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return errors.New("Webhook destinations must be public internet hosts.")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !notificationPublicIP(ip) {
		return errors.New("Webhook destinations must use public internet addresses.")
	}
	switch kind {
	case "slack":
		if host != "hooks.slack.com" || !strings.HasPrefix(u.Path, "/services/") {
			return errors.New("Use a Slack incoming webhook URL from hooks.slack.com/services/.")
		}
	case "discord":
		if (host != "discord.com" && host != "discordapp.com") || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return errors.New("Use a Discord webhook URL from discord.com/api/webhooks/.")
		}
	case "webhook":
		if len(secret) < 32 || len(secret) > 256 {
			return errors.New("Use a webhook signing secret between 32 and 256 characters.")
		}
	default:
		return errors.New("Choose email, Slack, Discord or webhook.")
	}
	return nil
}
func notificationPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/23", "2002::/16"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
func notificationClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
		Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, errors.New("no address")
			}
			for _, ip := range ips {
				if !notificationPublicIP(ip) {
					return nil, errors.New("private webhook address rejected")
				}
			}
			// Dial the validated address directly; do not resolve again after validation.
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}}}
}
func (s *Server) RunNotifications(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	client := notificationClient()
	defer client.CloseIdleConnections()
	for {
		s.deliverNotifications(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) deliverNotifications(parent context.Context, client *http.Client) {
	cleanup, cancel := context.WithTimeout(parent, 3*time.Second)
	_ = s.Store.PruneNotifications(cleanup)
	cancel()
	for i := 0; i < 2 && parent.Err() == nil; i++ {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		d, err := s.Store.ClaimNotification(ctx)
		if err != nil || d == nil {
			cancel()
			return
		}
		outcome, message := s.deliverNotification(ctx, client, *d)
		cancel()
		finish, stop := context.WithTimeout(parent, 3*time.Second)
		_ = s.Store.FinishNotification(finish, *d, outcome, message)
		stop()
	}
}
func (s *Server) deliverNotification(ctx context.Context, client *http.Client, d store.NotificationDelivery) (string, string) {
	var payload map[string]any
	if json.Unmarshal(d.Payload, &payload) != nil {
		return "failed", "Invalid notification payload."
	}
	app, _ := payload["application_id"].(string)
	target, err := s.Store.NotificationTarget(ctx, app, d.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "skipped", "Destination was removed."
	}
	if err != nil {
		return "pending", "Destination lookup is temporarily unavailable."
	}
	if !target.Enabled || target.Revision != d.TargetRevision {
		return "skipped", "Destination was disabled or changed."
	}
	plain, err := s.decryptAuth(target.Destination)
	var secret notificationSecret
	if err != nil || json.Unmarshal(plain, &secret) != nil || secret.Purpose != "deployment-notification" || secret.TargetID != target.ID || secret.ApplicationID != app {
		return "pending", "Destination credentials are unavailable."
	}
	if validateNotificationDestination(target.Kind, secret.Destination, secret.SigningSecret) != nil {
		return "failed", "Destination is invalid."
	}
	if s.Auth.PublicURL != "" {
		payload["dashboard_url"] = strings.TrimSuffix(s.Auth.PublicURL, "/") + "/applications/" + url.PathEscape(app) + "?tab=deployments"
	}
	text := fmt.Sprintf("Hakopod deployment %v\nApplication: %v\nProject: %v / %v\nRevision: %v", payload["status"], payload["application_name"], payload["project"], payload["environment"], payload["revision"])
	if link, ok := payload["dashboard_url"].(string); ok {
		text += "\n" + link
	}
	if target.Kind == "email" {
		if !s.smtpMailAvailable(ctx) {
			return "pending", "SMTP delivery is not configured or enabled."
		}
		if err := s.sendAuthMail(ctx, secret.Destination, "Hakopod deployment: "+fmt.Sprint(payload["status"]), text); err != nil {
			return "pending", "Email delivery failed; retry scheduled."
		}
		return "sent", ""
	}
	var body []byte
	switch target.Kind {
	case "slack":
		body = store.JSON(map[string]any{"text": text, "mrkdwn": false, "unfurl_links": false, "unfurl_media": false})
	case "discord":
		body = store.JSON(map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}})
	default:
		body = store.JSON(payload)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", secret.Destination, strings.NewReader(string(body)))
	if err != nil {
		return "failed", "Destination request is invalid."
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hakopod-Notifications/1")
	req.Header.Set("X-Hakopod-Event-ID", d.ID)
	if target.Kind == "webhook" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret.SigningSecret))
		mac.Write([]byte(timestamp + "."))
		mac.Write(body)
		req.Header.Set("X-Hakopod-Timestamp", timestamp)
		req.Header.Set("X-Hakopod-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	res, err := client.Do(req)
	if err != nil {
		return "pending", "Webhook connection failed; retry scheduled."
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return "sent", ""
	}
	if res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500 {
		return "pending", fmt.Sprintf("Webhook returned HTTP %d; retry scheduled.", res.StatusCode)
	}
	return "failed", fmt.Sprintf("Webhook returned HTTP %d. Check the destination settings.", res.StatusCode)
}
