# Service secret entry review

Independent UI/UX review on 2026-09-17, using the [dashboard checklist](ui-ux-checklist.md), repository contributor rules and Hatch components. The affected UI passes after the interaction fixes below.

## Coverage

| Surface | Rendered coverage |
| --- | --- |
| New application, including multiple services | Self-hosted and Cloud, dark and Paper, 1440px and 390px |
| Add service to an existing application | Same eight edition/theme/viewport combinations |
| Git source build runtime environment | Same eight combinations, plus workflow review and failed save |
| Application environment | Same eight combinations, plus secret references and failed upload |
| Service environment | Same eight combinations, plus inherited variables and secret references |

The main matrix contains 40 rendered cases. Application/service environment captures were refreshed after their final heading and help-copy changes. Additional 320px checks cover long revealed values and denied access in both themes and editions. The Cloud fixture represents a regular workspace owner with a connected BYO server; hosted Free quota behavior is outside this review.

## Completed checklist

- [x] Loaded route content and fonts before capture; no unexpected page errors, unhandled fixture requests or loading placeholders in the final matrix.
- [x] Inspected full-page screenshots, route contact sheets and full-resolution secret sections. The separate variable and secret headings, masked values, multiline placeholder, reveal action, import instructions and error states remain readable in both themes.
- [x] Measured real control bounds at desktop and mobile widths. No document overflow or controls escaping the viewport. Existing page insets and shared form layout remain intact.
- [x] Verified keyboard and pointer focus through automatic classification, manual protection, reveal/hide and removal. Self-hosted uses a visible two-pixel outline and renders no decorative brackets; Cloud retains its existing focus treatment. Touch reveal/hide works.
- [x] Pasted synthetic `.env` data into the dedicated editor and individual name/value inputs. Ordinary values remain variables; detected credentials move into Secrets. Successful import clears the pasted plaintext editor. An explicitly added secret with an ordinary name remains a secret.
- [x] Verified masked multiline PEM paste, reveal/edit/hide preservation, and continued typing when credential detection changes the value control. Base64 padding in PEM content is preserved.
- [x] Duplicate imports fail atomically without overwriting existing names or partially adding other rows. During delayed file reads, file input, Add secret and review controls are disabled.
- [x] Checked isolation between services. A validation error in the second service prevents all secret upload requests. Failed secret uploads preserve entered values.
- [x] Checked generated TOML, environment review payloads and Git build payloads: synthetic secret values are absent and secret references are present. A failed Git build save retains the runtime secret when returning from review.
- [x] Read-only project roles do not receive mutation controls. Shared Button, Input and Textarea components are reused; no new selector or style system was introduced.

## Findings resolved

1. Promoting a row on blur initially discarded keyboard and mouse focus. Focus now follows the equivalent value control after the row moves into Secrets.
2. Clicking Show or Remove immediately after typing a sensitive name initially swallowed the action during promotion. The first click now performs its action.
3. Credential detection could replace a textarea while a user was typing, and masked multiline paste could lose newlines. Control transitions preserve focus and typed content; multiline secrets preserve their original value.
4. Import and build-review copy now use singular labels correctly. Application/service editors identify both variables and secrets rather than counting secret rows as plain variables.

## Evidence and limits

Local ignored evidence is in `/tmp/hakopod-git-picker/web/work/secret-entry-review/`: `results.json`, `environment-final-results.json`, `interaction-results.json`, `transition-results.json`, `focus-visible-results.json`, `edge-results.json`, `save-results.json`, the review scripts, and named full-page/section screenshots.

This was an isolated headless Chromium review with explicitly synthetic accounts, applications, credential values and intercepted API responses. It made no production writes and did not use real credentials or the user's open browser windows. It verifies browser behavior and outgoing request contents, not production encryption, API authorization, Kubernetes injection, actual deployments or external provider behavior. Backend tests and live-runtime verification are recorded separately. Unrelated dashboard routes and Safari/Firefox were not retested.
