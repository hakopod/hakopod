package spec

import "testing"

func TestSafeRecoveryPolicy(t *testing.T) {
	app, err := Normalize(Application{Name: "sample", Services: map[string]Service{"web": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := Normalize(app)
	if RecoveryBlocked(app, &previous) != "" {
		t.Fatal("stateless recovery blocked")
	}
	app.Recovery = &RecoveryPolicy{OnFailure: "disabled"}
	if RecoveryBlocked(app, &previous) == "" {
		t.Fatal("disabled recovery allowed")
	}
	app.Recovery = nil
	s := app.Services["web"]
	s.Job = &Job{}
	app.Services["web"] = s
	if RecoveryBlocked(previous, &app) == "" {
		t.Fatal("previous migration would rerun")
	}
	s.Job = nil
	s.Volume = &Volume{}
	app.Services["web"] = s
	if RecoveryBlocked(app, &previous) == "" {
		t.Fatal("stateful recovery allowed")
	}
	s.Volume = nil
	app.Services["web"] = s
	app.Services["api"] = s
	if RecoveryBlocked(app, &previous) == "" {
		t.Fatal("service topology rollback allowed")
	}
	app.Recovery = &RecoveryPolicy{OnFailure: "always"}
	if _, err = Normalize(app); err == nil {
		t.Fatal("unknown policy accepted")
	}
}
