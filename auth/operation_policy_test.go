package auth

import "testing"

func TestOperationCredentialPolicyMatchesConcreteContractRoutes(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/backup-destinations", true}, {"PUT", "/backup-destinations/" + id, true},
		{"POST", "/git/connections", true}, {"PUT", "/git/connections/" + id, true},
		{"POST", "/databases/" + id + "/credentials", true},
		{"GET", "/git/connections", false}, {"POST", "/git/connections/" + id + "/extra", false},
		{"PUT", "/git/connections/../keys", false},
	} {
		if got := OperationRequiresCredentials(c.method, c.path); got != c.want {
			t.Errorf("%s %s credential=%v", c.method, c.path, got)
		}
	}
}
