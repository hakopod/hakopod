package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) deliverAlarmEmails(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	maintenance, stop := context.WithTimeout(parent, 3*time.Second)
	_ = s.Store.PruneAlarms(maintenance)
	_ = s.Store.RemovedAlarmResources(maintenance, "application", "", 0, nil, time.Now().UTC(), s.alarmEmailAvailable(maintenance))
	stop()
	// The same effective delivery gate used for account mail also gates every
	// alarm attempt. Scope email_enabled remains false until explicitly saved.
	if !s.alarmEmailAvailable(parent) {
		return
	}
	fanout, stop := context.WithTimeout(parent, 3*time.Second)
	err := s.Store.FanoutAlarmEmail(fanout)
	stop()
	if err != nil {
		return
	}
	for n := 0; n < 2 && parent.Err() == nil; n++ {
		ctx, cancel := context.WithTimeout(parent, 12*time.Second)
		job, err := s.Store.ClaimAlarmEmail(ctx)
		if err != nil || job == nil {
			cancel()
			return
		}
		recipient, err := s.Store.AlarmEmailRecipient(ctx, *job)
		outcome := "sent"
		if errors.Is(err, store.ErrForbidden) {
			outcome = "skipped"
		} else if err != nil {
			outcome = "retry"
		} else {
			subject := "Hakopod alarm: " + job.Transition
			body := fmt.Sprintf("%s\n\nRecorded transition: %s\nResource: %s\nObserved at: %s\nAlarm: %s\n", job.Summary, job.Transition, job.ResourceName, job.CreatedAt.UTC().Format(time.RFC3339), job.IncidentID)
			if job.Project != "" {
				body += "Project: " + job.Project + "\nEnvironment: " + job.Environment + "\n"
			}
			if s.Auth.PublicURL != "" {
				body += "\nOpen the inbox: " + strings.TrimSuffix(s.Auth.PublicURL, "/") + "/alarms\n"
			}
			if err = s.sendAuthMail(ctx, recipient, subject, body); err != nil {
				outcome = "retry"
			}
		}
		cancel()
		finish, stop := context.WithTimeout(parent, 3*time.Second)
		_ = s.Store.FinishAlarmEmail(finish, *job, outcome)
		stop()
	}
}
