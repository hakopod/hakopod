package actions

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
)

// Intent ciphertext is separate from registration material. Its domain and
// original slot identity are authenticated before the first external request;
// no provider runner ID or registration token exists at this point.
func intentAssociatedData(binding RegistrationBinding) ([]byte, error) {
	if binding.RunnerID != "" || !registrationID.MatchString(binding.ApplicationID) || !registrationID.MatchString(binding.SlotID) || !registrationService.MatchString(binding.Service) {
		return nil, errors.New("provider intent binding is invalid")
	}
	target, err := binding.Target.Canonical()
	if err != nil || target.Provider != ProviderGitLab {
		return nil, errors.New("provider intent target is invalid")
	}
	binding.Target = target
	data, err := json.Marshal(binding)
	if err != nil {
		return nil, errors.New("provider intent binding cannot be encoded")
	}
	return append([]byte("hakopod-provider-intent-v1\x00"), data...), nil
}

func SealProviderIntent(key []byte, binding RegistrationBinding, payload []byte) ([]byte, error) {
	if len(payload) < 1 || len(payload) > 192<<10 {
		return nil, errors.New("provider intent payload exceeds its bound")
	}
	aad, err := intentAssociatedData(binding)
	if err != nil {
		return nil, err
	}
	box, err := registrationCipher(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, box.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("provider intent encryption is unavailable")
	}
	return box.Seal(nonce, nonce, payload, aad), nil
}

func OpenProviderIntent(key []byte, binding RegistrationBinding, sealed []byte) ([]byte, error) {
	aad, err := intentAssociatedData(binding)
	if err != nil {
		return nil, err
	}
	box, err := registrationCipher(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < box.NonceSize()+box.Overhead()+1 || len(sealed) > (192<<10)+box.NonceSize()+box.Overhead() {
		return nil, errors.New("provider intent ciphertext exceeds its bound")
	}
	payload, err := box.Open(nil, sealed[:box.NonceSize()], sealed[box.NonceSize():], aad)
	if err != nil {
		return nil, errors.New("provider intent cannot be authenticated; preserve the original authentication encryption key")
	}
	return payload, nil
}
