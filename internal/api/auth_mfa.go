package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) authEncryptionKey() []byte {
	v := s.Auth.EncryptionKey
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString} {
		key, err := decode(v)
		if err == nil && len(key) == 32 {
			return key
		}
	}
	return nil
}
func (s *Server) encryptAuth(value []byte) ([]byte, error) {
	key := s.authEncryptionKey()
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: set a 32-byte authentication encryption key to enable this method", store.ErrInput)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, value, []byte("hakopod-auth-v1")), nil
}
func (s *Server) decryptAuth(value []byte) ([]byte, error) {
	key := s.authEncryptionKey()
	if len(key) != 32 {
		return nil, store.ErrUnauthorized
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(value) < gcm.NonceSize() {
		return nil, store.ErrUnauthorized
	}
	plain, err := gcm.Open(nil, value[:gcm.NonceSize()], value[gcm.NonceSize():], []byte("hakopod-auth-v1"))
	if err != nil {
		return nil, store.ErrUnauthorized
	}
	return plain, nil
}
func totpCode(secret []byte, step int64) string {
	var value [8]byte
	binary.BigEndian.PutUint64(value[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(value[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	code := (binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code)
}
func validTOTP(secret []byte, code string, last int64) (int64, bool) {
	now := time.Now().Unix() / 30
	for _, step := range []int64{now, now - 1, now + 1} {
		if step > last && subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
func recoveryDigest(code string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))))
	return sum[:]
}
func (s *Server) verifyMFA(ctx context.Context, id string, encrypted []byte, last int64, code string) error {
	secret, err := s.decryptAuth(encrypted)
	if err != nil {
		return err
	}
	if step, ok := validTOTP(secret, strings.TrimSpace(code), last); ok {
		result, err := s.Store.Pool.Exec(ctx, "UPDATE identities SET totp_step=$2 WHERE id=$1 AND totp_step<$2 AND totp_secret=$3", id, step, encrypted)
		if err == nil && result.RowsAffected() == 1 {
			return nil
		}
		if err != nil {
			return err
		}
	}
	result, err := s.Store.Pool.Exec(ctx, "DELETE FROM recovery_codes WHERE identity_id=$1 AND digest=$2", id, recoveryDigest(code))
	if err == nil && result.RowsAffected() == 1 {
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = s.Store.Pool.Exec(ctx, "UPDATE identities SET failed_logins=failed_logins+1,locked_until=CASE WHEN failed_logins>=4 THEN now()+interval '15 minutes' ELSE locked_until END WHERE id=$1", id)
	return store.ErrUnauthorized
}
func human(w http.ResponseWriter, r *http.Request) bool {
	if who(r).CredentialType != "browser" {
		authFailure(w, store.ErrForbidden)
		return false
	}
	return true
}
func (s *Server) authSecurity(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	p := who(r)
	var totp, password bool
	var codes int
	if err := s.Store.Pool.QueryRow(r.Context(), "SELECT totp_secret IS NOT NULL,password_hash IS NOT NULL,(SELECT count(*) FROM recovery_codes WHERE identity_id=$1) FROM identities WHERE id=$1", p.ID).Scan(&totp, &password, &codes); err != nil {
		authFailure(w, err)
		return
	}
	keys, err := s.passkeyList(r.Context(), p.ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"totp_enabled": totp, "password_enabled": password, "recovery_codes_remaining": codes, "passkeys": keys})
}
func (s *Server) authTOTPStart(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := s.Store.CheckPassword(r.Context(), who(r).Email, in.Password)
	if err != nil {
		authFailure(w, err)
		return
	}
	if len(p.TOTPSecret) > 0 {
		problem(w, 409, "already_enabled", "two-factor authentication is already enabled")
		return
	}
	secret := make([]byte, 20)
	if _, err = rand.Read(secret); err != nil {
		authFailure(w, err)
		return
	}
	encrypted, err := s.encryptAuth(secret)
	if err != nil {
		authFailure(w, err)
		return
	}
	challenge, err := s.Store.NewChallenge(r.Context(), "totp", map[string]any{"identity_id": p.ID, "secret": encrypted}, 10*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	uri := "otpauth://totp/" + url.PathEscape("Hakopod:"+p.Email) + "?" + url.Values{"secret": {encoded}, "issuer": {"Hakopod"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	write(w, 200, map[string]string{"challenge": challenge, "secret": encoded, "otpauth_url": uri})
}
func (s *Server) authTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	challenge, err := s.Store.ConsumeChallenge(r.Context(), in.Challenge, "totp")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending struct {
		IdentityID string `json:"identity_id"`
		Secret     []byte `json:"secret"`
	}
	if json.Unmarshal(challenge.Data, &pending) != nil || pending.IdentityID != who(r).ID {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	secret, err := s.decryptAuth(pending.Secret)
	if err != nil {
		authFailure(w, err)
		return
	}
	step, valid := validTOTP(secret, in.Code, -1)
	if !valid {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), "UPDATE identities SET totp_secret=$2,totp_step=$3,updated_at=now() WHERE id=$1 AND totp_secret IS NULL", pending.IdentityID, pending.Secret, step)
	if err != nil {
		authFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		authFailure(w, store.ErrConflict)
		return
	}
	codes := []string{}
	for i := 0; i < 10; i++ {
		random := make([]byte, 8)
		if _, err = rand.Read(random); err != nil {
			authFailure(w, err)
			return
		}
		encoded := strings.ToUpper(hex.EncodeToString(random))
		code := encoded[:8] + "-" + encoded[8:]
		codes = append(codes, code)
		if _, err = tx.Exec(r.Context(), "INSERT INTO recovery_codes(identity_id,digest) VALUES($1,$2)", pending.IdentityID, recoveryDigest(code)); err != nil {
			authFailure(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'mfa.enable',$1)", who(r).ID, who(r).KeyID); err != nil {
		authFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"enabled": true, "recovery_codes": codes})
}
func (s *Server) authTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := s.Store.CheckPassword(r.Context(), who(r).Email, in.Password)
	if err != nil {
		authFailure(w, err)
		return
	}
	if len(p.TOTPSecret) == 0 {
		authFailure(w, store.ErrInput)
		return
	}
	if err = s.verifyMFA(r.Context(), p.ID, p.TOTPSecret, p.TOTPStep, in.Code); err != nil {
		authFailure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "UPDATE identities SET totp_secret=NULL,totp_step=-1 WHERE id=$1", p.ID); err != nil {
		authFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM recovery_codes WHERE identity_id=$1", p.ID); err != nil {
		authFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=now() WHERE identity_id=$1 AND kind IN ('browser','cli') AND id<>$2 AND revoked_at IS NULL", p.ID, who(r).KeyID); err != nil {
		authFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"disabled": true})
}
