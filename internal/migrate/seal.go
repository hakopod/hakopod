package migrate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var errSeal = errors.New("migration credentials require the original persistent authentication encryption key")

// The additional data binds a sealed value to one migration and one purpose,
// so a token sealed for one migration cannot be opened as another's, or as
// any other kind of stored credential.
func sealAAD(migrationID, purpose string) []byte {
	return []byte("hakopod-migration-v1:" + migrationID + ":" + purpose)
}

func sealCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errSeal
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errSeal
	}
	return cipher.NewGCM(block)
}

// Seal encrypts a source token or user-supplied values for one migration with
// the installation's auth key. Purpose is "token" or "variables".
func Seal(key []byte, migrationID, purpose string, plaintext []byte) ([]byte, error) {
	gcm, err := sealCipher(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, errSeal
	}
	return gcm.Seal(nonce, nonce, plaintext, sealAAD(migrationID, purpose)), nil
}

func Open(key []byte, migrationID, purpose string, sealed []byte) ([]byte, error) {
	gcm, err := sealCipher(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errSeal
	}
	out, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], sealAAD(migrationID, purpose))
	if err != nil {
		return nil, errSeal
	}
	return out, nil
}
