# Reliable Batch Downloads — Implementation Plan

**Status:** Implementation and manual QA complete; ready for review
**Branch:** `feat/reliable-batch-downloads`
**Base:** `origin/main` at `43c7d24`
**Last updated:** 2026-08-22

This is the living implementation plan for reliable batch downloads. Keep the status, checklists, decisions, and validation record current as the feature is implemented.

## 1. Source material

- Product plan: `/Users/tejas/vidstow-reliable-batch-downloads-v2.html`
- UI reference: Penpot project **VidStow · Product UI**
  - **Reliable batch downloads — exploration**: `206f7735-38a5-80c3-8008-8421bf7c2072`
  - **Batch URLs — MVP**: `b44c5d20-92db-805f-8008-8498f2b0d2e9`
  - Linked **Batch URLs — Main** content board: `22fad558-50af-800a-8008-849f0af8a959`
  - Use the newer **Polish · 02 · Queue** (`b44c5d20-92db-805f-8008-84b1df0f614a`) and **Polish · 08 · Playlist review** (`b44c5d20-92db-805f-8008-84b1e07a4069`) boards for the current shared shell, spacing, and control patterns.
- Screenshot supplied during planning: `Screenshot 2026-08-22 at 11.48.50 AM.png`
- Relevant architecture:
  - `docs/ARCHITECTURE.md`
  - `docs/PLAYLIST_COLLECTIONS.md`
  - `docs/diagnostics/CONTRACT_V1.md`

## 2. Confirmed product decisions

1. Batch entry belongs on **Home**.
2. MVP accepts only individual public YouTube video and Shorts URLs; playlist URLs are rejected.
3. A batch contains **2–20 non-empty input lines**.
4. Duplicate detection is scoped to the current pasted batch and uses canonical video identity, not raw URL text.
5. One format/output-policy selection applies to every ready item in the batch.
6. URLs are analyzed independently. Invalid, duplicate, or analysis-failed lines remain visible, while all ready lines are admitted atomically when the user starts the batch.
7. A batch is represented by a durable, expandable parent in Queue.
8. Auth-required media is permanent/non-retryable in this release because authenticated downloads are not supported.
9. Disk recovery guidance is: free space or change the default folder, then start the item again. Existing failed-job output reservations are not migrated to a new folder.
10. The structured failure taxonomy applies to all jobs, not only batch children.
11. Product measurements are local-only, privacy-safe aggregate counters. Stable message keys are introduced now; English remains the only copy in this release.
12. Development occurs on a new branch from the latest `origin/main`.
13. Pre-admission batch input and review remain on Home. Queue is shown only after successful admission; validation counts and **Edit lines / Start downloads** controls never appear on Queue.
14. The approved UI structure is the line-level Home review and expandable Queue parent documented in Section 5. The current polished application shell is authoritative for window dimensions and responsive behavior rather than the older 1280×800 feature frame.

## 3. Goals

- Let a user paste 2–20 individual video URLs, understand which lines are usable, and start every ready item as one batch.
- Preserve each successful child when sibling jobs fail.
- Give every failure a clear category, explanation, next action, and capability-backed command.
- Recover transient failures without creating duplicate logical jobs or silently overwriting output.
- Preserve batch identity and progress across app restarts.
- Use backend-authored view models and opaque command tokens so the frontend never invents authority.
- Keep telemetry privacy-safe and avoid URL, video ID, title, and filesystem path collection.

## 4. Non-goals

- Playlist URLs in the batch composer.
- Authenticated/private/member-only downloads or browser-cookie import.
- Reordering or reprioritizing children within a batch.
- Migrating an existing failed job and its reservation to a different output root.
- Deduplicating against queue history, completed downloads, or other batches.
- Per-item format selection within a batch.
- Cloud telemetry for batch usage or successful/recovered jobs.
- A full localization framework or translated copy.

## 5. UX specification

### 5.1 Home flow

Add a **Single URL / Batch URLs** mode control to Home. Preserve the current single-URL flow unchanged by default.

The Batch URLs mode has four UI states:

1. **Input**
   - Multiline URL field with one URL per line.
   - Clear 2–20 item guidance.
   - Analyze action disabled until the client sees at least two non-empty lines, while the backend remains authoritative.
2. **Analyzing**
   - Disable duplicate submissions.
   - Show bounded progress without presenting unverified lines as valid.
3. **Review**
   - Remain on Home; do not render Queue jobs or Queue navigation state in this view.
   - Show one compact outcome summary beneath **Review URLs**. When every line is ready, use copy such as `2 videos ready to download`; for mixed results, show only non-zero ready, duplicate, invalid, and analysis-failed counts.
   - Do not repeat those values in a second summary strip; the primary action retains the admitted count because it confirms the action's scope.
   - Show every original non-empty line in input order with a status and safe explanation.
   - Show a 16:9 thumbnail for every successfully analyzed video, with a deterministic YouTube thumbnail fallback and a neutral placeholder when an image is unavailable. Keep non-ready rows aligned using the same placeholder slot.
   - For a duplicate, identify the first matching line, for example `Duplicate of line 1`.
   - Provide **Edit lines** to return to input and invalidate the current review/token.
   - Show one format selector and the current default output folder with **Change…**.
   - Label the primary action with its concrete ready count, for example **Start 3 downloads**.
   - Enable the primary action only when at least two items are ready and the analysis token remains valid.
4. **Started**
   - Confirm the admitted count.
   - Navigate to Queue and focus or reveal the new batch parent, initially expanded.
   - Do not carry the validation summary or review actions into Queue.

Approved review structure:

```text
Review URLs
3 ready · 1 duplicate · 1 invalid

1  [thumbnail]  youtube.com/...       Ready
2  [placeholder] youtu.be/...         Duplicate of line 1
3  [thumbnail]  youtube.com/...       Ready
4  [placeholder] example.com/...      Invalid URL
5  [thumbnail]  youtube.com/...       Ready

Format       [Video | Audio]
Save to      /Users/...                 [Change…]

[Edit lines]                         [Start 3 downloads]
```

The displayed URL must be a safe, visually truncated representation of the original line. It must not become telemetry or diagnostic data.

Suggested components:

- `frontend/src/lib/batch/BatchURLComposer.svelte`
- `frontend/src/lib/batch/BatchURLReview.svelte`
- Reuse the current polished playlist-review format and output-folder controls rather than introducing parallel policy UI.

### 5.2 Queue flow

After admission, use the normal Queue page and its current polished shell:

```text
Queue

Batch download · 3 videos
2 of 3 complete
  ├─ Completed video
  ├─ Completed video
  └─ Paused after restart             [Resume] [Cancel]
```

- Render a durable expandable parent titled from backend-authored collection data, with a fallback such as `Batch download · 3 videos`.
- Derive the parent total, completion text, and progress bar from the same durable ordered child set. Never display contradictory totals.
- Summarize child state accurately: queued, active, completed, action required, and failed.
- Display every batch child in stable input order and make collection membership visually unambiguous.
- Use indentation, a subtle collection rail/background, or an equivalent grouping treatment; include clear spacing after the last child so the next standalone job cannot appear to belong to the batch.
- Open the newly admitted batch expanded. Keep the parent easy to collapse because the MVP permits up to 20 children.
- Do not remove successful children when siblings fail or retry.
- Completed progress bars must be fully filled or omitted. Parent and child progress visuals must agree with their authored text.
- Treat **Remove** as a backend-authored capability with explicit semantics. If ambiguity remains, confirmation copy must state whether unfinished children are cancelled and that completed files remain on disk.
- Provide accessible text/icon state indicators; color must not be the only signal.
- Ensure keyboard access and visible focus for expansion and recovery actions.

### 5.3 Layout behavior

- Use the existing shared application shell and current polished Queue/Home dimensions; do not hard-code the older feature board’s 1280×800 canvas or 160px sidebar.
- Preserve the established 32px main-content padding and spacing rhythm where the active shell permits it.
- Let the main page own vertical scrolling. Avoid a second nested scrollbar inside the batch collection.
- Allow Home header/actions and review controls to wrap cleanly at the application’s supported minimum window width.
- Truncate long URLs and video titles before they collide with status or action controls, while preserving the full accessible label where safe.
- Use responsive/fill sizing for the summary and review list rather than the feature board’s fixed 560px summary strip plus disconnected validation copy.
- Target approximately 40–44px for interactive controls and retain visible keyboard focus.
- Use normal body sizing for important validation and recovery guidance; do not rely on 11px tertiary text for required actions.

### 5.4 Failure presentation

Each failed row receives a backend-authored failure projection:

```go
type QueueFailure struct {
    Category          string `json:"category"`
    MessageKey        string `json:"messageKey"`
    Heading           string `json:"heading"`
    Message           string `json:"message"`
    RecommendedAction string `json:"recommendedAction"`
    Retryable         bool   `json:"retryable"`
    PartialOutput     bool   `json:"partialOutput"`
}
```

The frontend renders only actions represented by backend-issued capabilities and opaque command tokens.

## 6. Backend design

### 6.1 Generalize durable collections

The existing collection model is playlist-specific. Generalize it so playlist and pasted batches share queue projection and lifecycle aggregation without conflating their admission flows.

Planned model changes:

- Add collection kind: `playlist` or `batch`.
- Retain stable collection ID, ordered child IDs, created time, and source metadata appropriate to the kind.
- Keep playlist-only fields optional and valid only for playlist collections.
- Add batch-safe display metadata that does not require storing the pasted raw text.
- Normalize existing State v2 playlist collections during load so current persisted state remains valid.
- Keep state validation strict: valid kind, unique child IDs, existing children, and stable ordering.

Do not persist the full pasted text or raw per-line URLs in the collection merely for UI display. Child jobs already retain the source required by the existing durable job model.

### 6.2 Batch analysis API

Add a trusted backend entry point, tentatively:

```go
AnalyzeBatchURLs(rawText string) (BatchAnalysisView, error)
```

Responsibilities:

1. Enforce a bounded request size before parsing.
2. Split by line, trim whitespace, and ignore blank lines.
3. Enforce 2–20 non-empty lines.
4. Validate each line through the existing URL validation boundary.
5. Accept only `single_video` inputs; reject playlists and unsupported hosts.
6. Canonicalize valid YouTube URLs and deduplicate by video ID within this analysis.
7. Analyze unique valid items independently using bounded concurrency (initially four workers).
8. Preserve original line order in the returned review model.
9. Return only frontend-safe analysis data plus an opaque, expiring analysis token.

Planned response shape:

```go
type BatchAnalysisView struct {
    Token       string                  `json:"token"`
    ExpiresAt   time.Time               `json:"expiresAt"`
    Counts      BatchAnalysisCounts     `json:"counts"`
    Items       []BatchAnalysisItemView `json:"items"`
    Policies    BatchPolicyView         `json:"policies"`
}
```

Per-line statuses:

- `ready`
- `duplicate`
- `invalid`
- `analysis_failed`

The token maps to an in-memory private plan containing trusted canonical identities and engine analysis. Proposed expiry: 30 minutes. The cache must be bounded, concurrency-safe, pruned, and invalidated after successful admission. Expiry or restart requires reanalysis.

### 6.3 Batch policy

- One user-selected format/output policy applies to all ready children.
- Resolve each child’s concrete media selection on the backend because available formats may differ by video.
- Reuse the current output-root setting, including the current `perVideoSubfolder` behavior.
- Reject stale, unknown, consumed, or policy-incompatible analysis tokens.
- Never accept client-supplied engine metadata, canonical IDs, output paths, or reservation paths as trusted admission inputs.

### 6.4 Atomic admission

Add a dedicated batch admission path rather than disguising pasted URLs as a playlist.

Tentative entry point:

```go
StartBatchDownload(token string, policy BatchStartPolicy) (CollectionQueueView, error)
```

Admission sequence:

1. Resolve and validate the private analysis plan.
2. Require at least two ready unique items.
3. Revalidate the selected policy and current output-root configuration.
4. Construct every logical job, initial attempt/session identity, output artifact, and reservation.
5. Validate the full collection and all jobs before mutation.
6. In one State v2 transaction, persist:
   - the batch collection parent;
   - every child logical job;
   - every initial attempt/session record;
   - every output reservation.
7. After the durable commit, submit children to the live queue in stable FIFO order.
8. Consume the analysis plan only after successful durable admission.

If live submission fails after the durable commit, retain a recoverable pending batch/jobs state for startup reconciliation. Never partially roll back by deleting an already durable subset.

### 6.5 Restart and reconciliation

- Restore batch collections and their ordered children from State v2.
- Feed each nonterminal child through existing queue/startup reconciliation rules.
- Derive parent progress from durable children; do not maintain a second mutable parent counter.
- A child retry retains the same logical job ID and batch membership while creating a new attempt as existing retry semantics require.
- Completed siblings remain completed and untouched.

## 7. Failure taxonomy and recovery

Classify failures at typed boundaries. Do not parse user-facing error strings.

Initial categories:

| Category | Retryable | Recommended behavior |
|---|---:|---|
| `network_interrupted` | Yes | Resume/retry the same logical job using existing validated-session escalation. |
| `authentication_required` | No | Explain that authenticated downloads are unsupported; offer Open source and Remove. |
| `resource_unavailable` | No | Explain removed/private/blocked media; offer Open source, Copy link, and Remove as supported. |
| `disk_full` | No in-place retry | Ask the user to free space or change the default folder, then start again. |
| `permission_denied` | No in-place retry | Ask the user to choose/fix an accessible default folder, then start again. |
| `security_blocked` | No | Preserve existing safe security guidance and allowed actions. |
| `retry_exhausted` | No automatic retry | Explain that retry limits were reached and expose only supported next steps. |
| `internal` | Policy-dependent | Give a safe generic message and existing diagnostics/reporting action where available. |
| Existing reconciliation categories | Existing policy | Preserve current action-required recovery contracts. |

Implementation notes:

- Map current engine categories such as network, authentication, unsupported, invalid input, security, cancelled, and internal into this stable UI taxonomy.
- Detect disk-full and permission failures from wrapped OS errors (`errors.Is`, `os.ErrPermission`, `ENOSPC`, and platform equivalents) or new typed write/finalize errors.
- Apply this projection to every queue job so single, playlist, and batch experiences remain consistent.
- Capabilities remain authoritative. `Retryable` is explanatory, not permission by itself.

### 7.1 Start-again flow for disk/permission failures

Because output reservations are bound during admission, this release will not migrate a failed job to a new root.

**Start again** should:

1. Confirm that the failed row is eligible for replacement.
2. Safely settle/remove the old failed job and release its reservation through the queue authority.
3. Return its source URL to Home for fresh analysis using the current default folder.
4. Admit a new standalone logical job only after normal user confirmation.

This ordering prevents accidental suffix duplicates or two active owners for the same reservation. A failed batch child’s replacement is a standalone job in this release; inserting a replacement child into the historical batch is deferred.

## 8. Frontend/API integration

Planned updates:

- Add batch analysis/admission request and response types in `frontend/src/lib/types.ts`.
- Add Wails wrappers in `frontend/src/lib/api.ts` and regenerate bindings where required.
- Add Home state management with stale-request protection so a late analysis cannot overwrite edited input.
- Add Queue failure DTO support to lifecycle types and row components.
- Keep all mutation actions routed through opaque backend commands.
- Announce analysis results, admission failures, and recovery state changes through the existing accessible status/live-region patterns.
- Avoid embedding English decision logic in Svelte; use backend stable keys and authored display copy.

## 9. Privacy-safe local measurements

Do not overload the diagnostics uploader. The current diagnostics contract is for terminal-failure diagnostics and explicitly excludes general usage, successes, retries, and recoveries.

If measurement ships with this feature, implement a separate local-only aggregate store containing only bounded counters/duration buckets such as:

- batch analyses started/completed;
- ready/duplicate/invalid/analysis-failed count buckets;
- batches admitted and admitted-size buckets;
- child terminal outcome categories;
- retry/recovery outcome counts;
- coarse analysis/admission duration buckets.

Never record:

- raw or canonical URLs;
- video IDs;
- titles/channel names;
- output paths/folder names;
- thumbnails or media metadata;
- free-form engine errors.

Requirements:

- Owner-only permissions.
- Atomic writes.
- Bounded schema and file size.
- Corruption-safe reset/normalization.
- No upload path in this release.

If the aggregate store threatens the core feature schedule, keep instrumentation hooks stable but defer persistence behind an explicit follow-up decision rather than weakening the privacy contract.

## 10. Validation plan

### 10.1 Backend unit tests

- Parsing CRLF/LF input, whitespace, blank lines, and bounded request size.
- Minimum/maximum count boundaries: 1, 2, 20, and 21 lines.
- Individual video/Short acceptance and playlist rejection.
- Canonical deduplication across `youtu.be`, watch, Shorts, and query variants.
- Original-order preservation.
- Independent analysis with mixed ready/invalid/failed results.
- Concurrency limit and cancellation.
- Token expiry, unknown token, consumed token, cache pruning, and replay prevention.
- Policy tampering and stale-policy rejection.
- Per-child format resolution.
- State normalization for existing playlist collections.
- Batch collection validation and stable child ordering.
- All-or-nothing durable admission under injected failures at each transaction stage.
- Post-commit live-submit failure and restart reconciliation.
- Output reservation conflicts and no silent overwrite.

### 10.2 Queue/recovery tests

- A failed child does not affect completed or active siblings.
- Retry uses the same logical job ID and a new attempt ID.
- Resume validation and fresh-session escalation preserve the reservation.
- Category-to-message/action mapping for every taxonomy value.
- Capability tokens agree with rendered actions.
- Auth/unavailable failures never expose Retry.
- Wrapped ENOSPC and permission errors map correctly.
- Start again releases the old reservation before replacement admission.
- Restart restores the expandable parent and accurate aggregate progress.
- Existing single and playlist queue behavior remains unchanged.

### 10.3 Frontend tests

- Single/Batch mode behavior and default mode.
- Input, analyzing, review, stale response, expiry, and started states.
- Mixed result counts and line statuses.
- Start disabled with fewer than two ready items.
- Edit lines invalidates the old review state.
- One policy selector affects all ready items.
- Validation review remains on Home and is absent from Queue after admission.
- Every input line remains visible in review; duplicates identify the first matching line.
- Primary admission copy reflects the ready count.
- Parent expansion, unambiguous child grouping, and accurate completed/total labels.
- Completed and parent progress visuals agree with their text.
- Failure headings, descriptions, and capability-backed actions.
- Keyboard navigation, visible focus, live announcements, and non-color state cues.
- Narrow and standard desktop layouts using the current polished shared shell rather than fixed feature-board dimensions.

### 10.4 Required validation commands

Run the repository’s canonical formatting, generated-binding, Go test, frontend test, lint/typecheck, and build commands documented on the implementation branch at that time. Record exact commands and results in Section 13 before completion.

## 11. Delivery sequence

- [x] **Phase 1 — Contracts and persistence**
  - Generalized durable collections with backward-compatible playlist normalization.
  - Added batch/failure DTOs, stable message keys, strict validation, and contract tests.
- [x] **Phase 2 — Analysis service**
  - Added bounded parsing, canonical dedupe, four-worker analysis, cancellation handling, bounded expiring single-claim tokens, and tests.
- [x] **Phase 3 — Atomic admission**
  - Added per-child plan/root resolution, one-transaction collection admission, FIFO submission, and durable post-commit recovery behavior.
- [x] **Phase 4 — Home UI**
  - Added mode control, composer, line-level review, shared policy/folder controls, token expiry, stale-response protection, and accessibility tests.
- [x] **Phase 5 — Queue parent**
  - Added durable batch parents/children with initial expansion, stable order, grouping, and consistent aggregate progress/title projection.
- [x] **Phase 6 — Failure taxonomy and recovery**
  - Added typed failure projection, stable copy keys, capability-aware actions, permanent auth/unavailable handling, and safe disk/permission start-again.
- [x] **Phase 7 — Local aggregate measurements (explicitly deferred)**
  - No usage store or upload path ships in this change. This avoids weakening the existing diagnostics privacy contract; instrumentation can be proposed separately.
- [x] **Phase 8 — Hardening and release validation**
  - Automated regression, race, persistence, accessibility, production build, documentation validation, and manual in-app comparison are complete.

## 12. Completion criteria

The feature is complete only when:

- A user can analyze and atomically start 2–20 ready individual public video/Short URLs from Home.
- Duplicate and invalid lines are explained before admission.
- A durable expandable batch parent survives restart and reports accurate child outcomes.
- Successes are preserved when siblings fail.
- Network recovery does not create a second logical job or duplicate output.
- Auth and permanent-unavailable failures do not offer false retry actions.
- Disk/permission guidance and start-again behavior cannot silently overwrite or create conflicting reservations.
- Single-download and playlist flows pass regression tests.
- The UI matches the approved Home-review and Queue-after-admission structures in Section 5, uses the current polished shared shell, and meets accessibility expectations.
- No URL, video identity, title, or path is added to diagnostics or product measurements.
- All required validation is green and recorded below.

## 13. Implementation log and validation record

Keep this section current throughout implementation.

### Progress log

- 2026-08-22: Reviewed the PRD, Penpot boards, current frontend/backend architecture, collection admission, retry/session semantics, State v2, reservations, and diagnostics contract. Confirmed scope decisions with the product owner. Created this implementation branch and plan. No feature implementation started.
- 2026-08-22: Reviewed the linked Batch URLs Main board in context with the full MVP frame and the current polished Queue/playlist layouts. Product owner approved the revised Home review and Queue-after-admission structures.
- 2026-08-22: Implemented bounded batch parsing/analysis, canonical within-batch dedupe, expiring single-claim analysis authority, atomic durable admission, generalized playlist/batch collection persistence, and per-video-subfolder reservation roots.
- 2026-08-22: Implemented Home composer/review states, batch-wide policy and folder controls, durable Queue parent/children, accurate progress, typed failure projections, capability-backed recovery actions, and disk/permission Start again.
- 2026-08-22: Added backend, persistence, race, frontend DOM/accessibility, and production Wails build coverage. Deferred local usage aggregates rather than coupling them to terminal diagnostics.
- 2026-08-22: Reviewed and merged startup recovery PR #76, then rebased this branch onto the resulting `origin/main` merge commit `43c7d24`.
- 2026-08-22: Manually tested a mixed five-line batch in the production macOS app: two ready videos, one canonical duplicate, one invalid host, and one safe analysis failure. Started both ready downloads, verified the expanded durable parent and stable child order, completed both files, restarted the app, and verified the restored `2 of 2 complete` collection.
- 2026-08-22: Manual review exposed stale speed/ETA text on a completed child. Updated the backend queue projection to clear live transfer telemetry for completed rows and added a regression test.
- 2026-08-22: Product review identified redundant review counts and missing analyzed-video artwork. Removed the duplicate count strip, made the remaining summary conditional and outcome-focused, added aligned 16:9 thumbnails/placeholders, and added a backend YouTube-thumbnail fallback plus regression coverage.

### Plan changes

- 2026-08-22: Separated pre-admission review from Queue, adopted line-level validation with duplicate source-line references, added batch-wide format/folder placement, clarified collection grouping and progress consistency, and made the current polished shell authoritative for responsive layout.

### Validation results

- `gofmt -w ...` — passed for all changed Go files.
- `go test -count=1 ./...` — passed across all Go packages.
- `go test -race -count=1 ./...` — passed across all Go packages.
- `cd frontend && npm run check` — passed with 0 errors and one pre-existing unused-selector warning in `About.svelte`.
- `cd frontend && npm run test:ui` — passed: 27 Node contract tests and 43 Vitest DOM tests.
- `cd frontend && npm run build` — passed.
- `go vet ./...` — passed.
- `go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build -clean` — passed; produced the signed macOS application bundle.
- Launched the production bundle with an isolated Home directory and visually verified the current 1280×800 polished shell and Single URL / Batch URLs mode control.
- Manual mixed-result review — passed with 5 pasted, 2 ready, 1 duplicate, 1 invalid, and 1 analysis-failed line; raw extraction detail remained hidden.
- Manual real-download admission — passed; both ready videos were admitted under one expanded batch parent, completed successfully, and used separate per-video output folders.
- Manual restart recovery — passed; the durable batch parent restored with stable order, full child progress, and `2 of 2 complete`.
- Manual expand/collapse accessibility — passed through the semantic button controls and updated accessible labels.
- Screenshots: `docs/assets/reliable-batch-downloads-review.png` and `docs/assets/reliable-batch-downloads-queue.png`.
- `git diff --check` — passed.

### Known residual risks

- Batch analysis metadata is intentionally ephemeral; an app restart before admission requires reanalysis.
- Disk/permission recovery creates a new standalone job rather than preserving historical batch membership.
- Wrapped `ENOSPC` and permission errors are mapped and tested, but a future engine stage that discards its wrapped OS cause could still fall back to the safe internal category.
- Batch-wide format intent is resolved independently per child, so concrete stream details can differ while honoring the selected cap/policy.
- Privacy-safe local usage aggregates were deliberately deferred; this release adds no product-measurement persistence or upload path.
