"""Installation-wide Slack alarm and audit integration."""
slack_event = {"type":"string","enum":[
 "alarm.opened","alarm.resolved","audit",
 "deployment.queued","deployment.started","deployment.succeeded","deployment.failed","deployment.cancelled","deployment.superseded","deployment.rollback.requested","deployment.cancellation.requested",
 "application.created","application.configuration.updated","application.renamed","application.deleted",
 "service.added","service.removed","service.renamed","service.configuration.updated","service.image.updated","service.variables.updated","service.resources.updated","service.scale.updated","service.suspended","service.resumed","service.restart.requested","service.network.updated","service.storage.updated","service.healthcheck.updated","service.placement.updated","service.command.updated","service.delivery.updated","service.update.started","service.ready","service.failed","service.job.scheduled","service.job.completed","service.certificate.renewed"]}
schemas["SlackEventDefinition"] = obj({"id":slack_event,"category":S,"label":S,"description":S},["id","category","label","description"])
schemas["SlackTeam"] = obj({"id":S,"name":S},["id","name"])
schemas["SlackChannel"] = obj({"id":S,"name":S,"is_private":B},["id","name","is_private"])
schemas["SlackManifest"] = obj({"display_information":mapping(S),"oauth_config":{},"settings":{},"features":{}},["display_information","oauth_config","settings","features"])
schemas["SlackIntegrationStatus"] = obj({"mode":{"type":"string","enum":["cloud","self_hosted"]},"available":B,"configured":B,"setup_available":B,"reason":S,"team":ref("SlackTeam"),"channel":ref("SlackChannel"),"events":array(slack_event),"event_catalog":array(ref("SlackEventDefinition")),"revision":I,"manifest":ref("SlackManifest")},["mode","available","configured","setup_available","events","event_catalog","revision"])
schemas["SlackConnectInput"] = obj({"client_id":S,"client_secret":{**S,"writeOnly":True}},[])
schemas["SlackConnectResult"] = obj({"authorization_url":S},["authorization_url"])
schemas["SlackChannelInput"] = obj({"channel_id":S,"expected_revision":I},["channel_id","expected_revision"])
schemas["SlackEventsInput"] = obj({"events":{"type":"array","items":slack_event,"minItems":1,"maxItems":64,"uniqueItems":True},"expected_revision":I},["events","expected_revision"])
schemas["SlackDelivery"] = obj({"id":I,"event":{"anyOf":[slack_event,{"type":"string","const":"test"}]},"status":{"type":"string","enum":["pending","sending","sent","skipped","failed"]},"attempts":I,"last_error":S,"created_at":T,"finished_at":{"anyOf":[T,{"type":"null"}]}},["id","event","status","attempts","last_error","created_at"])
schemas["SlackDeliveries"] = obj({"items":array(ref("SlackDelivery")),"next_before":I},["items"])
schemas["SlackTestResult"] = obj({"id":S,"status":{"type":"string","enum":["pending"]}},["id","status"])
route("/integrations/slack","get","getSlackIntegration",ref("SlackIntegrationStatus"))
route("/integrations/slack","delete","disconnectSlack",obj({"deleted":B},["deleted"]),obj({"expected_revision":I},["expected_revision"]))
route("/integrations/slack/connect","post","connectSlack",ref("SlackConnectResult"),ref("SlackConnectInput"))
route("/integrations/slack/channels","get","listSlackChannels",obj({"items":array(ref("SlackChannel")),"next_before":S},["items"]))
paths["/integrations/slack/channels"]["get"]["parameters"].append({"name":"before","in":"query","schema":S})
route("/integrations/slack/channel","put","setSlackChannel",ref("SlackIntegrationStatus"),ref("SlackChannelInput"))
route("/integrations/slack/events","put","setSlackEvents",ref("SlackIntegrationStatus"),ref("SlackEventsInput"))
route("/integrations/slack/deliveries","get","listSlackDeliveries",ref("SlackDeliveries"))
paths["/integrations/slack/deliveries"]["get"]["parameters"].append({"name":"before","in":"query","schema":I})
route("/integrations/slack/test","post","testSlack",ref("SlackTestResult"),obj({"expected_revision":I},["expected_revision"]),"202")
