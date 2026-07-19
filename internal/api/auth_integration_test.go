package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type authHarness struct {
	t          *testing.T
	server     *httptest.Server
	db         *store.Store
	config     api.AuthConfig
	licenseKey ed25519.PrivateKey
}

func newAuthHarness(t *testing.T, configure func(*api.AuthConfig)) *authHarness {
	t.Helper()
	db, _ := database(t)
	licenseKey := testProLicense(t, db)
	config := api.AuthConfig{PublicURL: "http://localhost:4173", SetupSecret: strings.Repeat("s", 32), EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	if configure != nil {
		configure(&config)
	}
	server := httptest.NewServer((&api.Server{Store: db, Auth: config}).Handler())
	t.Cleanup(server.Close)
	return &authHarness{t, server, db, config, licenseKey}
}
func (h *authHarness) call(method, path, token string, body any, want int) map[string]any {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(store.JSON(body))
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := h.server.Client()
	client.Timeout = 10 * time.Second
	response, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		h.t.Fatalf("non-json response %d", response.StatusCode)
	}
	if response.StatusCode != want {
		h.t.Fatalf("%s %s expected %d got %d: %v", method, path, want, response.StatusCode, result)
	}
	return result
}
func (h *authHarness) owner() string {
	h.t.Helper()
	v := h.call("POST", "/auth/setup", "", map[string]any{"name": "Installer Chosen Owner", "email": "owner@example.test", "password": "correct horse battery staple", "setup_token": h.config.SetupSecret}, 200)
	return v["token"].(string)
}
func TestHumanSetupSessionsTeamsAndDevice(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	h.call("POST", "/auth/setup", "", map[string]string{"name": "Intruder", "email": "bad@example.test", "password": "long enough password"}, 403)
	if !h.call("GET", "/auth/status", "", nil, 200)["setup_required"].(bool) {
		t.Fatal("empty install unexpectedly claimed")
	}
	ownerToken := h.owner()
	if !strings.HasPrefix(ownerToken, "hs_") {
		t.Fatal("browser login returned a machine credential")
	}
	me := h.call("GET", "/me", ownerToken, nil, 200)
	if me["owner"] != true || me["credential_type"] != "browser" {
		t.Fatalf("owner principal %v", me)
	}
	h.call("POST", "/auth/setup", "", map[string]string{"name": "Other", "email": "other@example.test", "password": "long enough password", "setup_token": h.config.SetupSecret}, 409)
	login := h.call("POST", "/auth/login", "", map[string]string{"email": "OWNER@example.test", "password": "correct horse battery staple"}, 200)
	otherSession := login["token"].(string)
	team := h.call("POST", "/teams", ownerToken, map[string]string{"name": "Platform"}, 201)
	teamID := team["id"].(string)
	invite := h.call("POST", "/teams/"+teamID+"/invites", ownerToken, map[string]any{"email": "developer@example.test", "role": "developer", "project": "demo"}, 201)
	parsed, _ := url.Parse(invite["invite_url"].(string))
	token := parsed.Query().Get("token")
	accepted := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": token, "name": "Developer", "password": "developer password strong"}, 200)
	devToken := accepted["token"].(string)
	dev := accepted["user"].(map[string]any)
	devID := dev["id"].(string)
	h.call("POST", "/auth/invites/accept", "", map[string]string{"token": token, "name": "Replay", "password": "replay password strong"}, 401)
	h.call("GET", "/users", devToken, nil, 403)
	h.call("POST", "/teams", devToken, map[string]string{"name": "Unauthorized"}, 403)
	principal, err := h.db.Authenticate(ctx, devToken)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.Allows("deployments:write", "demo", "development", "app") || principal.Allows("deployments:write", "other", "production", "app") {
		t.Fatal("membership scope was not enforced")
	}
	app, err := spec.Normalize(spec.Application{Name: "member-app", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := h.db.Accept(ctx, principal, "demo", "development", app, 0, "human-operation-durable")
	if err != nil {
		t.Fatal(err)
	}
	h.call("PUT", "/projects/demo/members", ownerToken, map[string]string{"identity_id": devID, "role": "viewer"}, 200)
	if err = h.db.Reauthorize(ctx, operation.KeyID, "demo", "development", "member-app"); err == nil {
		t.Fatal("role reduction did not fence a queued human deployment")
	}
	h.call("PUT", "/projects/demo/members", ownerToken, map[string]string{"identity_id": devID, "role": "developer"}, 200)
	device := h.call("POST", "/auth/device/start", "", map[string]any{"project": "demo", "environment": "development", "permissions": []string{"deployments:read", "deployments:write"}}, 200)
	h.call("POST", "/auth/device/token", "", map[string]any{"device_code": device["device_code"]}, 400)
	details := h.call("GET", "/auth/device?user_code="+url.QueryEscape(device["user_code"].(string)), devToken, nil, 200)
	if details["project"] != "demo" {
		t.Fatal("device consent omitted scope")
	}
	h.call("POST", "/auth/device/approve", devToken, map[string]any{"user_code": device["user_code"], "approve": true}, 200)
	h.call("POST", "/auth/device/token", "", map[string]any{"device_code": device["device_code"]}, 400)
	if _, err = h.db.Pool.Exec(ctx, "UPDATE device_codes SET last_polled_at=now()-interval '6 seconds'"); err != nil {
		t.Fatal(err)
	}
	cli := h.call("POST", "/auth/device/token", "", map[string]any{"device_code": device["device_code"]}, 200)
	cliToken := cli["token"].(string)
	if cli["user"].(map[string]any)["credential_type"] != "cli" {
		t.Fatal("CLI did not receive a distinct session kind")
	}
	h.call("POST", "/auth/device/token", "", map[string]any{"device_code": device["device_code"]}, 401)
	cp, err := h.db.Authenticate(ctx, cliToken)
	if err != nil || cp.IsAdmin() || !cp.Allows("deployments:write", "demo", "development", "app") || cp.Allows("deployments:write", "demo", "production", "app") {
		t.Fatal("CLI session was not scoped")
	}
	h.call("POST", "/auth/logout", cliToken, map[string]any{}, 200)
	h.call("GET", "/me", cliToken, nil, 401)
	h.call("PATCH", "/users/"+devID, ownerToken, map[string]bool{"disabled": true, "admin": false}, 200)
	h.call("GET", "/me", devToken, nil, 401)
	h.call("PATCH", "/users/"+me["id"].(string), ownerToken, map[string]bool{"disabled": true, "admin": false}, 400)
	h.call("POST", "/auth/logout", otherSession, map[string]any{}, 200)
	h.call("GET", "/me", otherSession, nil, 401)
	req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/auth/logout", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "hakopod_session", Value: ownerToken})
	req.Header.Set("Origin", "https://attacker.example")
	res, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin cookie mutation accepted")
	}
	t.Log("installer claim, email login, email invite, current roles, durable reauthorization, browser approval, one-shot scoped CLI token, session revocation and cookie CSRF verified")
}
func TestLegacyBootstrapClaimAndMachineSeparation(t *testing.T) {
	h := newAuthHarness(t, nil)
	raw, err := h.db.Bootstrap(context.Background(), "old operator")
	if err != nil {
		t.Fatal(err)
	}
	me := h.call("GET", "/me", raw, nil, 200)
	if me["credential_type"] != "machine" {
		t.Fatal("legacy key kind changed")
	}
	v := h.call("POST", "/auth/setup", raw, map[string]string{"name": "Chosen Human", "email": "chosen@example.test", "password": "chosen password is long"}, 200)
	if v["user"].(map[string]any)["id"] != me["id"] {
		t.Fatal("legacy claim did not convert the authenticated identity")
	}
	h.call("GET", "/me", raw, nil, 200)
	h.call("GET", "/auth/sessions", raw, nil, 403)
	h.call("POST", "/auth/setup", raw, map[string]string{"name": "Other", "email": "other@example.test", "password": "other password is long"}, 409)
}
func codeFor(secret string) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	var step [8]byte
	binary.BigEndian.PutUint64(step[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(step[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}
func TestTOTPRecoveryAndPasskeys(t *testing.T) {
	h := newAuthHarness(t, nil)
	token := h.owner()
	started := h.call("POST", "/auth/mfa/totp/start", token, map[string]string{"password": "correct horse battery staple"}, 200)
	enabled := h.call("POST", "/auth/mfa/totp/confirm", token, map[string]any{"challenge": started["challenge"], "code": codeFor(started["secret"].(string))}, 200)
	codes := enabled["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatal("recovery code count")
	}
	missing := h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 401)
	if missing["error"].(map[string]any)["code"] != "mfa_required" {
		t.Fatal("MFA not enforced")
	}
	h.call("POST", "/auth/login", "", map[string]any{"email": "owner@example.test", "password": "correct horse battery staple", "code": codes[0]}, 200)
	h.call("POST", "/auth/login", "", map[string]any{"email": "owner@example.test", "password": "correct horse battery staple", "code": codes[0]}, 401)
	var encrypted []byte
	if err := h.db.Pool.QueryRow(context.Background(), "SELECT totp_secret FROM identities WHERE owner").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	decoded, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(started["secret"].(string))
	if bytes.Contains(encrypted, decoded) {
		t.Fatal("TOTP secret stored without encryption")
	}
	h.call("POST", "/auth/mfa/totp/disable", token, map[string]any{"password": "correct horse battery staple", "code": codes[1]}, 200)
	registration := h.call("POST", "/auth/passkeys/register/start", token, map[string]string{"name": "Test Authenticator", "password": "correct horse battery staple"}, 200)
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := make([]byte, 32)
	rand.Read(credentialID)
	id := base64.RawURLEncoding.EncodeToString(credentialID)
	options := registration["options"].(map[string]any)["publicKey"].(map[string]any)
	rp := options["rp"].(map[string]any)["id"].(string)
	credential := testRegistration(t, private, credentialID, rp, options["challenge"].(string), h.config.PublicURL)
	h.call("POST", "/auth/passkeys/register/finish", token, map[string]any{"challenge": registration["challenge"], "credential": credential}, 201)
	security := h.call("GET", "/auth/security", token, nil, 200)
	if len(security["passkeys"].([]any)) != 1 {
		t.Fatal("passkey missing from account")
	}
	begin := h.call("POST", "/auth/passkeys/login/start", "", map[string]any{}, 200)
	getOptions := begin["options"].(map[string]any)["publicKey"].(map[string]any)
	me := h.call("GET", "/me", token, nil, 200)
	assertion := testAssertion(t, private, credentialID, me["id"].(string), rp, getOptions["challenge"].(string), h.config.PublicURL, 1)
	logged := h.call("POST", "/auth/passkeys/login/finish", "", map[string]any{"challenge": begin["challenge"], "credential": assertion}, 200)
	if logged["user"].(map[string]any)["id"] != me["id"] {
		t.Fatal("passkey resolved wrong account")
	}
	h.call("POST", "/auth/passkeys/login/finish", "", map[string]any{"challenge": begin["challenge"], "credential": assertion}, 401)
	hostile := h.call("POST", "/auth/passkeys/login/start", "", map[string]any{}, 200)
	hostileOptions := hostile["options"].(map[string]any)["publicKey"].(map[string]any)
	wrongOrigin := testAssertion(t, private, credentialID, me["id"].(string), rp, hostileOptions["challenge"].(string), "https://attacker.example", 2)
	h.call("POST", "/auth/passkeys/login/finish", "", map[string]any{"challenge": hostile["challenge"], "credential": wrongOrigin}, 401)
	h.call("DELETE", "/auth/passkeys/"+id, token, map[string]string{"password": "correct horse battery staple"}, 200)
	t.Log("encrypted TOTP enrollment, mandatory second factor, one-use recovery codes, real WebAuthn ES256 registration/assertion, replay and wrong-origin rejection verified")
}
func testRegistration(t *testing.T, key *ecdsa.PrivateKey, id []byte, rp, challenge, origin string) map[string]any {
	t.Helper()
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(rp))
	auth := append([]byte{}, rpHash[:]...)
	auth = append(auth, 0x45, 0, 0, 0, 0)
	auth = append(auth, make([]byte, 16)...)
	auth = append(auth, byte(len(id)>>8), byte(len(id)))
	auth = append(auth, id...)
	auth = append(auth, cose...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": auth, "attStmt": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	client := store.JSON(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": origin, "crossOrigin": false})
	encode := base64.RawURLEncoding.EncodeToString
	return map[string]any{"id": encode(id), "rawId": encode(id), "type": "public-key", "authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{}, "response": map[string]any{"attestationObject": encode(attestation), "clientDataJSON": encode(client), "transports": []string{"internal"}}}
}
func testAssertion(t *testing.T, key *ecdsa.PrivateKey, id []byte, user, rp, challenge, origin string, count uint32) map[string]any {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rp))
	auth := append([]byte{}, rpHash[:]...)
	auth = append(auth, 0x05, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(auth[33:], count)
	client := store.JSON(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": origin, "crossOrigin": false})
	clientHash := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return map[string]any{"id": encode(id), "rawId": encode(id), "type": "public-key", "authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{}, "response": map[string]any{"authenticatorData": encode(auth), "clientDataJSON": encode(client), "signature": encode(signature), "userHandle": encode([]byte(user))}}
}
