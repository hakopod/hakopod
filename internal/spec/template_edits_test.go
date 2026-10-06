package spec

import (
	"strings"
	"testing"
)

func TestEditedTemplateSecretsOnlyValidateRetainedReferences(t *testing.T) {
	values := map[string]string{"encryption-key": strings.Repeat("a", 32), "custom-token": "application-specific-value"}
	if err := ValidateEditedTemplateSecretSet("infisical", []string{"encryption-key", "custom-token"}, values); err != nil {
		t.Fatal("removed services still require their secrets", err)
	}
	if err := ValidateTemplateSecretSet("infisical", []string{"encryption-key"}, values); err == nil {
		t.Fatal("original template lost its required secret checks")
	}
	values["encryption-key"] = "invalid-sensitive-fixture"
	if err := ValidateEditedTemplateSecretSet("infisical", []string{"encryption-key"}, values); err == nil || strings.Contains(err.Error(), values["encryption-key"]) {
		t.Fatal("retained known secret validation bypassed or leaked its value")
	}
	if err := ValidateEditedTemplateSecretSet("cockroachdb", nil, nil); err != nil {
		t.Fatal("removed certificate references were required")
	}
}

func TestEditedTemplateSecretsKeepRetainedRelationships(t *testing.T) {
	password := strings.Repeat("p", 24)
	values := map[string]string{"database-password": password, "database-url": "postgresql://hakopod:" + strings.Repeat("q", 24) + "@db:5432/app?sslmode=disable"}
	if err := ValidateEditedTemplateSecretSet("infisical", []string{"database-url"}, values); err != nil {
		t.Fatal("independent retained connection URL rejected", err)
	}
	if err := ValidateEditedTemplateSecretSet("infisical", []string{"database-url", "database-password"}, values); err == nil {
		t.Fatal("mismatched retained database credentials accepted")
	}
	if err := ValidateEditedTemplateSecretSet("mysql", []string{"database-password"}, values); err != nil {
		t.Fatal(err)
	}
	values["database-root-password"] = password
	if err := ValidateEditedTemplateSecretSet("mysql", []string{"database-password", "database-root-password"}, values); err == nil {
		t.Fatal("same retained root/application credentials accepted")
	}
}
