# Slack dashboard UI fixture

`slack-ui-review.html` and `slack-ui-review-harness.tsx` are development-only review sources. Product routes do not import them, and they must not be included in a production dashboard build.

Run the fixture only on the designated development VM. It intercepts every `/api/*` request and labels the page as artificial data. It does not authenticate, connect to Slack, read a customer workspace, or submit a real mutation.

Start it with the client-only review configuration:

```sh
pnpm exec vite --config scripts/slack-ui-review.vite.config.ts
```

Open `/scripts/slack-ui-review.html` with these query parameters:

- `scenario=pro-connected`
- `scenario=pro-connected&view=detail&configure=1` (large grouped event catalog and review flow)
- `scenario=pro-connected&view=settings`
- `scenario=selfhost-setup&view=detail`
- `scenario=cloud-owner&view=detail`
- `scenario=cloud-unpaid&view=detail`
- `scenario=expired`
- `scenario=denied`
- `scenario=error`
- `scenario=test-pending`
- `scenario=mutation-error&view=detail`
- `scenario=events-success-channel-error&view=detail` (event save succeeds, first channel save returns 503, retry uses the new revision)
- `scenario=paginated-channels&view=detail` (empty first page, then two manually loaded channel pages)
- `scenario=channel-page-cap&view=detail` (ten pages and the channel-ID fallback)
- `scenario=catalog-unavailable&view=detail` (event catalog failure closes the save path)
- `theme=dark` or `theme=paper`

The fixture is visual evidence only. It does not verify API authorization, OAuth, Slack installation, delivery, entitlement enforcement, or production availability.

The connected fixture includes the full artificial event catalog used to inspect individual deployment,
application, service, and service-update choices. `mutation-error` also retains an unavailable saved
event so the review can confirm it is never dropped silently.

The partial-save scenario mutates its artificial status record. Change an event and the channel,
then save: the events request advances the fixture revision, the first channel request fails, and a
second save must send only the channel change with the advanced revision.

The paginated channel fixture starts with no choices and a continuation cursor. The saved channel
remains available while later pages are loaded; the second page adds a private channel.

The page-cap fixture continues past the tenth page so the manual channel-ID fallback can be inspected.

For the Cloud-owner review, use the actual composed Cloud edition hook and its workspace session/membership queries. Create the normal disposable composition from the intended engine checkout, then start this fixture with its generated dashboard path:

```sh
HAKOPOD_CLOUD_DASHBOARD_ROOT=/path/to/hakopod-cloud/hosted/.dashboard/web \
  pnpm exec vite --config scripts/slack-ui-review.vite.config.ts
```

The Cloud scenarios intercept the composed hook's `/cloud-api/session` and `/cloud-api/workspaces` requests with an owner membership fixture. They remain synthetic UI evidence only.
