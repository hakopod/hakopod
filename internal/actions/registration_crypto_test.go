package actions

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func registrationFixture() ([]byte, RegistrationBinding, ProviderRegistration) {
	binding := RegistrationBinding{ApplicationID: strings.Repeat("a", 32), Service: "runner", SlotID: strings.Repeat("b", 32), RunnerID: "42", Target: ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: "https://gitlab.com", ProjectID: 123}}}
	expires := time.Now().Add(-time.Hour).UTC()
	registration := ProviderRegistration{Runner: ProviderRunner{ID: "42", Name: "hakopod-" + binding.SlotID, Status: "starting"}, ManagerConfig: []byte(`{"token":"synthetic-manager-secret"}`), CleanupCredential: []byte("synthetic-cleanup-secret"), ExpiresAt: &expires}
	return bytes.Repeat([]byte{9}, 32), binding, registration
}

func TestProviderRegistrationEncryptedRoundTripAndPrivateSerialization(t *testing.T) {
	key, binding, registration := registrationFixture()
	sealed, err := SealProviderRegistration(key, binding, registration)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, registration.ManagerConfig) || bytes.Contains(sealed, registration.CleanupCredential) {
		t.Fatal("registration was persisted in plaintext")
	}
	again, err := SealProviderRegistration(key, binding, registration)
	if err != nil || bytes.Equal(again, sealed) {
		t.Fatal("registrations reused encryption nonces", err)
	}
	opened, err := OpenProviderRegistration(key, binding, sealed)
	if err != nil || !reflect.DeepEqual(opened, registration) {
		t.Fatal("expired registration could not be recovered for cleanup", err)
	}
	public, err := json.Marshal(opened)
	if err != nil || bytes.Contains(public, []byte("synthetic-")) {
		t.Fatal("private registration appeared in public JSON", err)
	}
}

func TestProviderRegistrationAuthenticatesEveryOwnershipDimension(t *testing.T) {
	key, binding, registration := registrationFixture()
	sealed, err := SealProviderRegistration(key, binding, registration)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RegistrationBinding){
		func(b *RegistrationBinding) { b.ApplicationID = strings.Repeat("c", 32) },
		func(b *RegistrationBinding) { b.Service = "other" },
		func(b *RegistrationBinding) { b.SlotID = strings.Repeat("c", 32) },
		func(b *RegistrationBinding) { b.RunnerID = "43" },
		func(b *RegistrationBinding) {
			b.Target.GitLab = &GitLabTarget{URL: "https://gitlab.com", ProjectID: 456}
		},
		func(b *RegistrationBinding) {
			b.Target.GitLab = &GitLabTarget{URL: "https://gitlab.example.com/prefix", ProjectID: 123, TrustPolicy: "approved"}
		},
		func(b *RegistrationBinding) {
			b.Target = ProviderTarget{Provider: ProviderGitHub, GitHub: Target{Repository: "team/repo"}}
		},
	} {
		changed := binding
		mutate(&changed)
		if _, err := OpenProviderRegistration(key, changed, sealed); err == nil {
			t.Fatal("registration moved across an ownership boundary")
		}
	}
	wrongKey := append([]byte(nil), key...)
	wrongKey[0] ^= 1
	if _, err := OpenProviderRegistration(wrongKey, binding, sealed); err == nil {
		t.Fatal("wrong encryption key was accepted")
	}
	for _, payload := range [][]byte{nil, sealed[:10], append(append([]byte(nil), sealed...), 0), bytes.Repeat([]byte{0}, MaxSealedRegistrationBytes+1)} {
		if _, err := OpenProviderRegistration(key, binding, payload); err == nil {
			t.Fatal("corrupt or unbounded registration was accepted")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := OpenProviderRegistration(key, binding, sealed); err == nil {
		t.Fatal("tampered registration was accepted")
	}
}

func TestProviderRegistrationRejectsUnboundAndUnboundedPayloads(t *testing.T) {
	key, binding, registration := registrationFixture()
	if _, err := SealProviderRegistration(nil, binding, registration); err == nil {
		t.Fatal("missing persistent encryption key was accepted")
	}
	registration.Runner.ID = "43"
	if _, err := SealProviderRegistration(key, binding, registration); err == nil {
		t.Fatal("wrong runner registration was accepted")
	}
	registration.Runner.ID = "42"
	registration.ManagerConfig = bytes.Repeat([]byte{1}, 128<<10+1)
	if _, err := SealProviderRegistration(key, binding, registration); err == nil {
		t.Fatal("unbounded registration was accepted")
	}
}
