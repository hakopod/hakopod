package spec

import (
	"strings"
	"testing"
)

func invocationFixture() Application {
	return Application{Name: "jobs", Services: map[string]Service{"report": {Image: "example/job:1", Job: &Job{Invocation: &JobInvocation{AllowedIdentities: []string{strings.Repeat("a", 32)}, InputKeys: []string{"payload"}}}}}}
}
func TestInvocationTemplateDefaultsAndIsolation(t *testing.T) {
	app, err := Normalize(invocationFixture())
	if err != nil {
		t.Fatal(err)
	}
	cfg := app.Services["report"].Job.Invocation
	if cfg.MaxInputBytes != 64<<10 || cfg.QueueLimit != 16 {
		t.Fatal("incorrect bounds")
	}
	for name, change := range map[string]func(*Service){
		"schedule": func(s *Service) { s.Job.Schedule = &JobSchedule{Cron: "* * * * *"} },
		"retry":    func(s *Service) { s.Job.Retries = 1 },
		"identity": func(s *Service) { s.Job.Invocation.AllowedIdentities = []string{"*"} },
		"key":      func(s *Service) { s.Job.Invocation.InputKeys = []string{"payload", "payload"} },
		"bytes":    func(s *Service) { s.Job.Invocation.MaxInputBytes = 128<<10 + 1 },
		"queue":    func(s *Service) { s.Job.Invocation.QueueLimit = 65 },
		"overlap": func(s *Service) {
			v := "bad"
			s.Files = map[string]File{"bad": {MountPath: "/run/hakopod", Content: &v}}
		},
		"persistent": func(s *Service) { s.Volume = &Volume{MountPath: "/data", SizeGiB: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			a := invocationFixture()
			s := a.Services["report"]
			change(&s)
			a.Services["report"] = s
			if _, err := Normalize(a); err == nil {
				t.Fatal("unsafe invocation template accepted")
			}
		})
	}
	app = invocationFixture()
	app.Services["web"] = Service{Image: "example/web:1", DependsOn: []string{"report"}}
	if _, err := Normalize(app); err == nil {
		t.Fatal("invocation readiness dependency accepted")
	}
}
