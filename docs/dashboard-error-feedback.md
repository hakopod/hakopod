# Dashboard error feedback

Page, request and form failures use the shared accent-red banner with a pale red
surface. The message stays visible where the action failed, retains entered values
and offers an existing retry or recovery action when available. Error codes are
secondary details. The router error boundary provides a recoverable page instead
of exposing a raw runtime exception.

`ErrorState` is the common component. Existing `inline-error` and alert surfaces
share the same theme rules in `web/src/styles/errors.css`, including logs, Git
repository access, infrastructure, settings, authentication and Cloud overlays.

The shared `Input`, `PasswordField`, `Textarea` and `SelectField` keep native constraint validation
but display its message below the control. Invalid controls expose `aria-invalid`
and link their message through `aria-describedby`; the first invalid control gets
keyboard focus. Correcting a native constraint clears its message. Field help
remains associated with the control.

An input can also receive an explicit `error` string. Build/deployment fields map
API messages only when the API names the exact `field.path:`. General permission,
capacity and network errors remain banners rather than being assigned to an
unrelated input. New forms should follow this pattern and preserve their draft
after a rejected request.

Rendered review covered both themes, desktop/mobile layouts, failure retention,
keyboard focus, long messages and error contrast. Cloud route coverage and
provider/cluster verification limits are recorded in the private Cloud audit.

## Source builds and compute limits

New source builds return to the matching step when the server identifies a field. Input and Select controls share the same inline error and focus behavior, including fields inside optional sections. Only the exact `invalid input:` wrapper is removed for matching; general permission and transport messages stay at form level. Group paths such as `build_args.PUBLIC_URL:` can be shown under their collection editor.

See [Source-build setup](source-build-onboarding.md) for the progression, draft handling and hosted Free guidance.
