package spec

import "testing"

func TestTLSIssuerKindValidation(t *testing.T) {
	for _, tc := range []struct {
		issuer, kind, certificate string
		valid                     bool
	}{
		{"default", "", "", true}, {"default", "ClusterIssuer", "", true}, {"custom", "Issuer", "", true},
		{"custom", "issuer", "", false}, {"", "Issuer", "uploaded", false}, {"", "ClusterIssuer", "", false},
	} {
		svc := Service{Public: true, TLS: &TLSConfig{Issuer: tc.issuer, IssuerKind: tc.kind, Certificate: tc.certificate}}
		if err := validateRuntimeService(svc); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
