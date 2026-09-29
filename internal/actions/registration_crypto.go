package actions

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const MaxSealedRegistrationBytes = 256 << 10

var registrationID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var registrationService = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// RegistrationBinding authenticates the immutable database identity alongside
// the private payload. Moving ciphertext to another slot or target must fail.
type RegistrationBinding struct {
	ApplicationID string         `json:"application_id"`
	Service       string         `json:"service"`
	SlotID        string         `json:"slot_id"`
	RunnerID      string         `json:"runner_id"`
	Target        ProviderTarget `json:"target"`
}

func (b RegistrationBinding) associatedData() ([]byte, error) {
	if !registrationID.MatchString(b.ApplicationID) || !registrationID.MatchString(b.SlotID) || !registrationService.MatchString(b.Service) || !ValidProviderID(b.RunnerID) {
		return nil, errors.New("runner registration binding is invalid")
	}
	target, err := b.Target.Canonical()
	if err != nil {
		return nil, errors.New("runner registration target is invalid")
	}
	b.Target = target
	data, err := json.Marshal(b)
	if err != nil {
		return nil, errors.New("runner registration binding is invalid")
	}
	return append([]byte("hakopod-runner-registration-v1\x00"), data...), nil
}

type registrationEnvelope struct {
	Version           int            `json:"version"`
	Runner            ProviderRunner `json:"runner"`
	ManagerConfig     []byte         `json:"manager_config"`
	CleanupCredential []byte         `json:"cleanup_credential"`
	ExpiresAt         *time.Time     `json:"expires_at,omitempty"`
}

func registrationCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("runner registration requires the persistent 32-byte authentication encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("runner registration encryption is unavailable")
	}
	return cipher.NewGCM(block)
}

func SealProviderRegistration(key []byte, binding RegistrationBinding, registration ProviderRegistration) ([]byte, error) {
	if registration.Runner.ID != binding.RunnerID {
		return nil, errors.New("runner registration identity does not match its slot")
	}
	if err := registration.Validate("hakopod-" + binding.SlotID); err != nil {
		return nil, err
	}
	aad, err := binding.associatedData()
	if err != nil {
		return nil, err
	}
	box, err := registrationCipher(key)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(registrationEnvelope{1, registration.Runner, registration.ManagerConfig, registration.CleanupCredential, registration.ExpiresAt})
	if err != nil {
		return nil, errors.New("runner registration cannot be encoded")
	}
	defer clear(data)
	nonce := make([]byte, box.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("runner registration encryption is unavailable")
	}
	sealed := box.Seal(nonce, nonce, data, aad)
	if len(sealed) > MaxSealedRegistrationBytes {
		return nil, errors.New("runner registration exceeds its storage bound")
	}
	return sealed, nil
}

// Expired registrations can still be opened for cleanup. Starting a manager
// separately requires a non-expired registration and current authorization.
func OpenProviderRegistration(key []byte, binding RegistrationBinding, sealed []byte) (ProviderRegistration, error) {
	invalid := errors.New("runner registration cannot be authenticated; preserve the original authentication encryption key")
	box, err := registrationCipher(key)
	if err != nil {
		return ProviderRegistration{}, err
	}
	aad, err := binding.associatedData()
	if err != nil {
		return ProviderRegistration{}, err
	}
	if len(sealed) < box.NonceSize()+box.Overhead() || len(sealed) > MaxSealedRegistrationBytes {
		return ProviderRegistration{}, invalid
	}
	data, err := box.Open(nil, sealed[:box.NonceSize()], sealed[box.NonceSize():], aad)
	if err != nil {
		return ProviderRegistration{}, invalid
	}
	defer clear(data)
	var envelope registrationEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 1 {
		return ProviderRegistration{}, invalid
	}
	registration := ProviderRegistration{Runner: envelope.Runner, ManagerConfig: envelope.ManagerConfig, CleanupCredential: envelope.CleanupCredential, ExpiresAt: envelope.ExpiresAt}
	if registration.Runner.ID != binding.RunnerID || registration.Validate("hakopod-"+binding.SlotID) != nil {
		clear(registration.ManagerConfig)
		clear(registration.CleanupCredential)
		return ProviderRegistration{}, invalid
	}
	return registration, nil
}
