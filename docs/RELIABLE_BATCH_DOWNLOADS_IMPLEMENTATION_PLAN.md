# Reliable Batch Downloads — Implementation Plan

**Status:** Planning complete; implementation not started  
**Branch:** `feat/reliable-batch-downloads`  
**Base:** `origin/main` at `1007c0b`  
**Last updated:** 2026-08-22

This is the living implementation plan for reliable batch downloads. Keep the status, checklists, decisions, and validation record current as the feature is implemented.

## 1. Source material

- Product plan: `/Users/tejas/vidstow-reliable-batch-downloads-v2.html`
- UI reference: Penpot project **VidStow · Product UI**
  - **Reliable batch downloads — exploration**: `206f7735-38a5-80c3-8008-8421bf7c2072`
  - **Batch URLs — MVP**: `b44c5d20-92db-805f-8008-8498f2b0d2e9`
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
   - Show counts for pasted, ready, duplicate, invalid, and analysis-failed lines.
   - Show every line with a status and safe explanation.
   - Provide **Edit lines** to return to input.
   - Show one format selector and the current default output folder.
   - Enable **Start batch** only when at least two items are ready and the analysis token remains valid.
4. **Started**
   - Confirm the admitted count.
   - Navigate to Queue and focus or reveal the new batch parent.

Suggested components:

- `frontend/src/lib/batch/BatchURLComposer.svelte`
- `frontend/src/lib/batch/BatchURLReview.svelte`
- Reuse an existing output/format selector where practical rather than introducing parallel policy UI.

### 5.2 Queue flow

- Render a durable expandable parent titled from backend-authored collection data, with a fallback such as `Batch download · 3 URLs`.
- Summarize child state accurately: queued, active, completed, action required, and failed.
- Display children in stable input order.
- Do not remove successful children when siblings fail or retry.
- Normalize the mockup inconsistency: displayed totals and `x of y complete` always derive from durable children.
- Provide accessible text/icon state indicators; color must not be the only signal.
- Ensure keyboard access and visible focus for mode selection, expansion, review rows, and recovery actions.

### 5.3 Failure presentation

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
- Parent expansion and accurate completed/total labels.
- Failure headings, descriptions, and capability-backed actions.
- Keyboard navigation, visible focus, live announcements, and non-color state cues.
- Narrow and standard desktop layouts against the Penpot reference.

### 10.4 Required validation commands

Run the repository’s canonical formatting, generated-binding, Go test, frontend test, lint/typecheck, and build commands documented on the implementation branch at that time. Record exact commands and results in Section 13 before completion.

## 11. Delivery sequence

- [ ] **Phase 1 — Contracts and persistence**
  - Generalize durable collections and add backward-compatible normalization.
  - Define batch/failure API DTOs and stable message keys.
  - Add schema and contract tests.
- [ ] **Phase 2 — Analysis service**
  - Add parser, canonical dedupe, bounded concurrent analysis, private expiring plan cache, and tests.
- [ ] **Phase 3 — Atomic admission**
  - Build all children/reservations, persist in one transaction, submit FIFO, and cover failure injection/restart.
- [ ] **Phase 4 — Home UI**
  - Add mode control, composer, review, common policy selection, admission flow, and accessibility tests.
- [ ] **Phase 5 — Queue parent**
  - Render durable batch parents/children with accurate aggregate progress and restart behavior.
- [ ] **Phase 6 — Failure taxonomy and recovery**
  - Add typed classification, backend failure projection, capability-aware actions, and safe start-again flow.
- [ ] **Phase 7 — Local aggregate measurements**
  - Add privacy-safe local counters only if the constraints in Section 9 can be met without expanding scope.
- [ ] **Phase 8 — Hardening and release validation**
  - Regression, race, persistence, accessibility, full build/test, manual Penpot comparison, and documentation updates.

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
- The UI meets the supplied Penpot behavior and accessibility expectations.
- No URL, video identity, title, or path is added to diagnostics or product measurements.
- All required validation is green and recorded below.

## 13. Implementation log and validation record

Keep this section current throughout implementation.

### Progress log

- 2026-08-22: Reviewed the PRD, Penpot boards, current frontend/backend architecture, collection admission, retry/session semantics, State v2, reservations, and diagnostics contract. Confirmed scope decisions with the product owner. Created this implementation branch and plan. No feature implementation started.

### Plan changes

- None yet.

### Validation results

- Not run; implementation has not started.

### Known residual risks

- Batch analysis metadata is intentionally ephemeral; an app restart before admission requires reanalysis.
- Disk/permission recovery creates a new standalone job rather than preserving historical batch membership.
- Current engine/platform error wrapping may need strengthening to guarantee typed disk classification on every output stage.
- Batch-wide format intent must be resolved independently per child without surprising quality differences; final UI copy should make this clear.
