package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"filippo.io/age"
)

func SealCredentials(key []byte, id string, c Credentials) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("backup credential encryption requires the 32-byte authentication encryption key")
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
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, []byte("hakopod-backup-credential-v1:"+id)), nil
}
func OpenCredentials(key []byte, d Destination) (Credentials, error) {
	var c Credentials
	if len(key) != 32 {
		return c, fmt.Errorf("backup credential encryption key is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return c, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return c, err
	}
	if len(d.EncryptedCredentials) < gcm.NonceSize() {
		return c, fmt.Errorf("backup credentials are invalid")
	}
	data, err := gcm.Open(nil, d.EncryptedCredentials[:gcm.NonceSize()], d.EncryptedCredentials[gcm.NonceSize():], []byte("hakopod-backup-credential-v1:"+d.ID))
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, fmt.Errorf("backup credentials cannot be decrypted; preserve the original authentication encryption key")
	}
	identity, err := age.ParseX25519Identity(c.EncryptionIdentity)
	if err != nil || identity.Recipient().String() != d.EncryptionRecipient {
		return c, fmt.Errorf("backup encryption identity does not match destination")
	}
	return c, nil
}
func NewEncryptionIdentity(raw string) (identity, recipient string, err error) {
	var key *age.X25519Identity
	if raw == "" {
		key, err = age.GenerateX25519Identity()
	} else {
		key, err = age.ParseX25519Identity(raw)
	}
	if err != nil {
		return "", "", fmt.Errorf("%w: encryption_identity must be an age X25519 secret key", ErrInput)
	}
	return key.String(), key.Recipient().String(), nil
}
