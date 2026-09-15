package spec

// Safe recovery restores workload configuration; it never restores external
// data. Jobs and persisted state require an operator-reviewed recovery plan.
type RecoveryPolicy struct {
	OnFailure string `json:"on_failure" toml:"on_failure"`
}

func RecoveryBlocked(next Application, previous *Application) string {
	if next.Recovery != nil && next.Recovery.OnFailure == "disabled" {
		return "Automatic release recovery is disabled in this revision"
	}
	if previous == nil {
		return "No previous successful release is available"
	}
	if len(next.Services) != len(previous.Services) {
		return "Service additions or removals require a reviewed recovery"
	}
	for name := range next.Services {
		if _, ok := previous.Services[name]; !ok {
			return "Service additions or removals require a reviewed recovery"
		}
	}
	for _, app := range []Application{next, *previous} {
		if len(app.Volumes) > 0 {
			return "Persistent volumes require a reviewed recovery; restoring configuration cannot restore stored data"
		}
		for _, name := range Names(app) {
			s := app.Services[name]
			if s.Job != nil {
				return "Deployment and scheduled jobs require a reviewed recovery; Hakopod will not rerun migrations or scheduled work automatically"
			}
			if s.Volume != nil || len(s.Mounts) > 0 {
				return "Persistent volumes require a reviewed recovery; restoring configuration cannot restore stored data"
			}
		}
	}
	return ""
}
