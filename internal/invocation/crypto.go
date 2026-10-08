package invocation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
)

func DecodeKey(value string) ([]byte, error) {
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString} {
		key, err := decode(value)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, fmt.Errorf("job invocations require a 32-byte encryption key")
}
func box(key []byte, id, kind string) (cipher.AEAD, []byte, error) {
	if len(key) != 32 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) || (kind != "input" && kind != "logs") {
		return nil, nil, fmt.Errorf("job invocation encryption context is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	return gcm, []byte("hakopod-job-invocation-v1:" + id + ":" + kind), err
}
func Seal(key []byte, id, kind string, plain []byte) ([]byte, error) {
	gcm, aad, err := box(key, id, kind)
	if err != nil {
		return nil, err
	}
	limit := MaxInputBytes
	if kind == "logs" {
		limit = MaxLogBytes
	}
	if len(plain) > limit {
		return nil, fmt.Errorf("job invocation encrypted value exceeds its byte limit")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, aad), nil
}
func Open(key []byte, id, kind string, sealed []byte) ([]byte, error) {
	gcm, aad, err := box(key, id, kind)
	if err != nil {
		return nil, err
	}
	limit := MaxInputBytes
	if kind == "logs" {
		limit = MaxLogBytes
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() || len(sealed) > limit+gcm.NonceSize()+gcm.Overhead() {
		return nil, fmt.Errorf("job invocation encrypted value is invalid")
	}
	value, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], aad)
	if err != nil {
		return nil, fmt.Errorf("job invocation encrypted value could not be verified")
	}
	return value, nil
}
