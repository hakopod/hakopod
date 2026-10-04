// Package slackevents defines the stable, user-selectable Slack event catalog.
package slackevents

// SlackEventDefinition is safe to expose through the public API. It contains
// no customer configuration or runtime data.
type SlackEventDefinition struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var catalog = []SlackEventDefinition{
	{"alarm.opened", "Alarms", "Alarm opened", "An alarm incident opens."},
	{"alarm.resolved", "Alarms", "Alarm resolved", "An alarm incident resolves."},
	{"audit", "Audit", "Audit event", "A recorded Hakopod audit event occurs."},
	{"deployment.queued", "Deployments", "Deployment queued", "A deployment is accepted into the durable queue."},
	{"deployment.started", "Deployments", "Deployment started", "A worker starts a deployment."},
	{"deployment.succeeded", "Deployments", "Deployment succeeded", "A deployment completes successfully."},
	{"deployment.failed", "Deployments", "Deployment failed", "A deployment finishes with a failure."},
	{"deployment.cancelled", "Deployments", "Deployment cancelled", "A deployment is cancelled."},
	{"deployment.superseded", "Deployments", "Deployment superseded", "A newer deployment supersedes this deployment."},
	{"deployment.rollback.requested", "Deployments", "Rollback requested", "A rollback deployment is requested."},
	{"deployment.cancellation.requested", "Deployments", "Cancellation requested", "Cancellation is requested for a deployment."},
	{"application.created", "Applications", "Application created", "A new application is accepted."},
	{"application.configuration.updated", "Applications", "Application configuration accepted", "An application configuration change is accepted."},
	{"application.renamed", "Applications", "Application renamed", "An application display name changes."},
	{"application.deleted", "Applications", "Application deleted", "An empty application is deleted."},
	{"service.added", "Services", "Service added", "A service is accepted into an application."},
	{"service.removed", "Services", "Service removed", "A service removal is accepted."},
	{"service.renamed", "Services", "Service renamed", "A service display name changes."},
	{"service.configuration.updated", "Services", "Service configuration accepted", "A service configuration change is accepted."},
	{"service.image.updated", "Services", "Service image accepted", "A service image change is accepted."},
	{"service.variables.updated", "Services", "Service variables accepted", "A service environment variable or secret reference change is accepted."},
	{"service.resources.updated", "Services", "Service resources accepted", "A service resource change is accepted."},
	{"service.scale.updated", "Services", "Service scaling accepted", "A service replicas or autoscaling change is accepted."},
	{"service.suspended", "Services", "Service suspension accepted", "A service suspension is accepted."},
	{"service.resumed", "Services", "Service resume accepted", "A service resume is accepted."},
	{"service.restart.requested", "Services", "Service restart requested", "A service restart is requested."},
	{"service.network.updated", "Services", "Service network accepted", "A service ports, HTTP, TLS or network access change is accepted."},
	{"service.storage.updated", "Services", "Service storage accepted", "A service storage change is accepted."},
	{"service.healthcheck.updated", "Services", "Service health check accepted", "A service health check change is accepted."},
	{"service.placement.updated", "Services", "Service placement accepted", "A service placement change is accepted."},
	{"service.command.updated", "Services", "Service command accepted", "A service command change is accepted."},
	{"service.delivery.updated", "Services", "Service delivery accepted", "A service update strategy, serverless or delivery configuration change is accepted."},
	{"service.update.started", "Services", "Service update started", "A service update starts."},
	{"service.ready", "Services", "Service ready", "A service becomes ready."},
	{"service.failed", "Services", "Service failed", "A service update fails."},
	{"service.job.scheduled", "Services", "Job scheduled", "A scheduled job configuration is applied."},
	{"service.job.completed", "Services", "Job completed", "A service job completes."},
	{"service.certificate.renewed", "Services", "Certificate renewed", "A service certificate renewal completes."},
}

func Catalog() []SlackEventDefinition { return append([]SlackEventDefinition(nil), catalog...) }
func Selectable(id string) bool {
	for _, definition := range catalog {
		if definition.ID == id {
			return true
		}
	}
	return false
}
func Valid(id string) bool { return id == "test" || Selectable(id) }

// Definition returns a catalog event by ID.
func Definition(id string) (SlackEventDefinition, bool) {
	for _, definition := range catalog {
		if definition.ID == id {
			return definition, true
		}
	}
	return SlackEventDefinition{}, false
}
