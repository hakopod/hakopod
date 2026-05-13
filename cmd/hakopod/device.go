package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/store"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func deviceLogin(ctx context.Context, cfg config, noBrowser bool, permissions []string) (store.Session, error) {
	cfg.Key = ""
	client, err := anonymousClient(cfg)
	if err != nil {
		return store.Session{}, err
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri_complete"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err = client.request(ctx, "POST", "/auth/device/start", map[string]any{"project": cfg.Project, "environment": cfg.Environment, "permissions": permissions}, "", &start); err != nil {
		return store.Session{}, err
	}
	if start.DeviceCode == "" || start.UserCode == "" || start.ExpiresIn < 1 || start.ExpiresIn > 900 {
		return store.Session{}, errors.New("API returned invalid device authorization")
	}
	u, err := url.Parse(start.URI)
	if err != nil || u.User != nil || u.Fragment != "" {
		return store.Session{}, errors.New("API returned invalid browser URL")
	}
	origin := *u
	origin.Path = ""
	origin.RawQuery = ""
	if _, err = anonymousClient(config{URL: origin.String()}); err != nil {
		return store.Session{}, err
	}
	fmt.Printf("Open %s\nVerify the code %s and approve %s/%s.\n", start.URI, start.UserCode, cfg.Project, cfg.Environment)
	if !noBrowser {
		var command *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			command = exec.Command("open", start.URI)
		case "windows":
			command = exec.Command("rundll32", "url.dll,FileProtocolHandler", start.URI)
		default:
			command = exec.Command("xdg-open", start.URI)
		}
		if command != nil {
			_ = command.Start()
			if command.Process != nil {
				go func() { _ = command.Wait() }()
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(start.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(max(5, min(start.Interval, 30))) * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return store.Session{}, errors.New("browser authorization expired or was cancelled; run login again")
		case <-timer.C:
		}
		var session store.Session
		err = client.request(ctx, "POST", "/auth/device/token", map[string]string{"device_code": start.DeviceCode}, "", &session)
		if err == nil {
			if !strings.HasPrefix(session.Token, "hs_") || session.User.CredentialType != "cli" {
				return store.Session{}, errors.New("API returned an invalid human CLI session")
			}
			return session, nil
		}
		if strings.HasPrefix(err.Error(), "authorization_pending:") {
			timer.Reset(interval)
			continue
		}
		if strings.HasPrefix(err.Error(), "slow_down:") {
			interval = min(interval+5*time.Second, 30*time.Second)
			timer.Reset(interval)
			continue
		}
		return store.Session{}, err
	}
}
