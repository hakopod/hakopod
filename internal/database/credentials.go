package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// SealCredentials binds the ciphertext to its database ID. Moving a credentials
// row to another database must not grant access to the original database.
func SealCredentials(key []byte, id string, password []byte) ([]byte, error) {
	if len(key) != 32 || len(id) != 32 || len(password) < 32 || len(password) > 128 {
		return nil, fmt.Errorf("database credential configuration is invalid")
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
	return gcm.Seal(nonce, nonce, password, []byte("hakopod-database-v1:"+id)), nil
}
func OpenCredentials(key []byte, d Resource) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("database encryption key is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(d.EncryptedCredentials) < gcm.NonceSize() {
		return nil, fmt.Errorf("database credentials are invalid")
	}
	plain, err := gcm.Open(nil, d.EncryptedCredentials[:gcm.NonceSize()], d.EncryptedCredentials[gcm.NonceSize():], []byte("hakopod-database-v1:"+d.ID))
	if err != nil {
		return nil, fmt.Errorf("database credentials cannot be decrypted")
	}
	return plain, nil
}
