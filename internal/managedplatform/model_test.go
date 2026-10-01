package managedplatform

import (
	"strings"
	"testing"
)

func TestImmutableImageInventory(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, image := range []string{"registry.example.test/app@sha256:" + digest, "registry.example.test/app:v1@sha256:" + digest} {
		if err := ValidateImages(map[string]string{"compute": image}, []string{"compute"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range []string{"registry.example.test/app:latest", "app@sha256:" + digest, "https://registry.example.test/app@sha256:" + digest, "user:password@registry.example.test/app@sha256:" + digest, "registry.example.test/../app@sha256:" + digest} {
		if err := ValidateImages(map[string]string{"compute": image}, []string{"compute"}); err == nil {
			t.Fatal("unsafe image accepted")
		}
	}
	if err := ValidateImages(map[string]string{"compute": "registry.example.test/app@sha256:" + digest, "extra": "registry.example.test/app@sha256:" + digest}, []string{"compute"}); err == nil {
		t.Fatal("extra image accepted")
	}
}

func TestSecretReferencesRejectBodiesAndUnversionedNames(t *testing.T) {
	for _, value := range []SecretReference{{Name: "secret", Revision: 0}, {Name: "password=123", Revision: 1}, {Name: "a/b", Revision: 1}, {Name: strings.Repeat("a", 64), Revision: 1}} {
		if err := value.Validate(); err == nil {
			t.Fatal("unsafe secret reference accepted")
		}
	}
}

func TestHTTPSOriginsRejectAmbiguousAndCredentialBearingURLs(t *testing.T) {
	for _, value := range []string{"http://app.example.test", "https://user:password@app.example.test", "https://*.example.test", "https://app.example.test?", "https://app.example.test#", "https://app.example.test/callback", "https://app.example.test/%2f", "https://app.example.test:8443"} {
		if err := ValidateHTTPSOrigin(value, "public_url"); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	if err := ValidateHTTPSOrigin("https://app.example.test", "public_url"); err != nil {
		t.Fatal(err)
	}
}
