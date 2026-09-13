package secretprovider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
)

var errEncryption = errors.New("secret provider credentials require the original persistent authentication encryption key")

func credentialCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errEncryption
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errEncryption
	}
	return cipher.NewGCM(block)
}

func SealCredentials(key []byte, p Provider, c Credentials) ([]byte, error) {
	if err := c.Validate(p.Kind); err != nil {
		return nil, err
	}
	gcm, err := credentialCipher(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, errEncryption
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, errEncryption
	}
	return gcm.Seal(nonce, nonce, data, []byte("hakopod-secret-provider-v1:"+p.Name+":"+p.Kind)), nil
}

func OpenCredentials(key []byte, p Provider) (Credentials, error) {
	var c Credentials
	gcm, err := credentialCipher(key)
	if err != nil || len(p.EncryptedCredentials) < gcm.NonceSize() {
		return c, errEncryption
	}
	data := p.EncryptedCredentials
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("hakopod-secret-provider-v1:"+p.Name+":"+p.Kind))
	if err != nil || json.Unmarshal(plain, &c) != nil || c.Validate(p.Kind) != nil {
		return Credentials{}, errEncryption
	}
	return c, nil
}
