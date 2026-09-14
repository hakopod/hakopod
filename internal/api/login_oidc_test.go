package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestOIDCVerifiesSignatureIssuerAudienceExpiryNonceAndEmail(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}
	var issuer string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/.well-known/openid-configuration" {
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		} else {
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}})
		}
	}))
	defer remote.Close()
	issuer = remote.URL
	provider, err := oidc.NewProvider(context.Background(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "fixture"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"valid", "wrong-audience", "wrong-issuer", "expired", "wrong-nonce", "unverified-email", "missing-subject", "tampered"} {
		t.Run(test, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": "client", "sub": "person", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": "browser-bound", "email": "member@example.test", "email_verified": true, "name": "Member"}
			switch test {
			case "wrong-audience":
				claims["aud"] = "other"
			case "wrong-issuer":
				claims["iss"] = "https://other.example"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "wrong-nonce":
				claims["nonce"] = "replay"
			case "unverified-email":
				claims["email_verified"] = false
			case "missing-subject":
				claims["sub"] = ""
			}
			data, _ := json.Marshal(claims)
			signed, err := signer.Sign(data)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			if test == "tampered" {
				raw = raw[:len(raw)-10] + "aaaaaaaaaa"
			}
			token := (&oauth2.Token{AccessToken: "fixture"}).WithExtra(map[string]any{"id_token": raw})
			identity, err := verifyOIDC(context.Background(), provider, loginProvider{ClientID: "client"}, token, "browser-bound")
			if test == "valid" {
				if err != nil || identity.Subject != "person" {
					t.Fatal(identity, err)
				}
			} else if err == nil {
				t.Fatal("unsafe identity accepted")
			}
		})
	}
}
func TestOIDCSettingsRejectUnsafeIssuerAndIncompleteCredentials(t *testing.T) {
	for _, issuer := range []string{"http://issuer.example", "https://user:password@issuer.example", "https://issuer.example?token=secret", "https://issuer.example#fragment", "https:///missing"} {
		if validateLoginProvider(loginProvider{Provider: "oidc", Enabled: true, ClientID: "client", ClientSecret: "secret", IssuerURL: issuer}) == nil {
			t.Fatal("unsafe issuer accepted", issuer)
		}
	}
	if validateLoginProvider(loginProvider{Provider: "oidc", Enabled: true, ClientID: "client", ClientSecret: "secret", IssuerURL: "https://identity.example/realms/team"}) != nil {
		t.Fatal("valid issuer rejected")
	}
}

func TestMalformedLoginSettingsDoNotEchoSecrets(t *testing.T) {
	secret := "never-echo-this-client-secret"
	for _, body := range []string{`{"client_secret":123,"` + secret + `":true}`, `{"` + secret + `":true}`} {
		request := httptest.NewRequest("PUT", "/installation/login-providers/github", strings.NewReader(body))
		response := httptest.NewRecorder()
		var value struct {
			ClientSecret string `json:"client_secret"`
		}
		if decodeLoginProvider(response, request, &value) || response.Code != 400 || strings.Contains(response.Body.String(), secret) {
			t.Fatal("invalid credential payload was accepted or reflected")
		}
	}
}
