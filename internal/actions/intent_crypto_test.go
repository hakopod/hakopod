package actions

import (
	"bytes"
	"strings"
	"testing"
)

func TestProviderIntentAuthenticatedBeforeRunnerIdentityExists(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	binding := RegistrationBinding{ApplicationID: strings.Repeat("a", 32), Service: "runner", SlotID: strings.Repeat("b", 32), Target: ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}}}
	payload := []byte(`{"version":1,"fixture":"synthetic original trust"}`)
	sealed, err := SealProviderIntent(key, binding, payload)
	if err != nil || bytes.Contains(sealed, payload) {
		t.Fatal("intent was not sealed", err)
	}
	opened, err := OpenProviderIntent(key, binding, sealed)
	if err != nil || !bytes.Equal(opened, payload) {
		t.Fatal("original intent could not be recovered", err)
	}
	for _, mutate := range []func(*RegistrationBinding){
		func(b *RegistrationBinding) { b.ApplicationID = strings.Repeat("c", 32) },
		func(b *RegistrationBinding) { b.Service = "other" },
		func(b *RegistrationBinding) { b.SlotID = strings.Repeat("d", 32) },
		func(b *RegistrationBinding) {
			b.Target.GitLab = &GitLabTarget{URL: "https://gitlab.com", ProjectID: 13}
		},
		func(b *RegistrationBinding) { b.RunnerID = "42" },
	} {
		changed := binding
		mutate(&changed)
		if _, err := OpenProviderIntent(key, changed, sealed); err == nil {
			t.Fatal("intent accepted a changed original identity")
		}
	}
	binding.RunnerID = "42"
	if _, err := OpenProviderRegistration(key, binding, sealed); err == nil {
		t.Fatal("intent ciphertext crossed the registration domain")
	}
	if _, err := SealProviderIntent(key, binding, payload); err == nil {
		t.Fatal("intent accepted an existing runner ID")
	}
}

func TestProviderIntentRejectsCorruptionAndBounds(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	binding := RegistrationBinding{ApplicationID: strings.Repeat("a", 32), Service: "runner", SlotID: strings.Repeat("b", 32), Target: ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}}}
	sealed, err := SealProviderIntent(key, binding, []byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := OpenProviderIntent(key, binding, sealed); err == nil {
		t.Fatal("corrupted intent was accepted")
	}
	for _, payload := range [][]byte{nil, make([]byte, (192<<10)+1)} {
		if _, err := SealProviderIntent(key, binding, payload); err == nil {
			t.Fatal("unbounded intent payload was accepted")
		}
	}
}
