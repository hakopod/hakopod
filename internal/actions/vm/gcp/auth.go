package gcp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

const scope = "https://www.googleapis.com/auth/cloud-platform"

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

// identity is Hakopod's own Google credential: a service account key file
// named by GOOGLE_APPLICATION_CREDENTIALS, or else the metadata server of the
// GCE VM or GKE workload Hakopod runs on. gcloud user credentials and
// external account (workload identity federation) files are not read.
func identity(h *http.Client) *tokenSource {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return &tokenSource{fetch: func(ctx context.Context) (token, error) { return keyFileToken(ctx, h, path) }}
	}
	return &tokenSource{fetch: func(ctx context.Context) (token, error) { return metadataToken(ctx, h) }}
}

func metadataToken(ctx context.Context, h *http.Client) (token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	return tokenResponse(h, req, "the metadata server")
}

func tokenResponse(h *http.Client, req *http.Request, from string) (token, error) {
	resp, err := h.Do(req)
	if err != nil {
		return token{}, fmt.Errorf("no Google credential from %s", from)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
		return token{}, fmt.Errorf("no Google credential from %s (HTTP %d)", from, resp.StatusCode)
	}
	return token{out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}

// keyFileToken exchanges a self-signed JWT for an access token, as Google's
// client libraries do for service account keys.
func keyFileToken(ctx context.Context, h *http.Client, path string) (token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return token{}, errors.New("GOOGLE_APPLICATION_CREDENTIALS cannot be read")
	}
	var key struct {
		Type       string `json:"type"`
		Email      string `json:"client_email"`
		PrivateKey string `json:"private_key"`
		TokenURI   string `json:"token_uri"`
	}
	if json.Unmarshal(raw, &key) != nil || key.Type != "service_account" || key.Email == "" {
		return token{}, errors.New("GOOGLE_APPLICATION_CREDENTIALS is not a service account key")
	}
	if key.TokenURI == "" {
		key.TokenURI = "https://oauth2.googleapis.com/token"
	}
	assertion, err := signJWT(key.PrivateKey, key.Email, key.TokenURI, time.Now())
	if err != nil {
		return token{}, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, key.TokenURI, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return tokenResponse(h, req, "the service account key")
}

func signJWT(pemKey, email, audience string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return "", errors.New("the service account key holds no private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", errors.New("the service account key holds no private key")
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("the service account key is not an RSA key")
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	unsigned := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		enc(map[string]any{"iss": email, "scope": scope, "aud": audience, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// impersonate trades Hakopod's own token for one of the capacity's service
// account. The customer grants Hakopod's identity Service Account Token
// Creator on that account and nothing else, so no key leaves their project.
func impersonate(h *http.Client, base *tokenSource, endpoint, account string) *tokenSource {
	return &tokenSource{fetch: func(ctx context.Context) (token, error) {
		own, err := base.Token(ctx)
		if err != nil {
			return token{}, err
		}
		body, _ := json.Marshal(map[string]any{"scope": []string{scope}, "lifetime": "3600s"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/projects/-/serviceAccounts/"+url.PathEscape(account)+":generateAccessToken", bytes.NewReader(body))
		if err != nil {
			return token{}, err
		}
		req.Header.Set("Authorization", "Bearer "+own)
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.Do(req)
		if err != nil {
			return token{}, fmt.Errorf("Google could not impersonate %s", account)
		}
		defer resp.Body.Close()
		var out struct {
			AccessToken string    `json:"accessToken"`
			ExpireTime  time.Time `json:"expireTime"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
			return token{}, fmt.Errorf("Google could not impersonate %s (HTTP %d); grant Hakopod's identity Service Account Token Creator on it", account, resp.StatusCode)
		}
		return token{out.AccessToken, out.ExpireTime}, nil
	}}
}
