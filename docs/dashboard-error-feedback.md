# Dashboard error feedback

Request failures use dismissible Hatch toasts. The shared `RequestError` renders an
accent-red notification without moving page content. Errors remain until dismissed,
resolved or their originating screen closes. Repeated polling with the same error
text does not reopen a dismissed notification. Each page or dialog displays at most
three concurrent errors; older notifications yield to newer failures.

`ErrorState` adds a compact recovery row with Retry (when available) and Show error,
so a failed page or section remains recoverable after dismissing its toast. Setup
prerequisites retain their infrastructure link. API codes remain secondary details.
Toasts originating in dialogs stay inside the modal's focus and accessibility scope.
Entered values are preserved when requests fail. Banners are reserved for
announcements and status information, not request errors.

Never style arbitrary `[role="alert"]` elements. Router and accessibility libraries
use hidden live regions with that role; adding borders or padding can reveal empty
strips above the dashboard. Notification chrome belongs only to the toast component.

The shared `Input`, `PasswordField`, `Textarea` and `SelectField` keep native constraint validation
but display its message below the control. Invalid controls expose `aria-invalid`
and link their message through `aria-describedby`; the first invalid control gets
keyboard focus. Correcting a native constraint clears its message. Field help
remains associated with the control.

An input can also receive an explicit `error` string. Build/deployment fields map
API messages only when the API names the exact `field.path:`. General permission,
capacity and network errors use toasts rather than being assigned to an
unrelated input. New forms should follow this pattern and preserve their draft
after a rejected request.

Rendered review covers both themes, desktop/mobile layouts, failure retention,
keyboard focus, long messages and error contrast. Cloud route coverage and
provider/cluster verification limits are recorded in the private Cloud audit.

## Source builds and compute limits

New source builds return to the matching step when the server identifies a field. Input and Select controls share the same inline error and focus behavior, including fields inside optional sections. Only the exact `invalid input:` wrapper is removed for matching; general permission and transport messages stay at form level. Group paths such as `build_args.PUBLIC_URL:` can be shown under their collection editor.

See [Source-build setup](source-build-onboarding.md) for the progression, draft handling and hosted Free guidance.
