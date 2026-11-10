package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	resource = "https://management.azure.com/"
	scope    = resource + ".default"
)

// token is a bearer token and when to stop using it.
type token struct {
	value   string
	expires time.Time
}

// tokenSource caches a token until a minute before it expires.
type tokenSource struct {
	mu     sync.Mutex
	cached token
	fetch  func(context.Context) (token, error)
}

func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached.value != "" && time.Until(s.cached.expires) > time.Minute {
		return s.cached.value, nil
	}
	t, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.cached = t
	return t.value, nil
}

// identity is Hakopod's own Azure credential, in this order: AKS workload
// identity (AZURE_FEDERATED_TOKEN_FILE), an app registration's client secret
// (AZURE_CLIENT_SECRET), or the managed identity of the VM Hakopod runs on.
// login is the Microsoft Entra authority; imds the managed identity endpoint.
func identity(h *http.Client, login, imds string) *tokenSource {
	tenant, client := os.Getenv("AZURE_TENANT_ID"), os.Getenv("AZURE_CLIENT_ID")
	if host := os.Getenv("AZURE_AUTHORITY_HOST"); host != "" {
		login = host
	}
	endpoint := strings.TrimRight(login, "/") + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	if file := os.Getenv("AZURE_FEDERATED_TOKEN_FILE"); tenant != "" && client != "" && file != "" {
		return &tokenSource{fetch: func(ctx context.Context) (token, error) {
			// The projected token rotates; read it for every exchange.
			assertion, err := os.ReadFile(file)
			if err != nil {
				return token{}, errors.New("AZURE_FEDERATED_TOKEN_FILE cannot be read")
			}
			return entraToken(ctx, h, endpoint, url.Values{"client_id": {client},
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion":      {strings.TrimSpace(string(assertion))}}, "workload identity")
		}}
	}
	if secret := os.Getenv("AZURE_CLIENT_SECRET"); tenant != "" && client != "" && secret != "" {
		return &tokenSource{fetch: func(ctx context.Context) (token, error) {
			return entraToken(ctx, h, endpoint, url.Values{"client_id": {client}, "client_secret": {secret}}, "the client secret")
		}}
	}
	return &tokenSource{fetch: func(ctx context.Context) (token, error) {
		q := url.Values{"api-version": {"2018-02-01"}, "resource": {resource}}
		if client != "" {
			q.Set("client_id", client)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imds+"?"+q.Encode(), nil)
		if err != nil {
			return token{}, err
		}
		req.Header.Set("Metadata", "true")
		return tokenResponse(h, req, "the managed identity endpoint")
	}}
}

func entraToken(ctx context.Context, h *http.Client, endpoint string, form url.Values, from string) (token, error) {
	form.Set("grant_type", "client_credentials")
	form.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return tokenResponse(h, req, from)
}

// tokenResponse reads an OAuth token. Entra returns expires_in as a number,
// the managed identity endpoint as a string.
func tokenResponse(h *http.Client, req *http.Request, from string) (token, error) {
	resp, err := h.Do(req)
	if err != nil {
		return token{}, fmt.Errorf("no Azure credential from %s", from)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
		return token{}, fmt.Errorf("no Azure credential from %s (HTTP %d)", from, resp.StatusCode)
	}
	seconds, err := strconv.ParseInt(strings.Trim(string(out.ExpiresIn), `"`), 10, 64)
	if err != nil {
		seconds = 300
	}
	return token{out.AccessToken, time.Now().Add(time.Duration(seconds) * time.Second)}, nil
}
