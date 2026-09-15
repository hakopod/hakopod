package spec

import "testing"

func TestScheduledJobValidation(t *testing.T) {
	base := `schema_version=1
name="reports"
[services.report]
image="alpine:3.21"
[services.report.job.schedule]
cron="0 * * * *"
`
	app, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	job := app.Services["report"].Job
	if job.Schedule.Timezone != "UTC" || job.Schedule.HistoryLimit != 1 || job.TimeoutSeconds != 300 {
		t.Fatal(job)
	}
	for _, invalid := range []string{"@hourly", "* * * * * *", "0 25 * * *", "TZ=UTC * * * * *"} {
		job.Schedule.Cron = invalid
		if _, err := Normalize(app); err == nil {
			t.Fatal("invalid cron accepted", invalid)
		}
	}
	job.Schedule.Cron = "*/5 * * * *"
	job.Schedule.Timezone = "Mars/Olympus"
	if _, err := Normalize(app); err == nil {
		t.Fatal("invalid timezone")
	}
	job.Schedule.Timezone = "UTC"
	job.Schedule.HistoryLimit = 3
	if _, err := Normalize(app); err == nil {
		t.Fatal("unbounded history")
	}
	job.Schedule.HistoryLimit = 1
	svc := app.Services["report"]
	svc.Suspended = true
	app.Services["report"] = svc
	if _, err := Normalize(app); err != nil {
		t.Fatal(err)
	}
	app.Services["web"] = Service{Image: "nginx:alpine", DependsOn: []string{"report"}}
	if _, err := Normalize(app); err == nil {
		t.Fatal("scheduled job cannot be a deployment completion gate")
	}
}
