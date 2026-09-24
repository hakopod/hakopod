# Framework onboarding UI review

Independent review on 2026-09-24 against [the standing UI/UX checklist](ui-ux-checklist.md). Result: pass for the changed shared source-build flow after the findings below were corrected.

## Coverage and evidence

The local fixture loads the actual `builds.new.tsx` and `builds.$buildId.edit.tsx` route components, shared styles, forms and API client. Its fetch boundary accepts only artificial local records; no production account, Git provider, cluster or workload was touched.

- New and edit routes: dark and Paper themes at 320, 390 and 1280 pixels (12 cases), loaded controls, completed fonts, one h1, no rendered error boundary and no document overflow.
- Linked application: actual new route with `?application=fixture-app`, loaded service metadata, source branch changes, failed detection and a Node-to-static port mismatch.
- Cloud composition: the same shared components in the composed dashboard, with the real private edition module and synthetic hosted Free workspace/session. New route in both themes at 320 and 1280 pixels (four cases). This retains the hosted compute notice and architecture restriction.
- Full-page and normal viewport screenshots were captured. Representative mobile/desktop screenshots in both themes, edit/new and Cloud were inspected directly. Desktop sticky footer behavior was distinguished from full-page screenshot capture: cards and settings remain reachable by scrolling and keyboard.

Ignored local evidence: `work/framework-review/` contains the fixture, `review.mjs`, `interactions.mjs`, `stale-linked.mjs`, `cloud-review.mjs`, `results.json`, `cloud-results.json`, and `screenshots/`. These are synthetic UI checks, not proof of successful Git builds or deployment.

## Completed applicable checklist

- [x] Shared page inset remains 24px desktop / 16px mobile; nested route content adds no page inset or maximum width. Header divider spans the page.
- [x] One page h1, compact form sections and no duplicate visible step headings. Existing contextual help remains separate from essential warnings.
- [x] Step/category selection uses the shared red text; radio cards have native selection semantics, visible selected text and visible keyboard focus.
- [x] Both themes preserve legible card labels, detection status and warning text. Two columns fit at 320px and four columns fit the desktop form.
- [x] All dropdowns use the existing SelectField. Long option text remains within its clipped trigger; no document overflow.
- [x] Category/card controls support touch; radio arrow navigation changes selection and Tab proceeds to the next control. Build settings opens with normal summary interaction.
- [x] Consequential creation has the existing runtime/review step and workflow review action. Saving failure retains the entered draft on review.
- [x] Initial detection applies the returned Next.js plan; later detection preserves an edited build command. Switching frameworks restores each recipe's draft. Use detected settings explicitly restores the returned plan.
- [x] Changed repository inputs clear the old detected commit, notes and restore action. A failed new detection preserves the selected recipe and permits retry or manual continuation.
- [x] Linked service port mismatch is visibly explained: a service routed to 3000 with a static recipe on 8080 requires matching the port or updating service networking before deployment.
- [x] Source choices and summary reflect actual API observations and supported recipes/provider build execution; the UI does not claim a deployed application from a detection result.
- [x] Cloud composition copies canonical shared source and only replaces designated edition modules; no private source is added to the public engine.

## Findings resolved

The sidebar summary initially used `word-break: break-all` for ordinary sentences, splitting words mid-line. It now uses overflow wrapping that keeps ordinary words intact while containing long repository/path values. A follow-up desktop screenshot confirms the fix.

During implementation review, duplicate visible step headings and muted selected-card text were removed/corrected. The linked-service port warning was added and verified in the loaded application fixture.

Initial fixture attempts missing the detection commit SHA, the root edition attribute, or a single deduplicated React runtime were corrected; failed/loading captures were discarded. They were fixture failures, not reported product failures.

## Limits

The full authenticated shell, Git provider permission/CI execution, backend authorization and real deployment outcomes are outside this synthetic visual pass. The fixture deliberately returns a save failure. Backend/unit/build validation is recorded separately by the implementation task. Existing authentication, unrelated route families and generic help interactions were not newly audited here.
