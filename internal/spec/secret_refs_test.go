package spec

import "testing"

func TestNativeAndExternalSecretReferencesAreExclusive(t *testing.T) {
	for _, tc := range []struct {
		ref   SecretRef
		valid bool
	}{
		{SecretRef{Ref: "native"}, true}, {SecretRef{Provider: "company", Path: "shop", Key: "PASSWORD"}, true}, {SecretRef{Provider: "company", Key: "PASSWORD"}, true},
		{SecretRef{}, false}, {SecretRef{Ref: "native", Provider: "company", Key: "PASSWORD"}, false}, {SecretRef{Provider: "company", Path: "../secret", Key: "PASSWORD"}, false},
		{SecretRef{Provider: "company", Path: "%2fsecret", Key: "PASSWORD"}, false}, {SecretRef{Provider: "company", Path: "/secret", Key: "PASSWORD"}, false}, {SecretRef{Provider: "company", Key: "bad/key"}, false},
	} {
		if tc.ref.Valid() != tc.valid {
			t.Fatalf("reference validation mismatch: %#v", tc.ref)
		}
	}
	app, err := Parse([]byte(minimum + "\n[services.web.secrets]\nDATABASE_URL={provider='company',path='shop',key='DATABASE_URL'}"))
	if err != nil || app.Services["web"].Secrets["DATABASE_URL"].Provider != "company" {
		t.Fatalf("external TOML reference rejected: %v", err)
	}
	if _, err = Parse([]byte(minimum + "\n[services.web.secrets]\nDATABASE_URL={provider='company',path='shop',key='DATABASE_URL',token='never-store-me'}")); err == nil {
		t.Fatal("credential field accepted into strict TOML")
	}
}
