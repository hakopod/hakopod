package sessionguard

import (
	"reflect"
	"strings"
	"testing"
)

func TestSessionGuardChecksExecIdentityBeforeCommand(t *testing.T) {
	t.Setenv("HAKOPOD_POD_UID", "pod-one")
	t.Setenv("HAKOPOD_SESSION_GENERATION", "generation-one")
	command := []string{"helper", "--fixed"}
	args := append([]string{"run", "--expected-pod-uid", "pod-one", "--expected-generation", "generation-one", "--ready-token", strings.Repeat("ab", 32), "--"}, command...)
	got, err := runCommand(args)
	if err != nil || !reflect.DeepEqual(got, command) {
		t.Fatal("matching exec target rejected", got, err)
	}
	for _, change := range []func(){func() { t.Setenv("HAKOPOD_POD_UID", "pod-two") }, func() { t.Setenv("HAKOPOD_SESSION_GENERATION", "generation-two") }} {
		t.Setenv("HAKOPOD_POD_UID", "pod-one")
		t.Setenv("HAKOPOD_SESSION_GENERATION", "generation-one")
		change()
		if _, err = runCommand(args); err == nil {
			t.Fatal("replacement pod or generation reached command")
		}
	}
	for _, bad := range [][]string{{"run", "--expected-pod-uid", "pod-one", "--expected-generation", "generation-one", "--ready-token", "invalid", "--", "helper"}, {"run", "--expected-pod-uid", "pod-one", "--", "helper"}, {"run", "--expected-pod-uid", "", "--expected-generation", "generation-one", "--", "helper"}, {"run", "--expected-pod-uid", "pod-one", "--expected-generation", "generation-one", "--"}} {
		if _, err = runCommand(bad); err == nil {
			t.Fatal("incomplete exec identity accepted")
		}
	}
}
