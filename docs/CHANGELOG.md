# Changelog

All notable changes to PAIMOS are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Three version eras appear in one sequence and are never retagged: legacy
Semantic Versioning (`x.y.z`, until 5.21.0), INSPR calendar v1
(`yy.mm.dd[.hh.mm]`, 26.08.31 – 26.09.09.13.13), and INSPR calendar v2
(`YYMMDDhhmmss.0.0`, the UTC reservation second as a SemVer-shaped
coordinate, from the first entry below that form).

## [Unreleased]

### Added

- Add the `AUTH_PASSWORD_LOGIN=enabled|disabled` operator switch so an instance can authenticate only through its configured OIDC provider. The default stays `enabled`, leaving existing deployments unchanged (PAI-1044).

### Security

- With `AUTH_PASSWORD_LOGIN=disabled`, every password-credential path fails closed in the handlers — password login, pending password-TOTP challenges, password reset issuance and redemption, dev login, `ChangePassword` and `TOTPDisable` — returning the same generic error as a wrong password. API-key authentication stays available as the break-glass path (PAI-1044).
- Refuse to start with password login disabled unless an active user has an OIDC-matchable email or a usable recovery API key exists, and require an HTTPS issuer unless the host is loopback (PAI-1044).
- Clear `must_change_password` when a user signs in through OIDC, so invited users are not trapped behind a password change they cannot make (PAI-1044).

### Changed

- Re-vendor the INSPR calendar-version presentation bundle at inspr `317f872`, adding `schemes.json`; all existing payloads are byte-identical (PAI-1045).
- Bump the doctrine pin to inspr-modules `v260921084702.0.0` and wire the `/inspr-versioning` command (PAI-1046).

## [260919160247.0.0] — 2026-09-19

### Fixed

- Allow owned Codex and Claude native messages to proceed while heartbeat and control reporting is busy, retaining first-heartbeat confirmation and exact sender authority (PAI-1041).
- Use already-confirmed worker proof for native sends and clear it on session closure, avoiding lease recreation from message calls (PAI-1041).

## [260919135916.0.0] — 2026-09-19

### Fixed

- Preserve the sender, message ID, ticket context and reply expectation when delivering messages into owned Codex and Claude inboxes, so agents can identify and thread native replies (PAI-1041).
- Include the attributed sender's configured return address, escape all context attributes and keep private runtime references outside model-visible inbox messages (PAI-1041).

## [260919114708.0.0] — 2026-09-19

### Added

- Let owned Codex and Claude agents send bounded native messages and threaded replies under their current worker identity, preserving receiver allowlists, reply hops, rate limits and human action holds (PAI-1041).
- Add internal support for restricted native Grok conversations and an explicitly bound private source-built variant (PAI-1028).

### Fixed

- Ship the pinned native message schema with the Claude bridge so standalone SDK installations work without a separate Zod peer. Verify its integrity, preserve its license and check the actual SDK tool protocol before deployment (PAI-1041).
- Keep native responses off vendor output readers, exclude echoed message arguments from runtime journals, bound startup waits and scope replay keys to each worker generation (PAI-1041).
- Expose bounded owned-runtime renewal diagnostics (PAI-1030).

### Changed

- Require DCO sign-offs for new contributions and link the active contribution ruleset (PAI-1039).

## [260919111200.0.0] — 2026-09-19

### Added

- Let owned Codex and Claude agents send bounded native messages and threaded replies under their current worker identity. The server rechecks worker authority and project permissions; receiver allowlists, reply hops, rate limits and human action holds still apply (PAI-1041).
- Add internal support for restricted native Grok conversations and an explicitly bound private source-built variant (PAI-1028).

### Fixed

- Keep native tool responses off vendor output readers and exclude echoed message arguments from runtime activity and journals. Bound startup waits and scope native replay keys to each worker generation (PAI-1041).
- Expose bounded owned-runtime renewal diagnostics without disclosing provider credentials or private session references (PAI-1030).

### Changed

- Require DCO sign-offs for new contributions and link the active contribution ruleset (PAI-1039).

## [260916122330.0.0] — 2026-09-16

### Added

- Return a canonical, server-accepted lifecycle readiness receipt to the owned `paimos-agentd` after an authorized transition. Its private socket supports only exact tuple lookup bound to the current daemon generation, account, immutable profile, workspace identity and mode, baseline, and expiry (PAI-1036).

### Fixed

- Reconcile the daemon-owned workspace availability immediately before Start, preserving the server’s final downgrade or expiry state and rejecting stale or mismatched readiness evidence (PAI-1035).

## [260915004615.0.0] — 2026-09-15

### Added

- Connect Aithema to a current native Codex account from Settings → Account, with explicit project and actor bindings, encrypted credential download, and existing credential revocation controls (PAI-1029).
- Refresh account and runtime authority before enrollment and reject stale selections or mismatched credential responses (PAI-1029).

### Fixed

- Offer conversation enrollment only after the owned worker has initialized its configured conversation consumer. Keep coding-only workers available for coding without implying conversation support (PAI-1030).
- Revalidate the exact conversation account, attachment revision, dispatch profile, policy and output limits when enrolling and using a binding; require re-enrollment after the configured worker is refreshed (PAI-1030).

## [260914224513.0.0] — 2026-09-14

### Added

- Connect Aithema chat and structured understanding to explicitly bound native Codex accounts through a scoped conversation service, with project/actor authorization, immutable account/model bindings and bounded restricted execution (PAI-1027).

### Fixed

- Reconcile failed and cancelled conversations without replaying inference, losing terminal evidence or holding account slots after the owned process exits (PAI-1027).
- Complete release cleanup when the main branch belongs to another worktree (PAI-1026).

## [260914132108.0.0] — 2026-09-14

### Changed

- Use the shared INSPR Calendar Versioning display in the sidebar and authentication footers, with Pretty/SemVer selection, canonical version copying and product branding (PAI-1025). Verify the pinned renderer, configuration and license files offline before builds.

### Fixed

- Show current, fulfilled Janus prerequisites in the delivery flow without treating them as user access authorization (PAI-967).
- Use the reported batch forecast for Current batch when no separate task forecast exists (PAI-967).

## [260913202224.0.0] — 2026-09-13

### Added — Retry-safe terminal evidence (PAI-1024)

- Expose owner-authorized terminal external-stage handoff evidence with the exact delivery, execution, authority, reporter, credential and terminal-report bindings required for a fresh retry.

### Fixed

- Accept a producer’s causal direct transition from accepted to succeeded or failed while preserving sequence, epoch, and typed-evidence checks (PAI-1022).
- Keep the v6 home link inside a configured public base path instead of navigating to the origin root (PAI-972).
- Isolate dependency-only database race checks while retaining the full required backend release assurance surface (PAI-1003).
- Restore the immutable v1 external-stage contract source while retaining the additive terminal-evidence API in an unpinned declaration file (PAI-1024).

## [260913075842.0.0] — 2026-09-13

### Fixed — Native baseline review handoff (PAI-967)

- Route Flow’s Review batch action directly to Paimos’s native baseline review controls and use concise review labels, while Start batch remains subject to the existing review and authority gates.

## [260913063837.0.0] — 2026-09-13

### Fixed

- Accept valid PNG and other binary raster attachments containing inert SVG or HTML metadata, including C2PA icon previews. Explicit active content types and disguised markup remain rejected (PAI-1019).

### Added

- Export newly enrolled machine-notifier credentials through encrypted enrollment delivery (PAI-1021).

## [260912203754.0.0] — 2026-09-12

### Added — Target-bound machine notifier credentials (PAI-1018)

- Let administrators enroll expiring, revocable machine-notifier credentials bound to one active project, owning user, sender, receiver and exact enabled simple-webhook target version.
- Limit those credentials to text-only message creation and content-free delivery receipts for their own messages; reject caller-supplied attribution and every unrelated API route.
- Recheck the credential, owner, project, sender-to-receiver allowlist and pinned target before webhook handoff, so revoked authority or target rotation stops queued delivery without falling through to another target.

## [260912053553.0.0] — 2026-09-12

### Fixed — Immutable requirements revision (PAI-1015)

- Preserve the approved Aithema baseline revision when starting a delivery batch and return it unchanged from later reads, lists and restarts.
- Keep historical batch identity independent of subsequent draft edits and requirements imports.
- Recover older batch revisions only from an exact sealed snapshot; leave unavailable historical revisions explicitly unknown.

## [260911190918.0.0] — 2026-09-11

### Added — Offer acceptance confirmations and overview status (PAI-1012)

- Show offer statuses in customer and offer overviews; preserve each offer’s state when a customer has multiple offers.
- Freeze validated contact and sender email addresses at finalization and send a joint acceptance confirmation with customer link and the stored accepted PDF.
- Stamp the customer signature area with acceptance metadata and “Annahmenachweis (SHA-256):”; share the same document renderer between the browser and server PDF.
- Preserve acceptance across PDF or email failures with bounded retries and explicit handling of uncertain SMTP delivery.
- Let drafts be marked deleted and finalized offers archived through a reversible flag, with a default-off deleted filter and restore action.

## [260911172741.0.0] — 2026-09-11

### Added — Reviewed delivery launch (PAI-978)

- Request a single-use Pharos launch only for the reviewed delivery, selected worker accounts and current authority; stale selections, changed candidates and any second launch are refused, while an exact retry returns the original admission or receipt.
- Publish the versioned launch-admission contract and CLI transport while retaining explicit release-scheme discrimination.

### Improved — Account and worker lifecycle (PAI-984, PAI-985, PAI-986)

- Record the effective Claude model returned by the running harness instead of presenting the requested model as execution evidence.
- Attach and detach enrolled accounts through reviewed lifecycle actions bound to the current account attachment revision.
- Retire workers with durable intent and recovery state, and make unfinished-work disposition explicit in the Habitat removal flow.

### Fixed — Browser-guard runtime diagnostics (PAI-981)

- Accept the declared agent browser-guard LaunchAgent environment on macOS only when every guarded path points at the exact refusal shim; any other LaunchAgent environment override is still refused.

### Fixed — Integration compatibility

- Preserve current Flow, release tooling and test-database improvements while adding launch and retirement migrations after the already-published migration sequence.
- Keep Unix transport tests scoped to the platforms supported by the production transport.

## [260911160144.0.0] — 2026-09-11

### Changed — Reviewed release preparation (PAI-1008)

- Prepare release metadata locally for review, then resume publication only from the exact reviewed commit while failing closed on main drift.

### Changed — Draft QR preview (PAI-1006)

- Show the customer QR and link area in drafts with clearly labelled preview data, while keeping real public links unavailable until finalization.
- Preserve the same QR layout in editor and PDF output; final documents continue to show the real customer link without the preview badge.

## [260911125749.0.0] — 2026-09-11

### Changed — Offer brand dots (PAI-1007)

- Reuse one three-circle brand mark beside the offer title and Roman section headings, keeping the circles identical, round and aligned with the heading text.
- Show a smaller grey variant before the unchanged footer logotype while preserving pagination, accessible heading names and sender-specific footers.

### Changed — Release backend assurance scheduling (PAI-1003)

- Run exhaustive backend assurance nightly and for the exact immutable release merge before tagging instead of repeating it after every main merge.
- Keep release publication fail-closed on successful full execution for the exact release commit.

## [260911121105.0.0] — 2026-09-11

### Changed — Compact offer editor (PAI-991)

- Collapse workspace and offer chrome into a slim, expandable toolbar with SVG actions and page-width, whole-page and percentage zoom.
- Keep save feedback visible with a one-second activity indicator, successful-save timestamp and a manual save action that preserves revision checks.
- Distinguish failed saves and invalid inputs from saved state; clarify that customer links and QR codes are created when an offer is finalized.
- Match footer logo height to the footer text and separate the net total with one full-width rule while retaining position separators.

### Changed — Frontend release assurance (PAI-998)

- Reuse successful ancestral full backend execution only for eligible frontend and release-metadata changes, retaining exact-head contracts and failing closed for unknown inputs.

### Improved — Faster test and build feedback (PAI-999, PAI-1000, PAI-1001, PAI-1002, PAI-1004, PAI-1005)

- Give database and handler shards sufficient timeout headroom while preserving the other lane budgets.
- Remove duplicate backend executions, plan affected PR lanes once and avoid allocating runners for empty lanes.
- Reuse migrated template databases in backend tests with pinned fixture dependencies and inexpensive integrity hashes.
- Remove tests that only enforce documentation prose while retaining schema and behavior checks.
- Build the production SPA from the real repository context so required release metadata is available without copying a synthetic frontend-only tree.

### Maintenance — Intake attribution comments (PAI-997)

- Credit start-agm-com in the audio and TTS implementation comments after its rename.

## [260911063734.0.0] — 2026-09-11

### Fixed — Offer section headings and pagination (PAI-991)

- Number the conditions and performance sections with Roman numerals and show each heading only once, at the start of its section.
- Match the spacing below section headings to the numbered-block gap and preserve a consistent top inset on continuation pages.
- Measure section and table headers during pagination, retaining the print guard for content too large to fit on one page.

## [260911005821.0.0] — 2026-09-11

### Changed — Final calendar version display (PAI-994)

- Render calendar version labels with the approved revision 3 segment weights and sun-coloured date tint from the pinned INSPR display data. Canonical version strings, copyable values and legacy labels stay unchanged.
- Verify the vendored display data against its immutable upstream revision in offline builds and initialized checkouts.
- Include the display changes from the unpublished `260910221338.0.0` reservation. Its tag audit did not finish before the UTC cut-day boundary; no tag or release image was published.

### Fixed — Release tag audit and UTC cutoff (PAI-995)

- Batch remote tag verification while preserving exact object, ancestry and movement checks, reducing the network round trips required for release recovery.
- Recheck the UTC cut day immediately before creating a release tag, including after a recovery audit.

## [260910221338.0.0] — 2026-09-10

### Changed — Final calendar version display (PAI-994)

- Render calendar version labels with the approved revision 3 segment weights and sun-coloured date tint from the pinned INSPR display data. Canonical version strings, copyable values and legacy labels stay unchanged.
- Verify the vendored display data against its immutable upstream revision in offline builds and initialized checkouts.

## [260910201738.0.0] — 2026-09-10

### Fixed — Exhaustive race job budget (PAI-991)

- Run the complete handler race plan on its own CI runner, keeping every test, package timeout, outer-job limit and exact-release assurance gate unchanged. This avoids the accumulated 90-minute core-run timeout that prevented the prior reservation from publishing.

### Included — CRM offer delivery (PAI-991)

- Publish the pending customer link/QR/online acceptance, compact running headers and monthly `KYYMMN` customer numbers described below. The prior `260910172446.0.0` reservation produced no tag or release image.

## [260910172446.0.0] — 2026-09-10

Unpublished reservation: its exact-head exhaustive CI reached the outer time limit. No tag or image was published; the application changes are included in the next release above.

### Added — Customer offer links and online acceptance (PAI-991)

- Share finalized offers through a customer link or a printable SVG QR code; customers can read and print without an account.
- Explicit online acceptance records the signer, company, time and immutable audit receipt atomically. Expired offers stay readable and cannot be accepted; repeated attempts cannot replace the first signer.
- Show creator-specific accepted offers in CRM. Keep capability URLs out of application logs, caches and referrers; rate-limit public requests and honor the CRM module switch.

### Fixed — Offer page headers (PAI-991)

- Keep the offer number and date on one line; full company names stay in the address block and no longer crowd the repeated page header.

### Changed — Monthly customer numbers (PAI-991)

- Assign `KYYMM` followed by an unpadded monthly counter, such as `K26091`, `K26092`, then `K26101` in October.
- Allow an explicit admin conversion of a legacy customer number while all associated offers remain drafts; update their snapshots and revisions atomically. Issued documents and monthly numbers remain fixed.

## [260910133810.0.0] — 2026-09-10

### Added — CRM offer editor and PDF printing (PAI-991)

- Create saved offers from a customer's CRM page; reuse primary contacts and assign permanent customer/offer numbers in Europe/Vienna.
- Edit the supplied v8 A4 document, positions and amounts directly, with autosave and concurrent-edit protection. Configure sender and suggested German text blocks in a small dialog under Integrations → CRM or the editor.
- Finalize an immutable document snapshot, duplicate it with a new number, and print through a shared A4 renderer with additional pages for longer terms or positions. Anta and Manrope are bundled locally.
- First delivery uses browser PDF and manual forwarding. Public customer links, QR codes and online acceptance remain in PAI-991's later stage.

## [260910101110.0.0] — 2026-09-10

### Fixed — Docker image build after the display-weights frontend change (PAI-989)

- The frontend build stage now copies `scripts/release/version-scheme.json`,
  which `vite.config.ts` reads for `__APP_VERSION_SCHEME__` since
  260910071632.0.0. Without it the image build failed on every main push and
  on the `v260910071632.0.0` tag, so that coordinate is a tag-only reservation
  without a published image; this entry ships the first image that carries
  the weighted version footer.

## [260910071632.0.0] — 2026-09-10

### Added — calendar v2 display weights in the shell (PAI-989, INSPR-400)

- `CalendarVersion.vue` renders the instance version with the INSPR calendar v2
  display weights: per-segment opacity and the teal Schmuckfarbe mixed into the
  date, read at build time from `frontend/src/brand/calendar-version-display.json`,
  an in-repo copy pinned byte-for-byte to `doctrine/lib/calendar-version-display.json`
  (inspr-modules 0.9.0) by `scripts/check-calendar-version-display.sh` (CI step).
  The scheme comes from `scripts/release/version-scheme.json` via
  `__APP_VERSION_SCHEME__`, never from the string's shape; text content and
  `data-version` stay the canonical label. Used in the sidebar footer.

## [260909151030.0.0] — 2026-09-09

### Changed — INSPR calendar v2 release coordinates (PAI-979, INSPR-395)

- Releases are now cut as `YYMMDDhhmmss.0.0`: the UTC reservation second as
  the SemVer MAJOR segment with MINOR and PATCH fixed at `0.0`. The coordinate
  is syntactically valid SemVer, fixed-width and therefore string-sortable, and
  is published as exactly one immutable image tag with no numeric aliases.
  `scripts/release.sh now` reserves it; an explicit coordinate reserved earlier
  the same UTC day is also accepted. Legacy `patch|minor|major` and
  `yy.mm.dd[.hh.mm]` cuts are closed once the first v2 coordinate exists.
  Every published tag, image and changelog entry from the earlier eras stays
  exactly as it is.
- The delivery-evidence contract accepts `inspr-calendar-v2` next to `legacy`
  and `inspr-calendar-v1` (external-stage v2 artifact evidence, baseline built
  receipts, typed release identities, `/api/schema`, OpenAPI). Versions are
  validated against the grammar of their declared scheme and never inferred
  from shape; unknown schemes fail closed. The frozen owner-v2 fixture gains an
  explicit calendar v2 deployment plus verification pair, and the stored
  evidence table admits the new scheme through an additive migration.
- The changelog parser, release hygiene, doc-sync, GHCR pruning and the CLI
  release workflow recognise the new coordinate; the migration anchor is
  recorded in `scripts/release/version-scheme.json`.

## [26.09.09.13.13] — 2026-09-09

### Added — CRM door in the Paimos 6 shell (PAI-980)

- Add `/crm` and `/crm/:id` in the Paimos 6 shell, rendering the existing
  customer list and detail inside the v6 layout, with a `Customers` rail entry
  and a `Customers` command-palette action. List and detail links keep the
  `/crm` base so the shell never changes underneath the user. The 5.x views
  are reused, not rewritten; no CRM capability is added.
- Add the instance-level switch `crm_enabled` (default on, `app_settings`),
  exposed to every authenticated user through `GET /api/instance` and
  writable by admins through `PUT /api/integrations/crm/module` and the new
  toggle under Integrations → CRM. Off hides the Customers entry points in the
  5.x sidebar, the Paimos 6 rail and the palette; customer data and the
  `/customers` routes stay reachable and nothing is deleted.

## [26.09.09] — 2026-09-09

### Added

- Support optional native public base paths for shared-origin deployments, including frontend assets, authentication redirects and CLI worker messaging (PAI-972, PAI-976).

### Fixed

- Distinguish worker startup from messaging readiness and preserve busy inbox/steer delivery until the receiver is ready (PAI-973, PAI-974).
- Preserve and diagnose optional Cursor permission content without exposing private message payloads (PAI-975).
- Reject unsafe authentication return paths and retain cookie security flag coverage across session, OIDC and CSRF cookies.
- Restore unsupported-platform security test compilation by sharing the Cursor redaction assertion outside platform-specific test files.

## [26.09.08.17.06] — 2026-09-08

### Changed

- Publish the reviewed daily delivery batch: shared Flow navigation, release-specific acceptance and email evidence, and scoped operator responses for local workers.
- Replace the unpublished 26.09.08.16.12 release attempt with this new coordinate through the canonical protected squash release path. The earlier attempt stopped before tagging; its source history remains intact.

## [26.09.08.16.12] — 2026-09-08

### Added — shared delivery navigation (PAI-967)

- Embed the published INSPR Flow shell with scoped project context, delivery
  progress and guarded review/start navigation. The bounded host layout keeps
  existing project pages usable on mobile and desktop.

### Added — release-specific acceptance (PAI-968)

- Bind acceptance parties, confirmations and a minimal readiness checklist to
  an exact release and deployment target. Keep deployment and acceptance
  separate, with email preview, delivery evidence and recorded-email support.
  A platform status alone does not establish completed acceptance.

### Fixed — owned Cursor interactions (PAI-963)

- Route Cursor permission, plan and question requests through explicit local
  operator decisions bound to the exact session, generation, request and
  digest. Expose bounded assistant output privately while keeping public
  status and journals free of message content.

## [26.09.08.09.02] — 2026-09-08

### Added — scoped build and QA receipts (PAI-960)

- Bind built artifact and QA evidence to the reviewed baseline attempt through
  a typed receipt API and CLI, with atomic writes, exact replay handling and
  current worker authority checks. Receipt acceptance does not prove a live
  deployment or bypass the existing human start and readiness controls.

### Added — mixed worker account scopes (PAI-964, PAI-966)

- Select Codex, Claude, Pi and Cursor account/model scopes from the runtime's
  reported capabilities in both repair and baseline review. Account, profile
  and generation bindings remain explicit; changing selection invalidates
  the previous human confirmation. Existing v1/v2 configurations remain valid.
- Permit a class-only Claude built receipt only when its account binding is
  actually empty; named bindings still require the exact selected account.

### Fixed — installed runtime diagnostics (PAI-965)

- Recognize the supported Pi and Cursor executable/account-registry service
  flags while retaining private-file and owned-process checks. Real provider
  authentication and configured cross-service delivery still require operator
  setup and live verification after upgrade.

## [26.09.08] — 2026-09-08

### Added — deliberate baseline delivery controls (PAI-956)

- Opt-in INSPR baseline batches with human review, owned `inspr.readiness.v1`
  observation, immutable batch start, and durable pause/cancel controls mapped
  onto delivery attempts and owned harness sessions.

### Added — owned Pi protocol controls (PAI-957)

- Managed `agentd_pi` sessions bind operator-selected `PI_CODING_AGENT_DIR`
  context with durable queue steer, held-queue pause/resume, and owned
  interrupt/stop primitives. Protocol controls are proved with a deterministic
  local provider fixture; real provider authentication and installed live
  cross-service delivery remain operator setup after upgrade.

### Added — reviewed baseline bridge and controlled reconcile (PAI-960)

- Bridge reviewed Aithema specifications through scoped implementation/QA
  evidence into existing Pharos external-stage handoffs. `reconcile` applies
  the next authorized Pharos step with typed setup gates; cancel preserves
  baseline artifact binding on live handoffs.

### Added — owned Cursor ACP harness (PAI-963)

- Add `agentd_cursor` with operator-selected identity checks, included-model
  preflight, native next-turn inbox via `session/prompt`, `session/cancel`, and
  owned stop. Composer and Grok harness models use the actual ACP protocol;
  same-turn text steer is explicitly unsupported. End-to-end operator wiring,
  billing, and installed service delivery follow this release.

### Operator compatibility

- Release `26.09.07.20.15` shipped named-account controls only. Upgrade
  agentd configuration and register new receiver targets before using Pi or
  Cursor managed harnesses.

## [26.09.07.20.15] — 2026-09-07

### Fixed — named-account intent schema compatibility (PAI-959)

- Accept lifecycle intent schema 1 and named-account schema 2 on reporter
  claim, executing, and completed paths. Keep the claim envelope at schema 1.
  Unknown schemas and account, runtime, or project mismatches still fail closed
  before local execution.

## [26.09.07.17.12] — 2026-09-07

### Added — verified worker account selection (PAI-952, PAI-953)

- Bind owned Codex workers to an operator-configured account key and isolated
  Codex home. Verify the native account identity before dispatch and preserve
  that binding through retries, steering and owned inbox delivery.
- Advertise named account choices separately from subscription class, model,
  profile and runtime. Habitat requires explicit selection and review; changing
  the account invalidates an unsubmitted review, and a recorded start keeps its
  selected account locked.
- Reject stale, forged or mismatched account selections and cross-account
  adoption. Public runtime metadata carries account keys and safe display labels,
  without exposing local home paths or authentication credentials.

### Operator compatibility

- Existing class-only lifecycle configuration remains supported. Ordinary
  legacy account keys retain their labels; colon-containing or long keys receive
  safe derived labels without changing their opaque keys.
- Invalid, reserved or dimension-colliding named keys and invalid explicit
  labels fail configuration or registration validation. Check configured account
  choices before upgrading; the daemon never silently chooses another account.
- Named accounts require explicit operator enrollment and matching server and
  runtime versions. This release adds the account-control foundation; it does
  not yet provide the complete INSPR delivery stream or all harness adapters.

## [26.09.07.11.41] — 2026-09-07

### Fixed — release documentation alignment (PAI-949)

- Align the Agent Intercom release assertion with the verified `26.09.07`
  production docs while retaining historical floors and administrator-boundary
  checks.

## [26.09.07.11.03] — 2026-09-07

### Fixed — Habitat lifecycle drafts (PAI-950)

- Preserve ticket, work-shape and parent choices when an unchanged worker is
  refreshed, while continuing to invalidate unsubmitted reviews after a
  binding revision changes.

## [26.09.07] — 2026-09-07

### Fixed — reliable runtime coordination and guided CLI (PAI-934–942, PAI-948)

- Preserve failed receiver-inventory evidence so another healthy receiver cannot
  hide missing runtime coverage. Process independent inbox streams with bounded
  concurrency while retaining per-stream fencing and stop/repair draining.
- Surface recovery-journal write failures and retain the failed checkpoint in
  memory until explicit repair, preventing silent loss of retry backoff.
- Reject missing or unknown CLI subcommands with a nonzero status. Keep guided
  parent selections bound to the active session UUID despite stopped history,
  offer only supported receiver fallback setup, and report raw HTTP errors in
  human and JSON modes.
- Check all projects included in Habitat's bounded snapshot and show incomplete
  attention coverage when projects or workers are omitted or stale. Refresh
  expired runtime advertisements, preserve valid form choices, and invalidate
  unsubmitted reviews when authority changes. Keep a review open across unrelated
  worker heartbeat updates.
- Align the runbook guard with the shipped release and matching-build contract
  while retaining the administrator-boundary checks.

### Added — isolated UX experiment (PAI-943–946)

- Include an opt-in static discussion prototype at
  `/prototypes/simple/index.html`. It uses simulated data and optional browser
  speech features; it does not create accounts, execute jobs or publish work.
  This experiment is not the product's default interface or an accepted simple
  UX design; a substantially simpler flow is being discussed separately.

## [26.09.06.21.31] — 2026-09-06

### Fixed — complete Habitat and CLI release (PAI-917, PAI-928)

- Include the shared orchestration contracts in the container's frontend build,
  allowing the new Habitat UI and guided CLI workflows to ship together.
- Supersede the incomplete 26.09.06 release: its CLI artifacts were built, but
  its server image was not published. Install the corrected server and matching
  CLI together. The complete Habitat and runtime feature notes remain below.
- Clarify that unmanaged Claude CLI/resume delivery cannot steer an active
  turn; the owned Agent SDK runtime has separate typed controls. Record the
  completed local Codex/Claude handoff proofs without closing clean-machine or
  named cross-machine acceptance gaps.

## [26.09.06] — 2026-09-06

### Added — Habitat and owned runtime controls (PAI-917)

- Added the Habitat worker control room with Home, Workers, Projects and Needs
  you, plus focused worker details, profile/workspace choices and activity
  history. Existing project,
  assignment, orchestration and delivery projections remain authoritative.
- Added guided `paimos orchestrator start` and `paimos worker start` with
  canonical agent/profile selection, explicit ticket/work-shape/parent binding,
  preview and scriptable modes, durable exact retries and public registration
  checks. Canonical personas and owned process generations remain distinct.
- Added named-instance runtime setup, read-only diagnosis, bounded repair and
  preview-confirmed reset for operator-reviewed macOS and Linux user-service
  declarations. Credentials, workspaces and generation replay evidence remain
  outside reset's eligible archive set.
- Added typed browser lifecycle intents, private runtime/session authority,
  revision-checked controls and immutable assignment history. Explicit local
  workspace/profile configuration determines which effects a daemon can accept;
  public browser metadata contains no runtime proof or local path.
- Added private consumer and per-attempt proofs for canonical FIFO inbox and
  attention handoffs. Claims carry no payload; execution releases a transient
  payload once; ambiguous outcomes remain quarantined. Sticky database fences
  prevent legacy consumers from reclaiming or acknowledging owned work after
  expiry, while exact completion replay retains the original result.
- Added coalesced typed runtime-health attention and a bounded browser health
  read for authorized internal project viewers, with current reporter
  reauthorization and explicit fresh/stale/offline/unknown evidence. Optional
  operator workspace labels and server-proved session-to-workspace handles make
  choices meaningful without exposing paths.
- Applied the supplied Paimos logo and hero artwork with the pearl, aqua, teal
  and gold visual system across Habitat. Bundled originals use stable asset
  routes outside user-upload storage.
- Added authenticated human Message and Steer actions for one exact selected
  managed generation. The server rechecks the user, project, runtime reporter,
  generation, revision and target in one transaction, records canonical
  `human` attribution, and returns an immutable idempotent delivery receipt.
- Added an explicit reviewed recovery action for a never-claimed pending
  delivery whose managed target generation has stopped. Recovery binds one
  proved fresh replacement while retaining the canonical message, original
  target snapshot, product session and an immutable recovery audit trail.

### Fixed — FIFO recovery and MCP schema compatibility (PAI-914)

- Delivery workers recover outstanding canonical FIFO handoffs even after a
  read acknowledgement advances the inbox cursor. Terminal handoffs stay
  closed, and blocked deliveries report a bounded `fifo_blocked` reason.
- MCP tool schemas emit `required: []` when no fields are required, preserving
  compatibility with clients that reject `required: null`.

### Fixed — Installed runtime and frontend polish (PAI-917)

- Codex account discovery now accepts the installed CLI's bounded login status
  stream while rejecting mixed or multiline diagnostics. Tool activity and
  completion bind to the exact app-server thread and turn; terminal errors
  fail closed and remain failed even when the child process exits cleanly.
- Changelog release navigation now aligns consistently, wraps on narrow
  screens, and resets the selected release to its beginning. Habitat actions
  restore keyboard focus to a meaningful control after cancel or completion.

### Changed — Verification and rollout evidence (PAI-928)

- Added authority, replay, concurrency, ambiguous-outcome, legacy-fence,
  migration-preservation, browser-permission and workspace-mapping coverage.
  Habitat's production-build Playwright checks run explicitly in the existing
  required E2E job rather than depending on an opt-in local environment.
- Updated the runtime quickstart and API contracts. Clean-machine onboarding,
  real vendor handoff and live rollout acceptance remain separate verification
  steps; these notes do not claim those checks are complete.

## [26.09.05] — 2026-09-05

### Added — Scheme-aware external-stage artifact evidence (PAI-876)

- Added exact v2 media negotiation and DTOs for Pharos evidence with explicit
  legacy/calendar schemes, original version, channel, monotonic sequence,
  artifact/source digests, and immutable release-set manifest identity.
- Added append-only storage that binds each v2 verification to the exact prior
  v2 deployment across every identity field, plus canonical legacy, calendar,
  rollback, replay, malformed-input, and downgrade coverage. Frozen v1 bytes
  and semantics remain unchanged.

## [26.09.04.20.54] — 2026-09-04

### Fixed — Actionable Agent Intercom empty states (PAI-893)

- Replaced dead-end orchestrator binding states with truthful next actions:
  authorized operators can open the existing agent editor without losing their
  selected project, refresh newly created choices, retry unavailable data, or
  enter the existing talk surface when the orchestrator is already configured.
- Added a bounded, secret-free admin request for users without setup authority;
  the browser still never executes terminal commands or receives credentials.
- Added keyboard, mobile, stale-catalog, retry, and configured-state regression
  coverage, including production-bundle Playwright checks.

## [26.09.04] — 2026-09-04

### Added — Explicit ship and scout task shapes (PAI-907)

- Added closed `ship` and `scout` classifications to explicit worker-ticket
  bindings, with revision-checked reassignment and immutable before/after
  assignment evidence; legacy missing values remain `unknown` until classified.
- Added bounded value-free work contracts to harness sessions, Agent Mode worker
  fleet v2 responses, and agent onboarding. Scout means investigation evidence,
  cannot silently promote to ship, and neither shape claims Git enforcement or
  rewrites historical delivery attempts.
- Added an explicit worker-fleet v2 with trust-gated machine, privacy-safe
  workspace kind/mode, exact dispatch model/effort, and closed account
  provenance. Frozen v1 remains unchanged; unmanaged and legacy-unverified v2
  rows suppress caller-supplied runtime axes.
- Added an on-demand focused-delivery worker context that consumes the
  authorized bounded project fleet and renders those runtime axes together
  with task shape and output kind; failures retain no stale worker data.

### Added — Owned workspace and dispatch provenance (PAI-906)

- Owned agentd starts now reject concurrent exclusive workspace reuse before
  spawn and persist canonical Git/worktree provenance. Exact versioned Codex
  and Claude dispatch profiles are resolved through the authenticated
  execution-options authority, pinned, applied through documented vendor
  fields, and recorded with bounded non-secret machine and account provenance.
- Cross-daemon authority conflicts stop the rejected child, retired profile
  snapshots remain readable, and legacy starts retain explicit unknowns.

### Added — Durable reply obligations and held-request decisions (PAI-905)

- Added opt-in, exact-counterpart reply obligations with bounded, poll-driven
  overdue attention, plus immutable session-authenticated human dispositions
  for held action requests that never execute, deliver, or mutate the request.
- Kept the closed v1 agent-message envelope and routes byte/shape compatible;
  new CLI and issue UI consumers use an explicit closed v2 contract.

### Added — Bounded worker fleet projection (PAI-904)

- Added one versioned, worker-rooted read model shared by Agent Mode project
  and portfolio routes, with semantic-zoom sample bounds, exact omission
  counts, authoritative hierarchy/ticket bindings, explicit liveness
  uncertainty, effective capabilities, and local-cache provenance.
- Added bounded content-free communication metadata and delivery-trust-gated
  progress/ETA fields without exposing message bodies, credentials, target
  references, raw workspace data, provider output, or a second event stream.

### Added — Durable worker hierarchy and ticket binding (PAI-903)

- Added nullable, structured parent-generation and ticket bindings to the
  authoritative harness-session state, registration, agentd start/reporting,
  API, and CLI without overloading identity, prompt, workspace, product
  sessions, or attribution sessions.
- Added explicit revision-CAS reassignment with immutable before/after events,
  same-project and active-parent enforcement, cycle/depth guards, and an
  explicit resolved/unset/ambiguous project-orchestrator projection.

### Added — Actionable attention wakes (PAI-902)

- Added a durable, bounded orchestrator attention feed derived from worker,
  delivery, held-action, and control transitions, with leased/coalesced wake
  batches, monotonic acknowledgement, and crash-safe recovery.
- Kept attention content-free and reauthorized target mutations transactionally;
  the feed does not create a second worker-state truth.

### Added — Truthful worker activity (PAI-901)

- Added authenticated, monotonic adapter activity evidence and explicit
  busy/idle/unknown/dead projections; stale or malformed evidence becomes
  unknown rather than inferred idle.
- Added immutable semantic-transition history, timeout reconciliation, and
  terminal reason provenance while keeping heartbeat/yield replays out of the
  event stream.

## [26.09.02] — 2026-09-02

### Added — Safe orchestrator-binding handoff (PAI-884)

- Added a super-admin setup surface that builds and copies one fully visible,
  secret-free CLI command from an explicit local instance alias and agent
  choice; the browser never executes the command or infers an agent.
- Added `paimos orchestrator set` with an explicit expected deployment
  identity, pre-write health verification, and compare-and-set binding so a
  mismatched deployment or concurrent update aborts without mutation.
- Preserved the operator's selected alias, agent, and label while refresh
  confirms the binding remains unset, while clearing that local state when
  project or authority context changes.

### Changed — Clearer repository story (PAI-877)

- Clarified the README's product positioning, authorship and INSPR-family
  context, Paimos 6 maturity note, and recent-product-changes summary.

## [26.09.01.17.31] — 2026-09-01

### Changed — Truthful empty Agent Intercom state (PAI-878)

- Explained that an empty Paimos home has no product-session records without
  implying that local Codex or Claude processes are adopted or hidden.
- Separated configured, unset, and unavailable orchestrator-binding states and
  exposed the existing setup guide only to authorized super-admins.

## [26.09.01.15.52] — 2026-09-01

### Fixed — Deterministic release-tag assurance (PAI-875)

- Made the lease-renewal release oracle wait for the synchronized client state
  instead of an earlier server-side request count, removing a CI ordering race
  without changing production control behavior.

## [26.09.01] — 2026-09-01

### Added — Secure Agent Intercom operations (PAI-870, PAI-871)

- Added project-scoped, value-free control-outcome reads through the API and
  `paimos harness control get`, with stable message, delivery, correlation,
  vendor-session, and public-session identifiers kept distinct.
- Added the canonical Agent Intercom operator guide and executable
  documentation contracts for start, status, tell, steer, interrupt, stop,
  recovery, authorization, support boundaries, and released-version checks.

### Changed

- Bound every managed worker mutation to a protected per-generation lease,
  retired invalid legacy inbox state fail closed, and made dead-target
  retargeting, FIFO delivery, restart reconciliation, cleanup, and process
  reaping durable across races and restarts.
- Normalized worker authorization failures to non-enumerating no-mutation
  responses and preserved the original typed result when a failed control,
  including `stop`, is replayed with the same correlation ID.
- Split the protected pull-request race gate across bounded generic and
  managed-harness matrices while keeping the exhaustive serial and broad race
  suites on main, nightly, manual, and release runs.

## [26.08.31] — 2026-08-31

### Added — Paimos 6.0 product train (PAI-855–PAI-866)

- Promoted the new Paimos shell to the authenticated product home while
  preserving the exact 5.x dashboard at `/legacy`, including desktop and
  mobile navigation, shortcuts, source zoom, and orchestrator pinning.
- Added durable product sessions, deeper node contracts, session-home
  projection, voice-agent intake, and structured knowledge proposal,
  promotion, and activation flows.
- Hardened knowledge authorization, per-instance isolation, and scoped
  identities across the 6.0 knowledge and session surfaces.

## [5.21.0] — 2026-08-30

### Added — Owned Agent Intercom sessions (PAI-848, PAI-849, PAI-850)

- Added the durable M161 harness-session control plane for managed workers,
  including authenticated status and heartbeat reporting, encrypted local
  target references, FIFO delivery leases, and audited steer, interrupt, and
  stop outcomes. Stale or unavailable generations reroute the same durable
  delivery to an eligible active generation or its snapshotted simple
  fallback instead of bypassing the ledger.
- Added `paimos-agentd`, an operator-local per-instance supervisor shipped as a
  signed, notarized Darwin universal artifact plus unsigned Linux amd64/arm64
  tarballs. It owns the exact child process,
  keeps private sockets and bounded content-free state under an instance lock,
  drains output before reaping, and reconciles sessions to `ownership_lost`
  after restart rather than adopting an unproven PID.
- Managed Codex steer now stays on the owned documented app-server process.
  Managed Claude steer uses the same live Agent SDK Query's documented
  `streamInput()`, `interrupt()`, and `close()` primitives. Claude support
  requires the operator-installed Agent SDK 0.3.251 with its pinned hash and
  inherits the existing local CLI login; PAIMOS ships no proprietary SDK,
  vendor token, prompt, response, PTY/TUI injection, or queue-faked steer.

## [5.20.1] — 2026-08-28

### Fixed — Bounded startup retention (PAI-843)

- Startup retention now deletes or updates at most 50 eligible rows per
  transaction and yields between batches, so a backlog cannot monopolize
  SQLite's writer slot while authenticated requests are starting.
- The mutation-log self-reference has a partial child-key index, avoiding a
  full table scan for every retained parent removed. Recent API-key reads stay
  read-only; a due best-effort usage stamp has a short, connection-local busy
  policy and never inherits the normal five-second writer wait.

## [5.20.0] — 2026-08-28

### Added — Pharos request links on work records (PAI-812)

- Issues can carry one optional `pharos_request_id`, linking a need-a-host work
  record to an opaque Pharos host-action/request ID without making Paimos a
  provisioner or Pharos a Paimos client. REST, `paimos issue create|update`,
  and the issue MCP tools expose the same field; an empty update clears it.
- The link accepts only 8–128 ASCII letters, digits, hyphens, and underscores,
  rejects secret-like material at both the application and SQLite boundaries,
  and never performs a remote lookup. URLs, ticket prose, credentials, webhook
  host creation, and scraped ticket-to-host automation remain out of scope.
- Clone starts with no Pharos request link so a new work record cannot silently
  claim the source issue's one host action. Mutation snapshots retain the field
  so undo/redo restores intentional changes.

### Fixed — Agent bus SQLite writer starvation (PAI-835)

- Empty webhook-dispatcher polls now stay read-only until eligible work exists,
  instead of opening an immediate SQLite transaction every 250 ms and starving
  authenticated request writes such as API-key usage stamps and session audit
  records. A positive probe is reselected inside the existing transaction, so
  FIFO leasing, retries, and exactly-once handoff remain atomic.

### Fixed — Stable Codex steer discovery and fallback (PAI-840)

- Codex steer delivery now discovers turns with exactly one documented
  `thread/read {threadId,includeTurns:true}` request, selects the latest
  nonempty `inProgress` turn, and sends the schema-required `turn/steer`
  request with its exact `expectedTurnId`. It no longer depends on the gated
  `thread/turns/list` method.
- Daemon, proxy, initialize, thread-read, and steer transport failures now
  degrade safely to the exact `codex queue --thread … --message …` primitive
  with `fallback_reason=transport_error`. RPC attempts remain bounded and
  response-ID matched. A completed remote JSON-RPC rejection during initialize
  or thread-read, any undocumented remote `turn/steer` rejection, and any
  malformed frame or malformed/mismatched successful response fail closed.
  Only `idle` and `notLoaded` are clean inactive statuses;
  unknown/system-error thread status,
  an unknown turn status, missing thread history, a mismatched thread, or a
  missing/mismatched returned turn ID cannot masquerade as a transport failure
  or successful queue fallback. An inactive thread response that still contains
  an `inProgress` turn is likewise rejected as contradictory protocol data.
  Documented steer rejection classification requires the structured
  `codexErrorInfo.activeTurnNotSteerable` object or the exact bounded vendor
  message shapes; marker text in unrelated data and unknown turn kinds fail
  closed.
  Fallback diagnostics retain only the controlled phase and typed reason;
  vendor error text cannot echo target references, bodies, or secret-like
  values into listener output. Queue stdout and stderr are also discarded,
  while a nonzero exit remains a controlled failed-handoff error.

## [5.19.0] — 2026-08-28

### Added — Week and project hours views (PAI-830)

- Added the user-facing `/hours/week` and `/hours/project` Reporting routes. The
  weekly grid is backed by the new authenticated `GET /api/time-entries/week`
  endpoint, while the project view presents the existing
  `GET /api/projects/{id}/time-report` rollup by issue and person.
- Filing from the weekly grid reuses the existing time-entry write endpoint.
  Only super-admins can create entries for another user; regular users and
  non-super-admin administrators remain limited to their own hours.

### Fixed — Codex steer transport (PAI-825 follow-up)

- `paimos listen --deliver codex` steer delivery never reached the Codex
  app-server: 5.17.3 and 5.18.0 wrote newline-delimited JSON into
  `codex app-server proxy`, but the vendor documents that proxied stream as
  the WebSocket HTTP Upgrade handshake followed by WebSocket frames, so the
  daemon waited for an HTTP request head, `initialize` never got an answer,
  and the timeout cleanup of the npm wrapper orphaned the native proxy and
  surfaced as `initialize Codex app-server: EOF` (reproduced against Codex
  CLI 0.149.1 with app-server daemon 0.150.1). The harness Codex plugin
  (`backend/agentmessage/harness`, PAI-829) now performs the documented
  handshake through the same vendor proxy and sends one JSON-RPC message per
  text frame without the `jsonrpc` header; PAIMOS still never opens the
  control socket itself.
- Active-turn discovery no longer depends on `thread/turns/list` alone, which
  0.150.x gates behind the `experimentalApi` initialize capability. The worker
  opts in, checks the stable `thread/read` status first, and only for an
  `active` thread pages the latest turn, falling back to
  `thread/read {includeTurns:true}` when the paginated page is rejected. A
  thread that is idle or not loaded in the daemon uses the exact queue
  primitive with `fallback_reason=idle`.
- A proxy that never answers now fails inside the 20 s steer budget with a
  precise error naming the wire phase and the daemon/CLI versions from
  `codex app-server daemon version`, and the whole proxy process group is
  killed so the npm wrapper cannot leave an orphaned native proxy holding the
  pipes.
- The queue fallback after a rejected `turn/steer` is limited to the vendor's
  documented preconditions: an active turn that cannot accept same-turn
  steering (`activeTurnNotSteerable`, "cannot steer a review|compact turn")
  and the expected-turn race ("expected active turn id … but found …") record
  `fallback_reason=not_steerable`, a turn that finished first ("no active turn
  to steer") records `idle`. Any other app-server rejection (unknown method,
  request-shape drift, sub-agent ownership, internal error) now fails the
  delivery with the rejection in the error instead of silently queueing, so a
  broken primitive cannot masquerade as a successful simple handoff; the same
  rule applies to the `thread/turns/list` history fallback, which only
  triggers on the experimentalApi gate or a missing method.
- Added the opt-in `TestAgentBusRealCodexSteerE2E` proof
  (`PAIMOS_AGENT_BUS_E2E_STEER_THREAD=<thread>`) and the read-only
  `TestCodexAppServerProxyReadOnlyProbe` (`PAIMOS_CODEX_PROBE_THREAD`); the
  fake Codex CLI in tests now proxies to a real WebSocket server so the
  handshake and framing are exercised, including the gated-page fallback and
  the silent-proxy timeout.
### Added — Amy inbound webhook sender key (PAI-828)

- `grok_bot_routine` targets now carry the routine's receiver-owned sender
  key. `paimos message target set … --target-key-file <file|->` (a file or
  stdin, never an argument) sends it as the write-only `target_secret`
  field; the server validates it as one raw key, encrypts it under the
  separate `agent-message-target-secrets` secretvault domain in the new
  nullable `agent_message_targets.target_secret_cipher` column (M157), and
  every wake POST carries `Authorization: Bearer <sender key>` — the header
  the Grok Bot "When a webhook fires" trigger card issues — next to the
  stable `Idempotency-Key`.
- The harness plugin socket gained the optional `SecretHeaderPlugin`
  capability. A plugin that implements it requires the secret at
  registration, every other plugin refuses one, a hostile validator cannot
  echo it, and a webhook version registered without a key is never
  dispatched: the delivery blocks with `last_error_code=target_secret_missing`
  without contacting the endpoint.
- Registration and `message target list` responses expose only `has_secret`.
  The key never appears in the ledger, delivery status, audit, wake payload,
  listen disclosure, CLI output, or error text, and `paimos secrets rotate`
  re-encrypts the new column atomically with the target references.
- Both file flags are fail-closed inputs: `--target-key-file` and, for
  webhook adapters, `--target-ref-file` are read only from a regular,
  single-linked file owned by the caller with owner-only permissions
  (`0600`/`0400`), opened without following symlinks — the same policy as
  external-stage handoff credentials. Group- or world-readable files,
  symlinks, hard links, directories, and other users' files are refused
  before any byte is read; `-` (stdin) remains available for one of the two.

### Added — Claude simple delivery (PAI-827)

- `paimos listen --deliver claude` now performs idle Claude delivery with the
  two documented print-mode primitives: a local session UUID target runs
  `claude -p --resume <session_id>` as a new turn, and a `session_…`/`cse_…`
  cloud target runs `claude -p --cloud <session_id>` as a queued follow-up.
  The framed body is piped over stdin with fixed argv and no shell; a zero
  vendor exit is the handoff, the response is discarded, and the durable
  cursor advances only afterwards. Socket paths, URLs, and session names are
  rejected, and no target is inferred from the environment.
- Added the opt-in `TestAgentBusRealClaudeSimpleE2E` proof: with
  `PAIMOS_AGENT_BUS_E2E_CLAUDE_SESSION=<local session UUID>` it runs
  `paimos tell --level simple|steer` through the ledger, a live listener,
  the real `claude -p --resume` primitive, and the cursor ack, and logs
  commit/pickup/handoff timings.
- The opt-in Channels path (`paimos serve --mcp-stdio --channel-as`) records
  simple delivery state on every `notifications/claude/channel` event
  (`requested_level`, `effective_level`, and `fallback_reason` when set) and
  audits each successful write as `claude_channel_handoff`.
- Claude steer stays unsupported by design: a `steer` request (durable
  `paimos tell --level steer`, or legacy `--deliver-mode steer` on pre-bus
  rows) with `--deliver claude` falls back to the selected simple primitive
  and records `fallback_reason=unsupported` instead of exiting 4.
- Folded Claude into the receiver target registry (M155): `paimos message
  target set --adapter claude_resume|claude_channel --kind claude_session`
  binds a receiver to an encrypted local session UUID or `session_…`/`cse_…`
  cloud id. `listen --deliver claude` now leases `claude_resume` work
  (`?delivery=claude_resume`), resumes the receiver-owned session instead of
  `--deliver-target`, and completes through `delivery-complete`; the opt-in
  channel leases `claude_channel` work and completes each successful push the
  same way, leaving rows that belong to another adapter or still have no
  target untouched. Claude targets are fixed to `maximum_level=simple`, a
  Claude delivery can never complete as `steer`, and an unknown `?delivery=`
  worker adapter is rejected instead of ignored. `--deliver-target` remains
  only for pre-bus envelopes.

### Fixed

- Removed the false Claude messaging-socket claims: the dead
  `CLAUDE_CODE_MESSAGING_SOCKET` lookup, the "Claude Unix socket"
  `--deliver-target` help, and the `docs/AGENT_INTERFACE.md` sentence. The
  5.16.0 entry described a socket adapter that never delivered; Claude Code
  documents only the optional socket auth line, and PAIMOS does not invent a
  user-message frame.

## [5.18.0] — 2026-08-26

### Added — Instant bidirectional agent bus

- Added M154 durable `simple|steer` delivery intent, atomic message-send
  idempotency, encrypted versioned receiver targets, and one linked
  delivery/outbox state row per eligible canonical message.
- Added the instance-local `grok_bot_routine` HTTPS dispatcher with a stable
  delivery ID, security-framed minimal wake payload, bounded retry/dead-letter
  state, SSRF controls, and atomic handoff/cursor completion. Grok Bot steer is
  explicitly unsupported and records a simple fallback.
- Added per-message Codex listener delivery: `simple` uses exactly `codex queue
  --thread`; `steer` uses the app-server daemon/proxy initialize handshake,
  checks the latest turn for `inProgress`, and sends `turn/steer` with typed
  text input. Idle, policy-capped, raced, and non-steerable turns fall back to
  the exact queue primitive. The 5.17.3 `--deliver-mode` flag remains available
  only for legacy pre-bus envelopes; bus rows always use their durable level.
- Added operator CLI/API controls for target registration, non-secret target
  and delivery visibility, explicit missing-target attachment, and stable-ID
  delivery requeue. `paimos tell --level` makes sender intent durable.

### Security

- Capability webhook URLs and Codex thread references are encrypted at rest,
  omitted from ledger, status, audit, and webhook payloads, and disclosed only
  to an attributed local Codex delivery worker. Webhooks require HTTPS, an
  explicit hostname allowlist, public resolved addresses by default, bounded
  timeouts, and no redirects.
- Held, action-request, unallowlisted, secret-like, over-hop, over-rate, and
  cross-instance messages retain the existing fail-closed behavior and never
  gain runnable delivery from message text.

## [5.17.3] — 2026-08-25

### Added — Codex mid-turn steer delivery (PAI-825)

- Added `--deliver-mode steer` for mid-turn interruption of live Codex agents
  through the `codex app-server proxy` JSON-RPC transport, communicating with
  the app-server control socket at `~/.codex/app-server-control/app-server-control.sock`.
- Steer delivery performs the required `initialize` + `initialized` handshake,
  queries active turns via `thread/turns/list` (matching `status: "inProgress"`),
  and sends `turn/steer` with `expectedTurnId` and `input: [{type: "text", text}]`.
- Falls back to `codex queue` when no turn is in progress or the turn is not
  steerable (e.g. `/review` or `/compact`).
- Queue mode (`--deliver-mode queue`, default) remains unchanged and uses the
  existing `codex queue` CLI for wait-until-idle delivery.
- Claude mid-turn delivery marked unsupported; official messaging socket frame
  is not documented beyond the optional auth line. Idle Claude delivery should
  use `claude -p --resume` / `--cloud` or cross-session SendMessage tool.

### Security

- Codex steer uses `codex app-server proxy` (validated transport) and does not
  implement custom WebSocket framing or direct socket connections.
- PAIMOS does not store or replay Codex app-server responses or credentials.
- All tests verify correct JSON-RPC schema compliance: `initialize` with
  `clientInfo`, `thread/turns/list` for `inProgress` turns, and `turn/steer`
  with `{type: "text", text}` input format.

## [5.17.2] — 2026-08-25

### Changed — Agent Mode acceptance coverage and relay documentation

- Added exact-build Chromium acceptance coverage proving authoritative
  multi-delivery and multi-project navigation through mouse, keyboard, and the
  shared voice-command path, including scoped idempotent grant retries.
- Synchronized the canonical relay publication surfaces and release metadata
  after the PAI-817 security release.

## [5.17.1] — 2026-08-25

### Fixed — Canonical relay security enforcement

- Added an explicit `is_action_request` API field and `paimos tell
  --action-request` flag so state-changing free-text requests reliably enter
  the human-only held path instead of depending on prose detection.
- Added a trusted message-body boundary and a clear issue-detail label for held
  action requests, while retaining conservative detection as defence in depth.
- Added production-ledger regression proof for sender allowlists, held-message
  visibility, A→B→A hop termination, rate and per-turn bounds, body caps,
  secret rejection, trusted attribution, and the never-delivered action
  invariant; corrected the security and data-model docs to match live behavior.

## [5.17.0] — 2026-08-25

### Added — Gated Grok Build delivery

- Added `--deliver grok` for one-turn delivery into an existing Grok Build
  session through the first-party `grok` CLI, with canonical session UUID
  targeting and an explicit per-invocation `--enable-grok-build-delivery` gate.
- Documented Grok Bot as receive-by-human because it has no documented external
  receiving API or CLI; PAIMOS does not automate its UI or claim native delivery.

### Security

- Grok Build delivery uses fixed no-shell argv, disables tools, planning,
  subagents, and web access, caps execution at one turn, discards vendor output,
  and advances the durable cursor only after a zero vendor exit.
- PAIMOS never reads, stores, forwards, or prints Grok authentication material
  or the model response.

## [5.16.0] — 2026-08-24

### Added — Native durable agent-message delivery

- Added `paimos listen --as <harness>:<agent>` with bounded one-shot and
  follow reads, explicit acknowledgement, durable per-address cursors, and
  distinct exit codes for no new mail and unavailable native adapters.
- Added first-party delivery into existing agent sessions: Codex through
  `codex queue --thread`, Claude Code through its authenticated messaging
  socket *(correction, PAI-827: that socket adapter never delivered and was
  removed; Claude uses `claude -p --resume`/`--cloud` or Channels)*, and an
  opt-in MCP `claude/channel` capability that emits
  `notifications/claude/channel`.
- Added the M153 message `read_at` field and receiver cursor table, attributed
  listen/ack endpoints, and an editor-controlled name-based allowlist endpoint.

### Security

- Receiver reads and acknowledgements are bound to trusted agent attribution;
  acknowledgements advance monotonically only to real delivered, non-action
  rows in that inbox and occur only after output or vendor handoff succeeds.
- Vendor session credentials remain in the vendor-owned process environment:
  PAIMOS neither stores nor logs Claude tokens or Codex session credentials.
  The Claude channel capability is absent unless explicitly configured.

## [5.15.0] — 2026-08-24

### Added — Durable, security-framed agent messages

- Added the canonical project-scoped A2A envelope and M152 ledger migration,
  including stable message/thread/reply IDs, project and issue context, text
  parts, metadata, trusted session attribution, and monotonic listen cursors.
- Added `paimos tell <harness>:<agent>` with sender identity derived from the
  authenticated agent session; unknown senders and addressees fail closed with
  stable `agent_message_*` problem codes instead of accepting numeric agent IDs.
- Added framed message list/get reads by addressee, thread, and since-cursor,
  capped at ten delivered messages per page for downstream listen adapters.
- Added issue-anchored agent-message visibility for humans in a dedicated UI
  region that never inserts or renders agent traffic as a human comment.
- Added the standalone `agent-message-v1` JSON Schema plus migration/backfill,
  service, CLI, stable error, and contract regression coverage.

### Security

- Added the PAI-817 untrusted-message foundation: per-receiver allowlists,
  end-to-end system-owned hop ceilings, per-sender write limits, 32 KiB body
  caps, secret-like-content rejection, and mandatory holds for action requests.
- Addressee reads expose only delivered non-action messages and wrap their first
  text part with the explicit external-data security preamble; held traffic is
  visible to humans for review without becoming executable agent input.

## [5.14.0] — 2026-08-23

### Added — Calm, discoverable Agent Mode navigation

- Added a searchable project picker backed by the complete authorized project
  catalogue, so Agent Mode can switch projects even when the current folder
  dropdown previously exposed only one active project.
- Added context-aware English and German voice-command hints with automatic
  rotation, manual previous/next/show-all controls, and deterministic clickable
  commands that share the typed-command reducer.

### Changed

- Reduced ordinary delivery-card density with a persisted Calm/Full preference
  while preserving rich detail for the selected card and compact mobile voice
  controls.

## [5.13.1] — 2026-08-22

### Fixed — Final Agent Mode proof coverage

- Added protected-CI execution of the unsupported-platform runner fallback,
  proving unavailable process-group ownership fails closed without issuing a
  cancel-control lease.
- Added exact-build browser proof for the deployed-but-unverified to verified
  transition while preserving one persistent selection and the accepted
  Detail-10 Agent Mode design.

## [5.13.0] — 2026-08-22

### Added — Provider-neutral supervisory control and external-stage contracts

- Published the standalone Draft 2020-12 external-stage v1 schema catalogue,
  with a closed reference graph and exact OpenAPI-component alignment for
  independently implemented Pharos and Janus adapters.
- Added provider-neutral runner-control adapter conformance with truthful
  cancel-only capability reporting, so unsupported pause, resume, and input
  actions remain unreachable instead of being advertised optimistically.

### Changed — Responsive and authority-safe Agent Mode

- Hardened the approved Detail-10 Agent Mode surface across responsive layouts,
  reduced-motion preferences, focus and visual states, while retaining the
  human-accepted information architecture and design language.
- Frozen runner capabilities and authority at activation, preserving the exact
  device, credential, lease, target, and revision boundaries through control
  delivery and reconciliation.

### Reliability and security

- Made control apply/reconcile state durable across duplicate reports, restarts,
  concurrent journal writers, and cancel/result races without widening safe
  provider capabilities or accepting stale authority.
- Added production-shaped 32-writer SQLite contention coverage, deterministic
  expired-lease zero-mutation proof, adapter mutation oracles, and protected
  race gates for command confirmation, effect claims, and terminal sealing.

## [5.12.0] — 2026-08-22

### Added — Production-safe PAI-810 delivery reachability

- Version-1 instrumented runs now bind successful tests to an allowlisted commit,
  same-issue attachment, or bounded source-free SHA-256 snapshot, then atomically
  project eligible implementation and required-QA evidence without taking over
  active human or external QA work.
- Added the authenticated, CAS-bound `external-stage owner activate` command and
  API, atomically committing operator activation, exact Pharos handoff, and
  principal-attributed append-only audit evidence.

### Fixed

- The local runner freezes trusted Git and repository identity, pins deploy
  reports to the tested state, resolves supervision/cancellation races before
  success, and keeps source, paths, diffs, commands, output, environment, and
  credentials outside the API boundary.
- Serialized backend CI now permits the additive migration suite up to 30 minutes
  while retaining per-package execution.

## [5.11.0] — 2026-08-21

### Added — Sealed external delivery-stage handoffs (PAI-810)

- Published the immutable external-stage v1 contract, canonical Pharos owner
  and Janus dependency fixtures, CLI parity, and a release/commit/digest pin
  for downstream adapter conformance.
- Added transactionally bound create, mint, rotate, revoke, pull, accept, and
  report lifecycles with independent owner/dependency streams, exact replay,
  server-time freshness, durable hints, and strict verification lineage.

### Security

- External reporters require both the exact registered machine/API-key identity
  and a separate one-time handoff credential whose raw bytes never enter URLs,
  JSON, argv, environment, logs, audits, fixtures, errors, or later responses.
- Numeric epoch/UID boundaries, synthetic-fixture secret-scanner exceptions,
  reporter-role ceilings, stale authority, and exact-artifact verification now
  fail closed under focused, full-backend, SAST, and cross-model review.

## Historical development notes — PAI-810

The original notes below did not identify a release; their placement and content
are preserved without assigning them to a new release.

### Fixed — PAI-810 production reachability

- Version-1 instrumented `tests_passed` reports now atomically project bounded
  implementation and required-QA evidence, retry terminal QA with exact CAS,
  preserve active human/external QA ownership, and leave legacy runs untouched.
- The no-commit local runner now binds the exact stable covered repository
  source surface with a bounded source-free SHA-256 digest; no source, paths,
  diffs, logs, commands, environment, credentials, or attachment side effects
  cross the API boundary. The configured test command contributes only through
  a one-way hash.
- Configured deploys now persist the bounded `tests_passed` artifact/QA boundary
  before execution and keep later lifecycle reports pinned to that tested Git
  state, so deploy-side checkout mutations cannot replace verified evidence.
  The runner quiesces the closed telemetry/control stream at that boundary while
  retaining local process-group, timeout, silence, and output supervision.
- Source-free snapshots now hash raw no-follow filesystem nodes rather than Git
  patch presentation: HEAD/index-discovered nodes, real deletions, non-ignored
  untracked nodes, every repository `.gitignore` policy, and initialized nested
  repositories share whole-source 10,000-node, 64-MiB, time, and depth bounds.
  They reject exceptional/hidden index entries, freeze a trusted absolute Git
  executable before provider execution, scrub Git environment redirection, and
  neutralize topology, replace-ref, attribute, filter, diff-driver, and exclude
  config. Ignored payloads and the external test environment remain explicitly
  outside this repository-source evidence boundary.
- Added an authenticated, CAS-bound internal Pharos owner-activation command
  whose operator start, external handoff, and principal-attributed append-only
  M149 audit fact commit together; exact replay remains read-only after drift.

### Added — Pinned external delivery-stage handoffs (PAI-810)

- Published the exact-byte standalone Draft 2020-12 external-stage v1 JSON
  Schema catalogue under `backend/contracts`, with a closed reference graph,
  complete OpenAPI-component alignment, and immutable schema digest.
- Added a fixed v1 internal handoff create/mint/rotate/revoke surface and an
  external opaque pull/accept/report surface for registered Pharos owner and
  Janus dependency reporters, without widening canonical delivery enums or
  transferring stage ownership.
- Added `paimos external-stage` with file/stdin-only credential input, durable
  new-`0600` mint/rotate output, strict one-value report validation, exact
  vendor media negotiation, and no raw-secret output mode.
- Added an authenticated Agent Mode admin plane and matching CLI commands to
  discover current registrations, create/revoke exact reporter bindings, and
  seal 0–16 explicit required/optional prerequisite bindings with idempotent
  mandatory setup audit. Only required bindings gate owner success.
- Published immutable owner deployment→verification and value-free dependency
  fixtures with strict OpenAPI/DTO drift gates, per-file digests, and canonical
  fixture-set digest
  `sha256:0318f4025902c9d5dd790384950cc9daebb16e02e79a4a90ce7dddc673e68bed`.

### Security

- External calls now require the exact registered API-key identity plus an
  independently rotated handoff credential. Current binding and authority are
  reauthorized transactionally; missing, stale, or invalid authority is
  concealed, and credential bytes never enter URLs, JSON, argv, environment,
  cookies, logs, audits, fixtures, errors, or CLI output.
- Owner and dependency sequences, evidence, latest projections, mandatory safe
  audit, and durable Agent Mode hints commit atomically. Janus cannot complete
  canonical state, and Pharos verification requires a fresh separate-stage
  observation of the exact deployed artifact and environment.

## [5.10.1] — 2026-08-18

### Added — Synthetic human demo identity (PAI-697)

- Development fixtures now seed the canonical Mara Ellis demo operator with a bundled, wholly synthetic portrait, while preserving locally customized profile names and avatars on later seed runs.
- The fixture image follows the authenticated avatar storage path, and its generation provenance is documented alongside the dev-only asset.

### Fixed — Two-letter avatar fallbacks

- Shared avatar initials now derive natural two-word or segmented-handle initials and never exceed two characters across reusable avatars, the sidebar, and account settings.
- Local avatar runtime data is ignored so `just dev-up` leaves the worktree clean.

## [5.10.0] — 2026-08-18

### Added — Operator-controlled OIDC account selection (PAI-740)

- `OIDC_PROMPT` optionally forwards space-delimited prompt values to the identity provider authorization endpoint; unset or blank preserves existing session reuse, while `select_account` asks compatible providers for account choice.
- Prompt-only runtime configuration changes invalidate the lazy OIDC cache; values are trimmed and encoded, coexist with PKCE and `login_hint`, and remain absent from public OIDC status.
- OIDC configuration documentation and the example environment now describe the operator contract.

### Fixed — Locale-consistent agent run timestamps

- Agent run timestamps now use the shared display-locale and timezone formatter, restoring the repository formatting gate without changing timestamp semantics.

## [5.9.2] — 2026-08-17

### Added — Safe CLI issue tag assignment (PAI-791)

- `paimos issue create --tags` now assigns comma-separated or repeated existing catalog tags after preflighting the complete, deduplicated name set before issue creation.
- `paimos issue update --add-tag` / `--remove-tag` apply idempotent association changes without replacing the issue's full tag set, reject contradictory intent before network access, and can accompany ordinary field updates.
- Manual system-tag restrictions, network-free tag dry-runs, machine-readable contextual failures, retry-safe partial-operation reporting, CLI help, and operator examples now share one documented contract.

## [5.9.1] — 2026-08-17

### Fixed — Protected-main release automation (PAI-790)

- Release preparation now uses a deterministic `release/vX.Y.Z` branch, an author-DCO-signed commit, one protected pull request, and squash auto-merge instead of pushing directly to `main`.
- The release tag is created only at the exact validated PR merge commit after pinning and byte-validating the PR head, proving squash provenance and tree identity, and rerunning claims and hygiene gates against that head.
- Interrupted open, merged-but-untagged, already-tagged, behind-main, and pre-push states resume safely, while branch, file, PR, merge-method, tree, tag, signing-configuration, and cleanup drift fail closed.
- Hermetic adversarial tests cover the protected lifecycle, DCO without a local GPG key, late head mutation, same-file and EOF-byte drift, stale checkouts, direct-main push rejection, exact branch cleanup, and production-scale changelog validation.

## [5.9.0] — 2026-08-17

### Changed — Explicit numeric issue references (PAI-792)

- User-facing CLI issue commands now require full issue keys by default and reserve `id:<n>` for intentional internal database IDs, eliminating collisions between bare issue numbers and internal IDs.
- Direct commands, moves, relations, tags, time entries, attachments, parents, apply plans, and batch updates share the same validation contract while REST, MCP, and the documented `issue restore <id>` recovery command remain unchanged.
- Apply and batch-update inputs are fully validated before any issue request is sent; batch input is securely spooled and then replayed in bounded chunks so a late ambiguous reference cannot leave an earlier partial write.

### Security — Patched Go toolchain and dependencies (PAI-794)

- The build now uses Go 1.25.7 with patched `x/crypto`, `x/net`, and `quic-go` releases, clearing the applicable standard-library and HTTP/3 advisories.
- CI now runs a dedicated vulnerability scan on main and pull requests, and the production binary is verified against the patched toolchain and dependency graph.

### Documentation

- The public INSPR doctrine is vendored for offline agent use and linked from the project guidance without changing runtime behavior.

## [5.8.18] — 2026-08-11

### Added — Reproducible seeded product walkthroughs (PAI-696)

- Fresh development workspaces now turn PAI-1 into a complete issue-to-agent example with realistic description, acceptance criteria, notes, report copy, estimates, actuals, Jira reference, relation-backed cost unit and release, and two completed time entries.
- Re-seeding fills only missing synthetic fields, preserves local fixture edits and existing labels, avoids duplicate time entries or containers, and keeps project issue-number allocation ahead of the added containers.
- The deterministic capture workflow and responsive visual baseline now verify that the seeded issue detail renders without empty body, metadata, or time placeholders.

## [5.8.17] — 2026-08-11

### Added — Project lifecycle states enforce where new work may begin (PAI-754)

- Projects can be active, frozen, archived, or deleted; frozen projects retain editable existing issues while rejecting new issue rows, and archived projects remain readable but retired.
- Project lists now default to active work, with explicit CLI and UI filters for the complete lifecycle plus `paimos project update --status` for controlled transitions.
- API guards and database triggers enforce the same creation boundary across direct, batch, clone, intake, and move paths, returning a named conflict instead of silently accepting work.
- The discoverable schema, generated frontend types, agent documentation, migration coverage, and lifecycle integration tests now describe and verify the contract.

## [5.8.16] — 2026-08-11

### Security — Secrets can stay in runtime files (PAI-759)

- `ADMIN_PASSWORD`, `PAIMOS_SECRET_KEY`, `SMTP_PASS`, and `OIDC_CLIENT_SECRET` accept conventional `_FILE` companions so secret values no longer need to enter the process environment.
- File input wins over the compatibility environment form, removes exactly one LF or CRLF ending, and fails startup without exposing the configured path when the file is empty, unreadable, or oversized.
- Configuration, Compose pass-through, threat-model guidance, and value-free hardening checks now document the runtime-file deployment contract shared with secret managers, systemd credentials, and Janus managed-service slots.

## [5.8.15] — 2026-08-11

### Security — Frontend development tools resolve patched versions (PAI-758)

- The lockfile now resolves Vite 6.4.3, Vitest 3.2.7, and patched brace-expansion releases, clearing one critical and two high development-dependency advisories.
- `npm audit` reports zero vulnerabilities including development dependencies, without changing declared package ranges or crossing a major version.
- The upgraded toolchain passes the complete unit, typecheck, production-build, hosted E2E, and security suites, and the Vite development server still serves its HMR client.

## [5.8.14] — 2026-08-11

### Fixed — Knowledge list type typos fail instead of lying (PAI-760)

- `paimos knowledge list <type>` no longer silently ignores the positional-looking type and returns an unfiltered inventory.
- The CLI exits with a usage error that directs callers to the supported `--type <type>` filter before project resolution or any network request.
- Existing filtered `--type` calls and intentional cross-type lists remain unchanged.

## [5.8.13] — 2026-08-11

### Added — Admin-selectable shared background patterns (PAI-738)

- Branding settings now offer triangle, square, hex, lines, or no texture, with triangle as the neutral default for new and older branding documents.
- One renderer supplies the selected texture to login, password-recovery, sidebar, and appearance-preview surfaces, removing the duplicated legacy hex definitions.
- The backend validates and persists the selection in `branding.json`, with unit, integration, and responsive visual coverage for the full choice set.

## [5.8.12] — 2026-08-11

### Fixed — Unbranded instances start from a neutral palette (PAI-737)

- Fresh installations and missing branding values now use achromatic gray accents instead of the retired blue identity, including UI effects and report fallbacks.
- Existing per-instance color overrides continue to win over defaults, so custom branding remains unchanged.
- Release hygiene now rejects the retired palette in hex or RGB notation and the retired product name across tracked product, asset, and documentation text.

## [5.8.11] — 2026-08-10

### Added — Voice Intake can recover from an incorrect project guess (PAI-757)

- Opening the project chip now loads the caller's complete accessible active-project set, with AI candidates kept first alongside their confidence scores and rationales.
- A focused search covers project keys, names, and descriptions, and selecting any result uses the existing non-destructive pin flow without changing auto-switch behavior.
- Stale AI candidates are filtered through the fresh access-scoped project response and fail closed when that response cannot be loaded.

## [5.8.10] — 2026-08-10

### Fixed — Voice Intake chooses projects by purpose, not backlog size (PAI-756)

- Project matching now considers every accessible active project and supplies each project description to the semantic ranking stage, while keeping the visible suggestion list bounded to five.
- Generated specification titles and summaries carry more weight than transcript-tail chatter; rarity weighting and per-project normalization prevent large generic backlogs from winning by repetition.
- The existing no-evidence confidence clamp and access boundary remain intact, with a JANUS regression fixture covering the original secrets-tool failure.

## [5.8.9] — 2026-08-10

### Fixed — Voice Intake impact analysis stays bounded and stable (PAI-727)

- Combined impacted and related issue lists are capped at 20, preserving retrieval hits before graph-only candidates fill the remaining budget in deterministic key order.
- Candidate IDs and titles resolve in one live, project-scoped query instead of per-key title and reference lookups.
- Impacted, related, and graph-hit arrays are sorted before storage so equivalent analyses no longer churn event and SSE payloads.

## [5.8.8] — 2026-08-10

### Fixed — Voice Intake history stays on the selected revision (PAI-726)

- History-slider input is debounced so rapid scrubbing fetches only the latest selected revision, while returning to live remains immediate.
- Late history responses cannot replace a newer selection or re-enter history after session lifecycle transitions.
- Transcript time travel now replays chunks and restores in one ordered query, reuses persistent snapshots for nested restores, and rejects restore ancestry beyond 64 levels before committing it.

## [5.8.7] — 2026-08-10

### Fixed — Voice Intake speech cannot reactivate its own microphone (PAI-731)

- A pending microphone permission request now counts as active capture, and cancellation stops any stream that arrives late instead of reopening the mic during speech playback.
- Replacement speech keeps capture suspended across clips, ignores superseded audio responses, and revokes every blob URL on completion, failure, replacement, or teardown.
- The report-only content security policy now explicitly permits same-origin blob audio, preserving speech playback when the policy moves to enforcement.

## [5.8.6] — 2026-08-10

### Fixed — Voice Intake reconnects preserve newer events (PAI-729)

- Reconnect healing now buffers persisted SSE replay until its snapshot arrives, installs that snapshot first, and reapplies only newer events in sequence order.
- Superseded or disconnected hydration requests cannot overwrite newer session state, and replayed events still apply if the healing request fails.

## [5.8.5] — 2026-08-10

### Security — Voice credentials stay on approved ElevenLabs endpoints (PAI-730)

- Voice Intake accepts only the standard and EU-residency ElevenLabs HTTPS roots; plaintext HTTP, arbitrary hosts, credentials, ports, paths, queries, and fragments are rejected without changing settings.
- Existing invalid stored endpoints fail closed before the provider key is decrypted, preventing legacy configuration from forwarding credentials or audio elsewhere.
- Operator documentation now records the exact endpoint allowlist and canonical forms.

## [5.8.4] — 2026-08-10

### Fixed — Voice Intake relations stay inside their analyzed project (PAI-728)

- Voice Intake impact artifacts now record the exact project they analyzed, so re-pinning a session cannot file stale relations into a different project.
- Issue creation re-resolves every impact target inside its transaction and accepts only live issues whose current project matches the authorized create target. Moved, deleted, cross-project, and legacy unbound targets fail closed.
- Valid same-project relations remain atomic with issue creation and continue to mirror into the entity graph.

## [5.8.3] — 2026-08-10

### Changed — Marketing captures are reproducible release evidence (PAI-695)

- `just marketing-captures` now regenerates all six public product captures from a seeded local workspace whose backend and frontend exactly match a shipped tag; it refuses live services and unshipped source.
- The canonical site records image hashes, release commit provenance, viewport framing, and DOM landmarks in a capture manifest. Its production build fails if dimensions, hashes, TASKS framing, or the three annotated hotspots drift.
- Release documentation now carries the one-command workflow, replacing workstation-specific paths and the manual screenshot checklist.

## [5.8.2] — 2026-08-10

### Changed — Public evidence follows the canonical shipped surface (PAI-689)

- Public release claims now point to the canonical `paimos.inspr.at` site and include the repository, branch, and before/after commit evidence shipped in 5.8.0.
- Release hygiene and documentation sync now target the reviewed `inspr-site` repository, while trust links resolve to stable anchors instead of the retired `paimos.com` trust page.
- Deployment and continuity guidance now match the current ppm compose-stack and immutable site-release workflows.

## [5.8.1] — 2026-08-10

### Fixed — Live knowledge freshness checks follow the API schema (PAI-687)

- The ppm-only live freshness check now derives its expected public API schema from the backend `SchemaVersion` source of truth instead of retaining a stale `1.5.0` literal after the API moved to `2.0.0`.
- Clean worktrees no longer fail under macOS Bash 3.2 when the optional PAIMOS knowledge cache is absent, and required CI now exercises the live health/schema contract through a hermetic endpoint simulation.

## [5.8.0] — 2026-08-10

### Added — Agent runs carry inspectable code evidence (PAI-702)

- Local Implement-this runners now report the repository, branch, and full before/after Git object IDs with their terminal result. The issue run panel links directly to the resulting commit and distinguishes a changed range, an intentional no-commit result, and unavailable evidence.
- The backend validates the declared reference shapes without fetching repositories or holding forge credentials; `(base, head]` preserves multi-commit runs, while equal object IDs explicitly mean that no commit was produced.

## [5.7.1] — 2026-08-07

### Fixed — Branded instances show their own name (PAI-736)

- The sidebar rendered the literal "Project Management" next to a correctly-branded logo, so every white-labelled instance advertised the wrong name on every page. It now shows the configured brand name.
- Related: `name` and `product` are one identity derived from `BRAND_PRODUCT_NAME`, but a hand-written `branding.json` usually declares only one of them and the other silently fell back to "PAIMOS" — visible on the login page title too. The two now agree with whatever the branding document declares.
- PAI-736: sidebar brand name comes from branding, not a literal (#73)

## [5.7.0] — 2026-08-07

### Added — Identifier-first login (PAI-743)

- Sign-in now asks who you are first, then shows only the method that applies. Password managers stop autofilling-and-arming a password before you can reach the SSO button, and SSO users never see a password field they shouldn't use.
- Routing is home realm discovery from operator config — the new `OIDC_SSO_DOMAINS` maps email domains to your IdP. It deliberately never looks the account up, so the login page cannot be used to discover which usernames exist. Unset means no routing: every identifier is offered password + SSO, exactly as before.
- The routing answer is a UI hint, never a gate: password sign-in still works for any account, and `/login?method=password` skips routing entirely as a break-glass path. The identifier is forwarded to the IdP as `login_hint` so SSO users don't retype it.
- PAI-743: identifier-first login with config-driven home realm discovery (#72)

## [5.6.4] — 2026-08-07

### Fixed — SSO users are no longer nagged about local 2FA (PAI-742)

- The "Two-factor authentication is not enabled" banner no longer shows for OIDC-authenticated sessions — their second factor is the identity provider's policy, not Paimos-local TOTP. Password sessions keep the nag; hybrid accounts can still enable TOTP in Settings.
- PAI-742: suppress the local-2FA nag for SSO sessions (#71)

## [5.6.3] — 2026-08-06

### Fixed — Fresh installs can finally have a super-admin (PAI-739)

- The first-run seed created an admin-only user while granting `super_admin` requires being one — the role was unreachable on new instances without DB surgery. The seeded bootstrap user is now a super-admin, and migration M138 promotes the seeded `admin` account on existing instances that have no super-admin at all (instances that already have one are untouched).
- PAI-739: break the super-admin bootstrap deadlock (#70)

## [5.6.2] — 2026-08-06

### Security — CLI credentials can no longer leak via argv or stale YAML (PAI-685)

- `paimos auth login --api-key` is retired: legacy invocations fail with guidance instead of putting the key into process lists and shell history. Workstations use the hidden prompt; headless/CI sets `PAIMOS_API_KEY` as a runtime-only override.
- A legacy `api_key:` in `config.yaml` now migrates into the OS keyring even when `PAIMOS_API_KEY` is set; keyring-less machines get a loud manual-cleanup warning instead of silent skipping.
- PAI-685: CLI credential hygiene — no argv keys, always scrub legacy YAML (#68)

## [5.6.1] — 2026-08-06

### Fixed — The last utterance of a session can no longer be silently dropped (PAI-725)

- A wakeup racing the orchestrator worker's idle exit could land in a channel nobody reads, leaving the spec stale until the next input. Pokes and worker retirement are now serialized under one mutex: a raced wakeup keeps the worker alive and runs the pipeline. Covered by race-detector regression tests.
- PAI-725: fix orchestrator idle-exit lost-wakeup race (#67)

## [5.6.0] — 2026-08-06

### Changed — The workbench switches languages instantly and owns its header (PAI-734, PAI-735)

- Switching EN/DE is now a view switch, not a regeneration: each language keeps its own cached spec, summaries and ticket preview, toggling back costs zero AI calls, and a generation in one language never overwrites the view of the other. New input regenerates only the language you're looking at; the manual refresh button still forces a full regeneration.
- The workbench toolbar (breadcrumb, project chip, talk controls) moved into the app header — one chrome row instead of two. The search field steps aside on this screen; `/` and `⌘K` still summon it.
- PAI-734: per-language spec cache — EN/DE toggle is a view switch (#65)
- PAI-735: workbench toolbar lives in the app header (#66)

## [5.5.0] — 2026-08-06

### Security — Paid voice endpoints are rate-limited and metered (PAI-724)

- Every voice call (speech-to-text and text-to-speech) now passes cost gates before touching the paid provider: per-user concurrency, per-minute burst caps, the daily AI cap, and per-user daily voice budgets — 2 audio-hours STT / 60k characters TTS, overridable via `PAIMOS_VOICE_STT_DAILY_SECONDS` / `PAIMOS_VOICE_TTS_DAILY_CHARS`. Exceeding any gate answers 429 with Retry-After; budgets survive restarts.
- Voice spend is no longer invisible: each call records its billed units (audio seconds / characters) and an estimated cost on the AI papertrail instead of zero.
- PAI-724: rate-limit and meter the paid voice endpoints (#64)

## [5.4.0] — 2026-08-05

### Added — The workbench speaks its understanding (PAI-714)

- The selected ELI5/10/15 summary is read aloud (ElevenLabs) after each update — only when its text actually changed, and once when you switch levels. Muted by default; toggle on the Understanding Check card.
- A microphone interlock suspends listening while the workbench talks and resumes after — it never transcribes its own voice; starting to talk cuts the playback.
- Nothing is stored: speech returns as bytes and is dropped, with a metadata-only audit trail. Configurable TTS voice under Settings → Integrations → AI.

### Changed

- The per-session AI budget is now configurable (Settings → System, default 60,000 tokens) after a real dictation session exhausted the old 20k guard; the freeze message now says what to do.
- PAI-714: speak the selected ELI summary aloud (ElevenLabs TTS) + configurable session AI budget (#63)

## [5.3.3] — 2026-08-05

### Changed — Voice Intake polish (PAI-721)

- The empty workbench greets you properly: a waveform hero with "Ready when you are", click-to-talk, a live Listening state, and microphone guidance as a calm hint instead of a red banner.
- The right-column cards carry soft icon tiles and clearer "Appears after the first generation" empty copy. Live collapsed summaries stay.
- PAI-721: voice-intake polish — empty-state hero, card icon tiles, calm permission guidance (#62)

## [5.3.2] — 2026-08-05

### Fixed — Continuous talking survives every interruption (PAI-719)

- Returning to the workbench reconnects the live session stream — previously the mic kept posting but the spec silently stopped updating.
- Listening can no longer die silently: the no-speech give-up is gone (silent stretches recycle the recorder), the stop-then-start zombie-recorder race is fixed, hidden tabs can't wedge an utterance open, and the talk button always tells the truth.
- Leaving the page hard-stops the microphone; Start Talking on a completed session begins a fresh one; creating the issue releases the mic.
- PAI-719: voice lifecycle — capture can no longer die silently, sessions survive every interruption (#61)

## [5.3.1] — 2026-08-05

### Fixed — Microphone could never prompt (PAI-717)

- The pre-voice `Permissions-Policy: microphone=()` header overrode the browser's own site permission and silently suppressed the permission prompt — speech input showed "mic blocked" even when the site was set to Allow. Now `microphone=(self)`; all other features remain disabled, and a regression test pins the policy.

### Changed

- "auto-switch ≥ 90%" is now an actual switch: off pins the current project (nothing moves), on hands the selection back to detection.
- PAI-717: mic Permissions-Policy fix + auto-switch toggle (#60)

## [5.3.0] — 2026-08-05

### Added — Voice Intake polish (PAI-715)

- Permanent microphone-permission chip with live tracking (updates the moment the browser's site setting flips), one-click re-prompt while the browser still allows it, and honest guidance when the mic is blocked.
- First project detection is threshold-free — with nothing selected, any signal beats no project — and your most recently visited project gets a head start for the opening rounds, so auto-selection lands fast. The confidence threshold still guards every later switch.
- The right-column cards collapse to live one-line summaries (top impact chips with hover previews, ELI digest, ticket title, transcript tail); the scrubber and session actions stay pinned at the bottom; the transcript grows and follows at its newest line.
- Start Talking calmed down: outline when idle, solid red with a soft breathing ring only while the microphone is live.
- PAI-715: live mic-permission status + talk button restyle (#59)

## [5.2.0] — 2026-08-04

### Added — Continuous-listening speech input (PAI-710)

- The Voice Intake workbench now listens for real: browser microphone capture with utterance segmentation (client-side silence gate), server-side transcription via ElevenLabs Scribe, and transcripts flowing through the identical pipeline as typed input. Speak, pause, watch the spec grow.
- Admin-configurable under Settings → Integrations → AI → Voice input: encrypted API key, EU-residency-ready base URL (the Enterprise EU host uses its own hostname and key), model selection.
- Privacy: audio is transcribed and dropped — never stored anywhere (INV-INTAKE-06). The typed input remains one click away and is the automatic fallback for missing mics, denied permissions, or an unconfigured provider.
- PAI-710: continuous-listening speech input via ElevenLabs Scribe (#58)

## [5.1.0] — 2026-08-04

### Added — Voice Intake / Spec Workbench (PAI-703)

- **Talk-to-ticket workbench** at `/intake`: capture an idea as transcript chunks (dev text mode; the transcript layer is pluggable for future speech sources), watch a structured Markdown specification build live, and file the issue with one click (PAI-704…709).
- **Continuous project detection** with confidence gating: deterministic FTS pre-filter over your accessible projects plus a clamped LLM re-rank; auto-switch at a configurable threshold (instance default + per-user override, default 90%), manual pin always wins (PAI-706).
- **Live specification loop**: `intake_spec` regenerates the full spec from prior spec + newest transcript, EN ↔ DE toggle, user edits always win with an apply-latest banner (PAI-705).
- **Impact analysis**: mixed retrieval + blast radius surface touched / conflicting / extended issues; categories file as real relations on create (PAI-708).
- **Understanding check**: ELI5 / ELI10 / ELI15 summaries with staleness badges (PAI-709).
- **Time travel**: append-only session history with named checkpoints, scrubbing, diff-vs-live, and non-destructive restore (PAI-704/709).
- Everything degrades gracefully: capture, editing, checkpoints, restore, and create keep working with AI unconfigured or budgets exhausted.

### Fixed

- Dev-stack vite proxy now aligns the Origin header with the proxied host, so cookie-auth mutations work from the dev SPA (PAI-709).
- PAI-709: understanding check + time-travel polish + e2e (slice 6 of PAI-703) (#57)
- PAI-708: impact analysis — retrieval fan-in, blast radius, category cards (slice 5 of PAI-703) (#56)
- PAI-707: one-click issue creation from an intake session (slice 4 of PAI-703) (#55)
- PAI-706: project detection — FTS pre-filter, clamped LLM rank, confidence chip, thresholds (slice 3 of PAI-703) (#54)
- PAI-705: live spec loop — intake_spec action, per-session orchestrator, generation UX (slice 2 of PAI-703) (#53)
- PAI-704: Voice Intake session core (slice 1 of PAI-703) (#52)
- fix(frontend): bump dompurify to 3.4.12, run useMarkdown tests on jsdom (PAI-701) (#51)
- chore: point registry, deploy, and release tooling at inspr-at
- chore: rename Go module to the inspr-at namespace
- fix(deploy): reach csb1 via Tailscale IP, not MagicDNS name

## [5.0.0] — 2026-07-23

### Added

- **Move an issue to another project (PAI-690).** `paimos issue move <ref>…
  --to <key|id>` (and `paimos issue update <ref> --project <key|id>` for a
  single issue) re-homes issues between projects without close-and-recreate,
  preserving comments, time entries, history, tags, and cross-project
  dependencies. The issue keeps its id but is re-keyed to the target
  project's prefix + next number (e.g. `PAI-690 → OPS-12`); its former key is
  recorded in a new `issue_key_aliases` table (migration 133) so existing
  references still resolve. Project-scoped structural links (parent, sprint,
  cost unit, release, group) that would become cross-project are detached and
  reported. New endpoints `POST /api/issues/{id}/move` and bulk
  `POST /api/issues/move`; both require edit access on the source **and**
  target project. A differing `project_id` on `PUT/PATCH /api/issues/{id}` is
  now rejected with a pointer to the move endpoint rather than silently
  ignored.

### Changed

- **BREAKING: repo anchor directory `.pmo/` is now `.paimos/` (PAI-687).**
  `paimos anchors scan|verify|upload` default paths moved from
  `.pmo/anchors.json` to `.paimos/anchors.json` with **no legacy fallback** —
  rename the directory in consuming repos. The legacy `@pmo` anchor comment
  syntax is no longer recognized; use `@paimos`.
- **Report contractor identity is operator-configurable (PAI-686).**
  Projektbericht PDFs build the "Auftragnehmer" block from a new `contractor`
  string-array in `branding.json` (Settings → Visual → Workspace Branding,
  max 10 lines; served publicly via `GET /api/branding`, so imprint-grade
  data only). When unset, reports fall back to the branding company/product
  name — the previously hardcoded contractor block is gone.
- **Brand-neutral internals (PAI-687).** Frontend CSS custom properties
  `--bp-*` renamed to `--brand-*`; the retired second-operator deploy surface
  removed (`deploy-pmo` recipes, `deploy.pmo.conf`, secrets-mirror workflow);
  agent session preambles and MCP tool descriptions now read "PAIMOS
  project"; docs reference the second-operator instance neutrally
  (decommissioned June 2026).

## [4.8.0] — 2026-07-08

### Changed

- **SSO, AI control-plane, and public evidence move into the v4.8 baseline.**
  Generic OIDC SSO is now validated on the ppm reference deployment with
  Zitadel, the reference-deployment register and claim matrix reflect that
  live path, and the public site describes the stabilized AI control plane
  consistently: Codex and Claude Code runner actions, profile/effort/prompt
  and context controls, PPM knowledge prompt presets, repo-scoped local
  runners, hosted OpenRouter drafts, OpenAI-compatible local-model drafts, and
  the external Claude Code adapter path. This release is intentionally a
  housekeeping minor after the v4.7 stabilization line.

## [4.7.8] — 2026-07-08

### Changed

- **Generic OIDC SSO is invite-only by default (PAI-680).** The OIDC callback
  now requires an explicit `email_verified: true` claim, maps existing active
  users by verified email, refuses unknown emails unless
  `OIDC_PROVISION_MODE=auto-create` is set, and supports `member`/`external`
  auto-create roles without changing PAIMOS authorization rules. OIDC session
  creation now matches normal login lifetime semantics, and token/userinfo
  error handling avoids logging response bodies. Mocked-issuer tests cover
  PKCE, invite-only refusal, auto-create, and missing or unverified email.

## [4.7.7] — 2026-07-08

### Fixed

- **Project counters and visual baselines tightened after audit (PAI-678).**
  Project list/detail counters now share the same completed-status and
  soft-delete semantics for accepted and invoiced work, while the daily-work
  visual baseline uses the debug-admin fixture and stable per-document CSS so
  screenshots do not capture account reminder chrome.

## [4.7.6] — 2026-07-08

### Fixed

- **Project-list counters ignore soft-deleted issues (PAI-677).** The
  `/api/projects` aggregate now joins only non-deleted issue rows, matching
  `/api/projects/:id` counts and preventing deleted backlog rows from
  inflating project overview/list open totals.

## [4.7.5] — 2026-07-08

### Added

- **Daily-work visual regression baseline (PAI-673).** Added an opt-in
  Playwright screenshot baseline for project issue lists, issue detail / AI
  Workbench, settings, customer detail, and the customer portal dashboard at
  desktop and narrow widths. `just visual-baseline` boots disposable dev
  fixtures, compares the committed PNGs, and `--update-snapshots` refreshes
  them after intentional UI changes.
- **Role smoke coverage for debug fixtures (PAI-674).** The E2E stack now
  seeds disposable debug-role accounts and verifies admin, editor, viewer, and
  customer portal access across daily-work screens without printing or
  persisting generated passwords.

### Changed

- **Issue-list run actions have a clearer ownership boundary (PAI-672).**
  Row-level agent/run action discovery moved out of `IssueList.vue` into a
  focused composable with tests for runner discovery, draft-provider fallback,
  row defaults, and disabled/customer-mode reset behavior.

## [4.7.4] — 2026-07-08

### Fixed

- **AI call costs use the full model catalog (PAI-448).** `ai_calls` pricing
  now checks a private full OpenRouter model index before falling back to
  curated picker buckets, starts a background model refresh when the cache is
  cold, and backfills recent zero-cost rows once pricing is available.

## [4.7.3] — 2026-07-08

### Changed

- **Issue-list chrome has one saved-view control (PAI-669).** Global and
  project issue lists no longer render a second saved-view tab strip above the
  table; the shared IssueList picker owns the active view, filters, and display
  options. The picker row is grouped more tightly, server-backed counts now read
  as loaded/total once, and the views dropdown puts defaults, personal views,
  shared views, and epic-column display preferences in one compact panel.

## [4.7.2] — 2026-07-08

### Fixed

- **AI optimize per-hunk decisions apply the selected mix (PAI-219).** The
  optimize diff overlay now sends its mixed keep/reject text through the
  shared AI surface instead of applying the full model output regardless of
  hunk decisions. The hunk controls are explicit Keep/Reject segments with
  keyboard support (`K` keep, `R` reject) and regression coverage for a
  multi-hunk mixed accept flow.

## [4.7.1] — 2026-07-08

### Added

- **Normal-login debug fixtures (PAI-667).** Local dev builds can now seed
  `debug-superadmin`, `debug-admin`, `debug-user`, and `debug-customer` via
  an AGE-backed local env file, with 2FA reminder suppression limited to the
  `dev_login` build tag.
- **AI workbench selector summary (PAI-28).** The issue AI panel now surfaces
  the chosen action, model/default source, effort profile, prompt source,
  context mode, and project agent in one compact read-only summary.

### Changed

- **Dev fixture project key corrected to `PAI`.** The old `PAIT` seed key is
  normalized to `PAI` on dev-seed so local RBAC test data matches the canonical
  PAIMOS project key.
- **Viewer contexts are read-only end-to-end (PAI-24).** Project and issue
  list/detail surfaces now gate create, edit, delete, timer, upload,
  comment, relation, memory-curation, and implement-run controls by project
  edit access.

### Fixed

- **Portal list sort accepts `created_at` (PAI-25).** The customer portal list
  path now recognizes the shared issue-list created-date sort key.
- **Customer portal chrome stays internal-free (PAI-26/PAI-27).** Portal users
  no longer start the internal live-changes stream, saved-view loads, or undo
  controls that only belong in the authenticated internal app.

## [4.7.0] — 2026-07-07

### Added

- **Live project changes feed (PAI-230).** Added the authenticated
  `GET /api/changes` SSE stream with mutation-log replay, project access
  filtering, metadata-only payloads, and frontend `useChangesStream` /
  freshness integration so issue and project views can react to server-side
  changes without waiting for coarse polling.
- **Explicit provider actions for "Implement this" (PAI-629).** Claude and
  Codex runner actions are now first-class (`claude_cli.implement`,
  `codex_cli.implement`) with run/action metadata, runner capability surfaces,
  CLI selection, and frontend action/status labels.
- **Request-level undo coverage for batch tag/status cascade work (PAI-228).**
  Bulk tag changes and terminal-status cascades now record inverse mutation
  rows so request-level undo/redo can revert the full operation, not only the
  visible parent row.

### Changed

- **Claude Code adapter extracted (PAI-333).** `claude-code` now has its own
  public reference repository and independent `v1.0.0` release:
  `https://github.com/inspr-at/paimos-adapter-claude-code`. The PAIMOS
  registry points at that external source while the bundled fallback remains in
  place, so existing `paimos skill render --harness claude-code` workflows keep
  working.
- **Customer portal issue lists reuse the shared IssueList engine (PAI-476).**
  Customer-mode project views now go through the canonical list/table
  components instead of the old bespoke portal list path.
- **"Implement this" runner and web UX follow-through (PAI-625).** The runner
  and issue-list/detail surfaces gained the workstation proof fixes, clearer
  deploy-target behavior, AI status badges, and reviewable run status display
  that closed the final implement-run audit.

### Fixed

- **Project open-ticket counts exclude knowledge entries.** Project overview
  and project-list aggregates no longer count knowledge-plane rows as open
  tickets.
- **Knowledge updates preserve metadata when omitted (PAI-644).** Server and
  CLI update paths now keep existing metadata unless the caller explicitly
  replaces it.
- **Agent run write ownership tightened (PAI-624).** Run writes are constrained
  to the requester, claimer, or admin, with schema/OpenAPI guard coverage for
  the public run surfaces.

### Internal

- **Release tooling ignores non-release tags.** `just status`, `just release`,
  and `just doc-sync` now select the latest SemVer `vX.Y.Z` release tag, so
  operational bookmark tags such as `pai-open-start-*` cannot confuse release
  math or doc-sync diff ranges.

## [4.6.3] — 2026-06-29

### Fixed

- **"Implement this" — second-audit hardening (PAI-605).** A four-agent
  adversarial audit plus a full QA pass (including the race detector) of the
  v4.6.1/v4.6.2 work found real defects; all are fixed and now proven by tests:
  - **Atomic idempotency.** "One active run per issue" is now enforced by a DB
    partial unique index (migration 127). The previous `SELECT`-then-`INSERT`
    could let two concurrent "Implement this" clicks create duplicate runs that
    the runner executed twice (a double-deploy risk). Verified by a concurrent
    test; the atomic claim is likewise now proven under real goroutine concurrency.
  - **Lifecycle integrity.** Run status changes must follow a legal edge (a run
    can no longer jump straight to `deployed` with a fabricated version), and a
    terminal run (`deployed`/`failed`/`cancelled`) is immutable.
  - **Attribution & access.** The auto-posted report comment is attributed to the
    user who actually reported (previously forgeable as the requester);
    `log_attachment_id` must belong to the run's issue; advertising runner
    capability (`implement=1`) requires project-edit rights.
  - **Stale-orphan recovery.** A run a crashed runner left wedged in `running`
    is reaped on the next implement, so the pipeline can't get stuck.
  - **Runner.** Log attachment is now **opt-in** (`--attach-logs`, off by
    default — agent output can contain secrets visible to all project members).
    The reconnect backoff resets after a healthy connection instead of ratcheting
    to the cap; catch-up poll failures are logged instead of swallowed.
  - **Frontend.** Fixed a post-unmount polling/timer leak that accumulated across
    navigations, and a zone-less-timestamp edge. Closed several audited test
    false-greens (concurrency, visibility-polling, device selection, timestamps).

## [4.6.2] — 2026-06-29

### Added

- **Agent runs attach their log (PAI-617).** `paimos run-agent watch` captures
  the spawned job's combined output, attaches it to the ticket, and stamps
  `log_attachment_id` on the run — so a run is reviewable from PAIMOS, not just
  the runner's terminal. Best-effort: a capture/upload failure never fails the run.

### Changed

- **Runner `--exec` runs through a shell (PAI-619).** Quoting, pipes, and
  chaining now work (e.g. `--exec "claude --print 'do x' && npm test"`). The
  `implement` capability is now a first-class SSE query parameter rather than a
  string-concatenated suffix.
- **Issue-list "Implement this" follow-through (PAI-618).** After queueing, the
  inline confirmation becomes a one-click link that opens the issue's run panel —
  no more dead-end.

## [4.6.1] — 2026-06-29

### Fixed

- **"Implement this" hardening (PAI-605).** An adversarial review of the v4.6.0
  feature surfaced robustness, correctness, and contract gaps; all are fixed:
  - **Runner robustness.** `paimos run-agent watch` now reconnects with backoff
    on a dropped SSE stream (it previously exited silently), processes one job at
    a time via a worker queue, and periodically catches up on queued runs it
    missed while offline/busy or that a server restart orphaned.
  - **No double-execution.** A run is claimed atomically (`queued → running` via
    an `if_status` compare-and-set), so two runners can never both execute the
    same open run, and the report comment posts exactly once. Terminal statuses
    (`deployed`/`failed`/`cancelled`) are now enforced server-side.
  - **Cross-user runs.** A project editor — typically the developer whose box
    runs the job, not whoever clicked the button — can now report a run back
    (previously a 403). Repeated clicks are idempotent (return the active run).
  - **Deploy double-consent.** The off-by-default, triple-gated deploy now also
    asks for its own confirmation unless `--yes-deploy` is passed.
  - **Per-connection runner capability** in the SSE broker, so a browser tab and
    a runner on the same device no longer mask each other in the device picker.
  - **Frontend.** The run-status card stops polling when the tab is hidden,
    sequences overlapping fetches, refreshes the runner list live, distinguishes
    a runners-endpoint error from "no runner online", and renders timestamps in
    local time. New tests cover the issue-list quick action and the polling
    lifecycle.
  - **Contract + docs.** The five agent-run routes (plus a catch-up
    `GET /projects/{id}/runs`) are documented in `openapi.json`; the
    threat-model "audited" wording, deploy-flag docs, and a stale audit comment
    are corrected.

## [4.6.0] — 2026-06-29

### Added

- **"Implement this" — UI-triggered agent runs (epic PAI-605).** A button on a
  ticket hands it to a coding agent running on the developer's own workstation,
  which implements it and reports back — end to end.
  - **Run lifecycle API (PAI-606/607).** `POST /api/issues/{key}/implement`
    creates a queued run and publishes an `implement_requested` SSE event;
    `GET /api/issues/{id}/runs`, `GET|PATCH /api/runs/{id}`, and
    `GET /api/projects/{id}/runners` (online, implement-capable devices) round it
    out. New `agent_runs` table (migrations 125/126).
  - **Local runner (PAI-608).** `paimos run-agent watch` subscribes over SSE and,
    on a job, spawns Claude Code (override with `--exec`) in `--repo-root` and
    reports `tests_passed` / `failed`. Opt-in, repo-scoped, one job at a time,
    prompts before each run unless `--yes` — report-back only.
  - **Deploy gating (PAI-613, off by default).** The runner deploys only when
    `--allow-deploy` AND `--deploy-exec` AND a run-level `deploy_target` are all
    set, then captures the version and marks the run `deployed`.
  - **UI (PAI-610).** An "Implement this" panel on the issue detail view (button +
    device picker + live run-status card) and a quick "Implement this" action in
    the issue-list row menu.
  - **Report-back (PAI-609).** On a terminal transition the server auto-posts a
    summary comment on the ticket, keeping the human trail consistent with the
    structured run record.

### Internal

- **Dormant v1 issue-list paths removed (PAI-595).** IssueList v2 has been the
  production default since v3.10.6; the v1 fallback fetch paths and the
  `ff_issuelist_v2` off-switch are deleted (−387 lines), collapsing IssuesView /
  ProjectDetailView / PortalProjectView to the single controller path. The shared
  `components/issue-list/` renderer stays (its replacement is PAI-476).

### Security

- **Threat model for remote-triggered execution (PAI-611).** New trust boundary
  §2.7 and invariants INV-RUNNER-01..04 in [`THREAT_MODEL.md`](THREAT_MODEL.md);
  the structural-change review is logged in `SECURITY_GOVERNANCE.md`. Posture:
  opt-in per-workstation, repo-scoped, consent-gated, report-back only by default,
  deploy triple-gated, project-editor gated + audited. Operator + protocol docs in
  [`AGENT_INTEGRATION.md`](AGENT_INTEGRATION.md) (PAI-612).

## [4.5.0] — 2026-06-29

### Added

- **`paimos issue delete` / `restore` / `trash` (PAI-481).** The CLI now wraps
  the existing soft-delete lifecycle so accidental / probe / duplicate tickets
  have a real cleanup path. `issue delete <ref> --yes` moves an issue to the
  trash (reversible; cascades to descendant tasks); `--purge` removes it
  permanently but is refused when the issue has comments or attachments —
  steering to a reversible soft-delete or `--status cancelled` so real work
  keeps its audit trail. `issue restore <id>` and `issue trash` round out the
  lifecycle. `--yes` is required, so the commands are non-interactive and
  agent-safe.

### Changed

- **OpenAPI declared an explicit public-core contract (PAI-294).**
  `/api/openapi.json` now documents its coverage policy in `info.description`:
  it is the stable, curated *public* surface (auth, users, projects, issues,
  comments, time entries, attachments, documents, incidents, knowledge, GDPR
  ops); internal/admin one-off tooling is intentionally omitted and is not part
  of the stability contract.

### Internal

- **OpenAPI route-coverage guard (PAI-294).** The `/api` route block was
  extracted into a testable `mountAPI()`; a new guard test (`TestOpenAPIContractRoutesExist`)
  walks the real route tree and fails if the published spec references a route
  that no longer exists. A CONTRIBUTING rule asks that new public routes be
  documented in the same PR.

## [4.4.0] — 2026-06-25

### Added

- **Optimistic concurrency for issue edits (PAI-231).** `GET /api/issues/{id}`
  now returns a strong per-row `ETag`; the edit form captures it and sends
  `If-Match` on save. If the issue changed since it was loaded, the server
  returns `412` with a structured conflict body naming the diverged fields, and
  the editor shows a conflict banner with a **Reload latest** action instead of
  silently overwriting the concurrent edit. Clients that don't send `If-Match`
  (CLI, API-key) are unaffected — the guard is opt-in and backward compatible.

### Fixed

- **Soft-deleted cost_unit/release labels no longer leak onto member tickets
  (PAI-602).** A trashed cost_unit/release container vanished from the picker
  but its label still surfaced on member tickets and in filters. The payload
  JOINs and filter expressions now guard `deleted_at`, returning a nil ref (not
  a half-populated `{id, label:""}` ghost) once the container is soft-deleted.
  The batch partial-update path now also threads the request context.

### Internal

- **Real frontend lint gate (PAI-295).** `npm run lint` now runs eslint (Vue 3
  essential + typescript-eslint, prettier-compatible) in CI instead of just
  `vue-tsc`. Error-level bug rules fail the build; existing high-volume debt is
  surfaced as warnings to burn down incrementally. prettier is available via
  `npm run format` (opt-in).
- **E2E smoke suite (PAI-297).** A small, fast Playwright suite
  (`npm run test:e2e`, or `scripts/e2e.sh`) boots the real backend + frontend
  and drives the highest-value cross-boundary flows — dev-login, seeded
  projects, the authenticated shell, and a project view rendering issues from
  the API — wired into CI as a dedicated `e2e` job.

## [4.3.0] — 2026-06-25

### Added

- **Knowledge graph stays renderable at scale (PAI-350).** The graph endpoint
  now caps the node count (`?limit`, default 500, keeping the most-connected
  nodes) and can focus on a node's N-hop neighborhood (`?focus=<id>&hops=<n>`,
  default 2). The graph view shows an "X of Y shown" indicator with Show-all /
  Whole-graph controls and a Focus button on each node — so a project with
  hundreds of knowledge entries no longer overwhelms the canvas.

## [4.2.0] — 2026-06-25

### Added

- **Knowledge graph governance + polish (PAI-350).** The graph now includes the
  project's **agents** as nodes, with a `governed_by` edge to each memory their
  `non_negotiable_rules` reference — surfacing *which agent is bound by which
  rule*. Every node shows a short name label below it; clicking a node opens a
  side panel rendering its full body as **markdown** in read mode; double-click
  navigates to the entry; hovering an edge shows its relation type.

### Changed

- **Memory `content_revised_at` is now maintained on every write path
  (PAI-351).** The "needs re-review" signal for memory dependencies fires
  whether a memory's body is edited via the Knowledge tab, the generic issue
  update (incl. MCP `update_issue`), or a batch update — and undo/redo
  round-trips the timestamp, so undoing a body edit also clears the dependents'
  flag instead of leaving a stale one.

## [4.1.0] — 2026-06-25

### Added

- **Knowledge graph + self-maintaining memory dependencies (PAI-350, PAI-351).**
  The Knowledge tab gains a List/Graph toggle rendering an interactive
  force-directed graph of the project's knowledge plane (memory, runbook,
  external_system, related_project, guideline) plus the issues linked to them —
  derived from existing relations, no schema change. Memory entries can declare
  `depends_on` references; `GET …/knowledge/memory/:slug/dependents` and
  `paimos memory deps <slug>` show who builds on a rule. When a memory's body
  changes, its dependents are flagged **"needs re-review"** (a pill + filter on
  the list, a banner + Acknowledge action in the editor) — computed on read from
  two timestamp columns (`content_revised_at` / `deps_reviewed_at`), never
  stored, so it can't drift. The flag is maintained whether the body is edited
  via the Knowledge tab or the generic issue path (incl. MCP `update_issue`),
  and declared `depends_on` also renders as graph edges.
- **`just shot` — a headless Playwright screenshot harness for the dev UI**
  (captures any authenticated route to a PNG; self-bootstraps Chromium).
- **Money-path billing regression suite (PAI-582).**

### Fixed

- **Multi-word issue types now display title-cased** (`Cost_unit` → `Cost Unit`).
- **CLI `knowledge update memory <slug> --title X` (no `--body`) no longer wipes
  the body** — it carries the existing body over on the full-replace PUT (this
  also prevented a false "needs re-review" cascade to every dependent).
- CLI `--description` help text corrected (PAI-482).
- gosec G202 false-positive on the knowledge-graph SQL suppressed (baseline 0).

### Internal

- **Frontend schema-drift gate (PAI-490) + canonical-enum guard (PAI-489):** the
  generated frontend schema is regenerated and diff-checked in CI, and enum
  option lists are kept canonical + schema-synced.
- API schema version synced to 2.0.0 for the v4.0.0 line.

## [4.0.0] — 2026-06-23

### Changed (BREAKING)

- **`cost_unit` and `release` are now relation edges, not string columns
  (PAI-599; API schema 2.0.0).** Each ticket's cost_unit/release is a typed
  edge to a container issue (the single source of truth), mirroring the PAI-584
  `parent` edge. **Breaking API change:** the issue payload now returns
  `cost_unit` and `release` as objects `{ "id": <container_id>, "label": "<name>" }`
  (or `null`) instead of plain strings. **Writes are unchanged** — create/update
  still accept a string label (`"cost_unit": "ENG-A"`); the backend resolves or
  creates the container automatically, so the CLI, importers, and forms need no
  changes. Clients that read `issue.cost_unit` / `issue.release` as strings must
  switch to `.label`.
- The `issues.cost_unit` / `issues.release` columns are dropped. A migration
  backfills a container issue per existing label and edges every ticket before
  the drop (no data loss); the rate cascade now resolves the cost_unit container
  by id rather than a fragile title match.

> **Deploy note (NIX-193):** this is a major version — watchtower will **not**
> auto-pull `4.x`. It must be deployed manually (`scripts/deploy.sh ppm v4.0.0`),
> which takes a DB backup first.

## [3.10.11] — 2026-06-23

### Changed

- **Issue hierarchy fully migrated to the edge model (PAI-584, P6; internal).**
  The legacy `issues.parent_id` column is dropped — the typed `parent` relation
  edge is now the sole source of truth. All writes go directly to the edge,
  undo/redo and every report/lookup read from it, and one-parent + no-cycle
  invariants are enforced at the DB. **No API change**: the `parent_id` field is
  still accepted on writes and returned on reads (sourced from the edge), so
  clients are unaffected.

## [3.10.10] — 2026-06-23

### Added

- **`parent` relation type — manage the issue hierarchy as a first-class
  relation (PAI-584).** The relation API + MCP `paimos_relation_add` now accept
  `type=parent` (source=parent, target=child) for epic⊃ticket / ticket⊃task
  links, alongside the existing `parent_id` field. A legacy `type=groups`
  relation with an epic source is auto-translated to `parent`, so older agent
  calls produce a fully-visible link. Schema version → 1.6.0.

### Changed

- **One parent per child is now enforced (PAI-584, P5).** A second parent for
  any issue is rejected (DB unique index + API 409), and reparenting that would
  create a hierarchy cycle is refused (422) — on both the relation API and
  `parent_id` updates. Documented the SSOT convention in the schema and agent
  docs.

## [3.10.9] — 2026-06-23

### Fixed

- **GDPR access-audit retention now actually purges old rows.** The retention
  sweep deleted from `access_audit` using a non-existent `occurred_at` column
  (the table records `created_at`; only `session_activity` has `occurred_at`),
  so it errored on every run (`no such column: occurred_at`) and silently never
  trimmed old access-audit entries. Corrected the column — rows past the
  configured `PAIMOS_RETENTION_DAYS_ACCESS_AUDIT` window are removed again.

## [3.10.8] — 2026-06-23

### Fixed

- **Epic memberships created via the relation API are no longer invisible
  (PAI-584).** An epic→ticket link added through the relation API alone (without
  setting the ticket's `parent_id`) used to be missing from the issue list, epic
  filter, children, reports, and AI context, while estimate rollups read the
  other store — the two representations had silently diverged. Issue hierarchy
  now reads from a single source of truth (a typed `parent` edge), so these links
  surface consistently everywhere, including each issue's own parent badge.

### Changed

- **Issue hierarchy migrated to an edge-based single source of truth (PAI-584,
  phases P1–P3; internal).** `parent_id` is now mirrored into a typed `parent`
  relation by a DB trigger (unbypassable by any write path), every backend read
  and the API payload are sourced from that edge, and existing links were
  backfilled. Additive and backward-compatible — the `parent_id` field and its
  API/MCP contract are unchanged. Lays the groundwork for graph-native hierarchy
  (multi-membership, cost-unit/release relationization) in later phases.

## [3.10.7] — 2026-06-22

### Fixed

- **Intermittent `SQLITE_BUSY` 500s on issue writes (PAI-596).** `PUT /api/issues`
  and `POST …/comments` could fail (~20% under concurrent writes) with "database
  is locked". A deferred read→write transaction that has to upgrade its lock fails
  instantly on a snapshot conflict, and `busy_timeout` can't rescue it. The DB now
  opens with `_txlock=immediate`, so every transaction takes the write lock up
  front (BEGIN IMMEDIATE) and `busy_timeout` applies — concurrent writers queue
  instead of erroring. Verified: a mixed concurrent-write repro went from 10/50
  failing to 0/50.
- **Version-history keyboard navigation (PAI-382).** The history overlay now
  supports ← / → to step through versions (Esc to close), with an always-visible
  "← → navigate" hint beside the arrows.

## [3.10.6] — 2026-06-21

### Changed

- **IssueList v2 is now the default (PAI-575).** `ff_issuelist_v2` flips ON: the
  internal Issues and Project lists and the customer portal run on the shared
  `useIssueQuery` controller by default. The v1 paths remain as a fallback,
  reachable without a redeploy via `localStorage.setItem('ff_issuelist_v2','0')`,
  and are removed in a later cleanup.
- **Customer portal on the shared core (PAI-570).** `PortalProjectView` now
  fetches through the controller (portal mode + `createPortalFetcher`), so the
  internal and portal lists share one fetch/orchestration engine — completing
  the shared-core goal of the IssueList v2 epic (PAI-560).

### Notes

- Verified at v1/v2 parity (render, mount, load-all, filter, search, selection,
  bulk over all-matching, portal visible-only) and by the PAI-574 cross-cutting
  regression matrix; 320 frontend tests + typecheck + build green.

## [3.10.5] — 2026-06-21

### Added

- **IssueList v2 engine — staged behind `ff_issuelist_v2` (PAI-560).** A
  reusable, canonical issue-list controller (`useIssueQuery`) that, when the
  flag is enabled, powers the internal Issues and Project lists: one typed
  query model with a stable fingerprint; a fetch lifecycle guarded by a
  monotonic request id + AbortController + fingerprint (no overlapping or
  out-of-order results); a normalized by-id row-window cache (precise
  loaded-vs-all-matching, dedup, stable order); optimistic inline-edit
  reconciliation (PAI-567); fingerprint-bound selection across lazy-loaded
  sets (PAI-565); and controller-driven incremental refresh / delta
  reconciliation (PAI-568). Mode-agnostic fetchers let the internal and
  customer-portal lists share one engine (PAI-570).

  **Disabled by default — no behavior change to the shipped lists.** This
  release stages the engine in production for canary (enable
  `localStorage.ff_issuelist_v2=1`) before it becomes the default and the v1
  paths are removed (PAI-575).

## [3.10.4] — 2026-06-21

### Added

- **Users** — Admin user management is now one harmonized surface. A single
  create/edit form exposes the same fields for both flows (username, role,
  password + force-change, email, nickname, internal rate, locale), and the
  Users tab gained an Active / Inactive / Deleted status filter with inline
  disable, enable, delete, and restore — the whole account lifecycle on one
  screen. `CreateUser` now accepts the same profile fields as `UpdateUser`, so
  a new account no longer needs a second edit to set email/nickname/rate/locale.

### Changed

- **Auth** — Consolidated every password-write path behind one minimum-length
  policy (`auth.MinPasswordLen` = 8): self-service reset, self-change, admin
  create, and admin reset. Client hints (first-login screen + account settings)
  now agree with the server instead of advertising the old 6-char minimum.
- Admin password reset (`PUT /users/{id}` with a password) now invalidates the
  target user's sessions and forces a password change on next login, atomically
  with the password update — matching the self-service reset and self-change
  flows. A self-edit keeps the current session and does not force a re-change.

### Fixed

- The admin "Edit user → new password" flow now reliably takes effect with the
  same side-effects as the other password flows. Removed a dead, unrouted
  duplicate users view; deleted users now live under the Users tab's Deleted
  filter rather than the Trash tab.

### Security

- **PAI-223** — Triaged the gosec baseline to zero.

## [3.10.3] — 2026-06-09

### Added

- **PAI-590 / PAI-591** — `paimos serve` adds a local read-only context
  broker for coding agents. HTTP mode is loopback-only by default and exposes
  bounded repo state, search, file-read, symbol fallback, remote+local retrieve,
  and context-pack endpoints; MCP stdio mode exposes the same small read/search
  surface. The broker blocks traversal and symlink escape, redacts common secret
  shapes, caps returned data, labels repo-derived content as `untrusted_data`,
  and has threat-model invariants plus focused hardening tests.
- **PAI-222** — Server-side project retrieval now defaults to
  `local-semantic-v2`, stores/ranks vectors through SQLite via
  `paimos_cosine()` (`vector_index: sqlite-scalar-cosine`), reports embedding
  provider/freshness metadata, and carries a fixed MRR eval guard against the
  previous `local-hash-v1` baseline.

### Changed

- Agent integration docs now distinguish central `/retrieve` from the local
  `paimos serve` broker, document the current pure-Go SQLite vector path, and
  call out the remaining ONNX/sqlite-vec ANN follow-up honestly.
- The threat model now includes the local broker trust boundary and
  `INV-BROKER-*` security invariants.

## [3.10.2] — 2026-06-09

### Added

- **PAI-221** — Added shared frontend display-locale helpers for dates, times,
  counts, file sizes, durations, fixed decimals, percentages, and compact
  currency formatting, plus a raw-format guard to keep new Vue surfaces on the
  same path.

### Changed

- **PAI-221** — Rolled locale-aware display formatting across the PAIMOS UI,
  including routed pages, portal pages, issue tables/details, dashboard and
  reporting screens, project/settings panels, AI activity surfaces, attachment
  metadata, and sync/history states.
- **PAI-109** — Refreshed the published claim matrix so shipped security and
  air-gap claims carry current evidence references.

## [3.10.1] — 2026-06-09

### Fixed

- **PAI-576** — A pending migration that adds a constraint no longer bricks an
  upgrade with an opaque mid-transaction error. Migration 113's unique index on
  `(project_id, issue_number)` now runs behind a data precondition: if the
  issues table already holds a duplicate non-NULL pair, startup fails fast with
  an error naming each offending `project_id / issue_number / ids=[…]` so an
  operator can renumber, instead of the container looping forever on
  `UNIQUE constraint failed`. NULL-`project_id` sprint markers are excluded
  (they are distinct under the partial index). The precondition hook is
  generic, so future constraint-adding migrations can register their own check.

## [3.10.0] — 2026-06-03

### Added

- **PAI-580** — Projektbericht export gains two options: an optional **"Booked
  by"** column (`cols=booked_by`) listing the short usernames who booked time on
  each row ("mba, opa"), and a **"By month"** grouping mode (alongside flat and
  by-epic) that splits each ticket per calendar month of its bookings — one row
  per ticket-month under a `YYYY-MM` header, so monthly subtotals are
  billing-correct.

### Changed

- **PAI-580** — Projektbericht PDF polish:
  - The `[keine Kundenfassung]` placeholder is gone; a ticket without a
    customer-facing summary now falls back silently to the technical
    description.
  - Numbers use the German thousands separator (`19.033,01`) across AR EUR,
    AR h, and the subtotal / grand-total rows.

## [3.9.5] — 2026-06-03

### Added

- **PAI-580** — Project report export "By month" scope. The export dialog gains
  an inline scope toggle: alongside the existing current-filter export, a
  time-booked scope auto-selects every ticket with ≥1 time booking in a window.
  A month quick-picker fills the editable From/To fields (the SSOT — widen or
  narrow freely), dynamic state checkboxes (all issue statuses, default = the
  completed set), and a flat vs by-epic grouping toggle. Reported AR h / AR EUR
  reflect the window-booked time (and material), not the ticket's lifetime AR.
- **PAI-579** — Booked-hours report endpoint
  `GET /api/projects/{id}/time-report?from=&to=&user=`: per-user, per-day, and
  per-issue booked hours + material over an explicit window, attributed by the
  work date (`date(started_at)`, user-settable via PAI-478). Hours-only — no
  rate exposure, so project-view access suffices.
- **PAI-581** — Per-entry material booking. Time entries can carry an optional
  `material_lp` (Leistungspunkte / token cost) independent of hours, enabling
  Time & Material reporting (time × hourly rate + material × LP rate). Entries
  may be hours-only, material-only, or both.

### Fixed

- **PAI-557** — Projektbericht PDF now prints the customer's full postal address
  when it lives only in the free-form `address` field. The fallback chain
  (billing → visit → free-form) no longer short-circuits on a bare country line,
  with the country de-duplicated.
- **PAI-54** — Projektbericht AR EUR resolves the rate through the full
  effective hierarchy (issue → epic → project → customer). A customer-only rate
  no longer yields a blank AR EUR column.

### Security

- Bumped the Go build toolchain to **1.25.11**, patching three standard-library
  vulnerabilities (GO-2026-5037 crypto/x509, GO-2026-5038 mime, GO-2026-5039
  net/textproto) flagged by govulncheck.

## [3.9.4] — 2026-06-02

### Fixed

- **PAI-577** — The issue-list `BOOKED` column (and other time fields) could
  show stale values. The conditional-GET ETag was keyed only on
  `issues.updated_at` + row count, so it was blind to data the list renders
  from other tables: booking time, changing tags, or editing sprint membership
  never changed the ETag, and clients kept stale rows via `304 Not Modified`
  (this survived a hard reload, since programmatic fetches still send
  `If-None-Match`). A new `issues.content_rev` counter — bumped by triggers on
  `time_entries`, `issue_tags`, `issue_relations`, and `tags` — is folded into
  the ETag via `SUM(content_rev)`, so any change to a list-rendered derived
  field now invalidates the cache. The triggers are the enforcement layer: no
  write path (API, mite import, CLI, manual SQL) can bypass the marker.

### Changed

- **PAI-563 / PAI-564 / PAI-571** — IssueList "show all" is now an explicit
  `limit=0` window mode. Global, project, and portal lists preserve that mode
  across compatible query transitions such as sorting instead of snapping back
  to the first 100 rows.

## [3.9.2] — 2026-06-01

### Added

- **PAI-563 / PAI-564 / PAI-565 / PAI-572** — IssueList envelopes now expose
  `returned`, `has_more`, `fingerprint`, and `selection_fingerprint` metadata.
  The ordered query fingerprint stays stable across page windows, while the
  selection fingerprint identifies the all-matching set used by IDs-only
  selection expansion.

### Changed

- **PAI-565** — Project `Select all matching` now resolves IDs through the
  project-scoped `/api/projects/{id}/issues?ids_only=1` path and rejects a
  response whose selection fingerprint no longer matches the visible list.

## [3.9.1] — 2026-06-01

### Fixed

- **PAI-562** — IssueList envelope `revision` metadata now reports the bare
  list revision hash instead of a partially trimmed weak ETag string.
- **PAI-551** — The release script now updates the pinned `docs/INSTALL.md`
  version examples alongside `VERSION`, README, and CHANGELOG so the
  knowledge-freshness gate stays green during non-interactive release cuts.

## [3.9.0] — 2026-06-01

### Added

- **PAI-560 / PAI-561 / PAI-562** — Added the IssueList v2 foundation:
  documented current list ownership and failure modes in `docs/ISSUELIST_V2.md`,
  and introduced server-side list envelopes for global, project, and portal
  issue lists with explicit `total`, `limit`, `offset`, `sort`, `order`,
  `query`, and `revision` metadata.
- **PAI-574** — Added regression coverage for list envelopes, project-scoped
  `ids_only`, negated project filters, portal-safe search/window responses,
  and optimistic inline-edit rollback/reconciliation.

### Changed

- **PAI-563 / PAI-564 / PAI-566** — Project, global, and portal issue lists now
  request server-backed search/sort/window metadata instead of treating the
  currently loaded client rows as the full universe. Project issue lists page
  through the envelope and keep `Load all` behavior explicit.
- **PAI-565 / PAI-572** — "Select all matching" now resolves IDs through the
  same server query used by the visible page and stays project-scoped in
  project views.
- **PAI-567** — Inline status/priority/assignee/release/sprint edits now apply
  optimistic patches, reconcile with the server-confirmed row, and roll back
  only the affected edit on failure.

### Fixed

- **PAI-560** — Selection mode no longer lets the checkbox column overlap the
  frozen issue-key column during horizontal scroll; actions remain frozen on
  the right edge.
- **PAI-560** — The authenticated app shell automatically uses its compact
  sidebar rail on small screens so IssueList keeps a usable mobile table
  viewport instead of being squeezed by the full 230px sidebar.
- **PAI-484 / PAI-485 / PAI-487 / PAI-488 / PAI-494** — Hardened schema-contract
  error handling: enum validation paths keep returning Problem Details JSON,
  CLI enum validation has fixture coverage, and the frontend preserves nested
  Problem Details metadata.

## [3.8.3] — 2026-06-01

### Added

- **PAI-551** — Added `scripts/check-knowledge-freshness.sh` and wired it
  into release hygiene. The check catches removed project-manifest API
  references, stale install/schema examples, and shipped security gaps
  that are still documented as open.
- **PAI-552** — Added `paimos curl`, a raw API helper that uses the active
  instance URL and configured auth, so operator runbooks can call endpoints
  such as `/portal/overview` without hand-assembling bearer headers.
- **PAI-555** — Added a shared CRM provider contract-test harness under
  `backend/handlers/crm/contracttest`; HubSpot and HTTP sidecar providers
  now exercise the same ImportRef / Sync / DeepLink contract.
- **PAI-558** — Added explicit customer legal identifier fields:
  `tax_id` for UID/tax number and `company_register_number` for FN.

### Changed

- **PAI-553** — Updated source docs and PAI knowledge for the current data
  model through M114 and API schema `1.5.0`.
- **PAI-557** — Projektbericht customer party blocks now print postal
  address lines plus UID and FN when those customer fields are available.
- **PAI-559** — The `CUSTOMERPORTAL` tag chip now renders as the compact
  eye + `CP` marker with the tooltip "issue is shown in customer portal".

### Fixed

- **PAI-554** — Fixed project issue-number allocation under concurrent
  creates. M113 adds an atomic per-project counter plus a unique database
  backstop on `(project_id, issue_number)`. The duplicate ppm row was
  repaired before the migration: id `2183` is now `PAI-555`.
- **PAI-556** — Projektbericht PDFs now reserve roughly 3cm more vertical
  clearance before the paper signature line so printed reports are easier
  to sign by hand.

## [3.8.2] — 2026-05-29

### Fixed

- **PAI-509** — **Non-Issues project tabs now scroll.** Every primary
  tab except Issues rendered its content into `.pd-page`'s bounded-flex
  height with no scroll viewport of its own, so tall content (most
  visibly the new Settings tab, but also Knowledge/Agents/Context/…)
  was clipped with no way to scroll. Wrapped all non-Issues tabs in a
  single bounded scroll viewport (`flex:1; min-height:0; overflow-y:auto`).
  Issues stays exempt — `IssueList` keeps its own sticky-thead scroller —
  and none of the wrapped components carry an in-flow vertical scroller
  (the Knowledge side-panel is `position:fixed`), so there is exactly
  one scrollbar per tab and no nesting.

## [3.8.1] — 2026-05-29

### Changed

- **PAI-508** — **Project settings move out of the ⋯ modal into a
  Settings footer tab.** The "Edit Project" modal (and its ⋯-menu
  entry) is gone. Project configuration now lives in a right-aligned,
  admin-only **Settings** footer tab (gear + label) with plain
  **General / Billing / Danger** sections — no sub-tabs — reusing the
  same save logic the modal had. Environments + deploy recipes move to
  the **Context** tab alongside linked repos. Pure relocation: no new
  capability and no API change; it closes the discoverability gap that
  PAI-504/505 began, so first-class concepts (agents, knowledge) and
  settings are all footer tabs and nothing hides behind a menu. IA
  documented in `docs/DEVELOPER_GUIDE.md`.

## [3.8.0] — 2026-05-29

### Added

- **PAI-504** — **Agents are a first-class project tab.** The
  project-page footer bar gains an **Agents** tab, peer to Knowledge.
  The declarable-agents editor (PAI-326/329) that previously lived
  only inside the Edit Project modal is now reachable directly as a
  primary tab (and via `?tab=agents`), with the same inline
  create/edit/delete UX and a live count badge. Writes stay
  admin-gated (`isAdmin && canEditProject`); reads follow the project
  view guard.
- **PAI-506** — **First-class agent CRUD in the CLI, MCP, and
  schema.** New `paimos agent {list,get,create,update,delete}` verb,
  mirroring `paimos knowledge`: `--project` (key or id), file inputs
  (`--body-file`, `--bootstrap-steps-file`, `--rules-file`),
  `--metadata` JSON, `--json` output, and idempotent partial update by
  name. Five matching MCP tools (`paimos_agent_*`). Agent endpoints +
  entry shape are now discoverable in `GET /api/schema` (schema bumped
  to `1.5.0`). Previously agent writes were UI / raw-API only, forcing
  scripted/LLM workflows to hand-roll `curl`.

### Changed

- **PAI-505** — **Edit Project settings restructured into a coherent
  IA.** The modal's accreted fields are grouped into four scannable
  sub-tabs — **General** (name, key, description, status, tags, owner,
  logo), **Billing** (customer, label, rates), **Environments**
  (shared inventories), and **Danger** (archive/delete/purge) — styled
  after the app Settings tabs. Agents moved out to its own primary tab
  (PAI-504); no first-class concept is reachable only from inside the
  settings modal. The settings IA is documented in
  `docs/DEVELOPER_GUIDE.md` so future additions have an obvious home.

### Documentation

- **PAI-503** — **Doc sync for v3.7.4..v3.7.10.** README feature
  catalogue now lists the timer-panel today-total footer + day
  scrubbing (PAI-495/499) and the customer-portal side-panel pin +
  prev/next navigation (PAI-496/497); `docs/api-minimal.md` documents
  the previously-missing `GET /api/time-entries/today-summary`
  endpoint. (Hero-screenshot refresh and the `paimos-site` sibling
  sync remain deferred — see the PAI-503 ticket.)

## [3.7.10] — 2026-05-27

### Fixed

- **PAI-502** — **Compact `DD.MM.` date in the timer footer.** The
  locale-formatted fallback ("Mon, May 25") from PAI-500/501 truncated
  to "MON, MA…" in the narrow sidebar once the user scrubbed back two
  or more days. Replaced with a universal `DD.MM.` form (e.g. `25.05.`)
  — dense, deterministic, no truncation, no year noise. Today /
  Yesterday / Tomorrow still translate via `timer.day.*`.

## [3.7.9] — 2026-05-27

### Fixed

- **Operational** — **Recovery from stuck workflow dispatch.** The
  `v3.7.7` and `v3.7.8` tags both pushed but never produced images:
  v3.7.7 failed at the GHCR push step (storage exhausted), and the
  failed run wedged itself in a "Queued/attempt=2" state that no API
  or UI cancel form could clear. Every subsequent push then silently
  created zero workflow runs — paimos-repo-specific (other
  markus-barta repos kept dispatching). Resolved by renaming the
  workflow files (`ci.yml`/`release.yml` → `ci-v2.yml`/`release-v2.yml`)
  to give GitHub a fresh workflow identity, plus refreshing the gosec
  baseline whose line numbers had drifted. v3.7.9 ships v3.7.7's
  intended code content (PAI-496/497 portal sidebar + PAI-499 day
  scrubbing + PAI-500/501 footer polish) — all already documented in
  the 3.7.5 / 3.7.6 / 3.7.7 entries below. The v3.7.7 and v3.7.8
  GitHub tags remain on origin as historical pointers; they never
  resolved to a container image.

## [3.7.8] — 2026-05-26

### Fixed

- **Operational** — **Re-cut of v3.7.7.** The `v3.7.7` git tag pushed
  fine but its CI workflow failed at the GHCR push step
  (`failed to fetch oauth token: denied: denied`) — root cause was
  storage exhaustion on the `markus-barta` user's GHCR package
  (~2,000 lingering manifest versions accumulated over the project's
  history). Pruned 1,666 untagged + old `sha-*`-only versions
  (semver releases and the 20 most-recent `sha-*` rollback pins were
  kept), then re-cut v3.7.7's content as v3.7.8. No code differences
  vs. v3.7.7. If `v3.7.7` ever publishes via a stuck queued run it
  will be byte-equivalent to v3.7.8 minus the CHANGELOG entry.

## [3.7.7] — 2026-05-26

### Fixed

- **PAI-501** — **Timer footer: stop the label wrapping, stop the nav
  drifting.** PAI-500's 3-cell grid still let "Sun, May 24" line-wrap
  inside its 1fr column on the narrow sidebar, and the 1fr columns
  sized to content so the centre nav drifted off-centre whenever the
  label width differed from the total width. Switched to
  `minmax(0, 1fr) auto minmax(0, 1fr)` so the side columns honour the
  fr ratio regardless of content (nav stays anchored across day
  selections); added `white-space: nowrap` + `overflow: hidden` +
  `text-overflow: ellipsis` to both the label and the total so the
  text never wraps and only truncates as a last resort.

## [3.7.6] — 2026-05-26

### Fixed

- **PAI-500** — **Timer footer polish: no-jump layout, hover-only nav,
  locale-aware labels.** The PAI-499 day-scrubbing row in the sidebar
  timer panel switched from a flex/baseline/space-between layout to a
  3-column grid (`1fr auto 1fr`) with `align-items: center`. Flipping
  between short labels ("Today") and long ones ("Mon, May 26") no
  longer shifts the `< • >` controls or the daily total — the nav
  stays anchored to the panel centre and all three elements vertically
  centre against each other instead of baseline-aligning. The nav
  cluster also became opacity:0 by default and fades in on
  `.timer-body:hover` or `:focus-within`, matching the "Stop all"
  affordance and reducing noise when the user isn't actively scrubbing
  days. Day label (`Today` / `Yesterday` / `Tomorrow`) and the
  `toLocaleDateString` fallback now route through `useI18n`'s active
  locale with `{date}`-interpolated tooltips (`timer.day.*` keys land
  in en + de).

## [3.7.5] — 2026-05-26

### Added

- **PAI-499** — **Day-scrubbing in the timer panel footer.** The PAI-495
  daily-total row gained three ghost buttons between the day label and
  the sum: prev/next chevrons walk one day at a time, and a centre dot
  jumps back to today (disabled when already there). The label flips
  between "Today" / "Yesterday" / "Tomorrow" for nearby days and falls
  back to weekday + short date otherwise. Live-ticking elapsed of any
  running timers only contributes when the selected day is today —
  past days are settled, so adding live seconds to them would be
  nonsense. Backend already accepted arbitrary `from`/`to` windows on
  `/api/time-entries/today-summary`, so this is a frontend-only feature.
- **PAI-496** — **Pin button on the customer portal side panel.** The
  portal's `PortalIssueSidePanel` gained a pin icon in its header that
  routes through the shared `useSidePanelPinned` singleton. Pinning
  fires `AppLayout`'s existing inset (padding-right on `.main` driven
  by `pinned && visible`) so the panel shrinks the header + content
  together instead of overlaying them — matching the internal SPA's
  behaviour exactly. Width is read from the shared `useSidePanelWidth`,
  so the panel and the layout offset stay in lock-step. ESC dismissal
  is skipped while pinned (the explicit X button still closes).
  Leaving the portal route releases the inset cleanly.
- **PAI-497** — **Prev/next issue navigation in the customer portal
  side panel.** Two chevron buttons in the header walk through the
  current tab+filter list without leaving the panel. The parent
  (`PortalProjectView`) passes the active `tabBoundIssues` IDs and
  handles the emitted `navigate` event by swapping the open issue;
  buttons disable at list bounds (no wrap-around). Composes cleanly
  with the pin button — navigating while pinned keeps the panel open.

## [3.7.4] — 2026-05-26

### Added

- **PAI-495** — **Today-total footer for the sidebar timer panel.**
  A new hairline-divided row at the bottom of the expanded
  `SidebarTimerPanel` shows the live sum of every time booking for
  the current local day across all of the session user's projects.
  Stopped entries are summed server-side via the new
  `GET /api/time-entries/today-summary?from=&to=` endpoint (the
  client sends its browser-local day window, so server timezone
  doesn't enter the calculation); the elapsed seconds of any
  currently running timers are added on top so the figure ticks
  alongside the RUNNING row. Refreshes on panel open, after
  start/stop, and when a peer tab/window stops a timer over the
  existing `paimos:timer` BroadcastChannel. Empty days render `0m`
  so the row stays discoverable.

## [3.7.3] — 2026-05-23

### Fixed

- **Release evidence re-cut.** Re-cuts the v3.7.2 application payload after
  GHCR rejected an SBOM attestation upload with `BLOB_UNKNOWN` during the
  tag workflow. The CI release job now retries CycloneDX SBOM attestation
  uploads before failing, so transient registry upload errors are less
  likely to leave a signed-but-unverifiable patch tag. No runtime behavior
  changes beyond the v3.7.2 contents.

## [3.7.2] — 2026-05-23

### Changed

- **PAI-483 / PAI-484..PAI-494** — **Intent contract hardening across
  API, CLI, MCP, and frontend schema consumers.** The schema endpoint now
  exposes the canonical issue enum sets used by generated frontend types,
  write endpoints validate enum values consistently, and invalid inputs
  return RFC 7807 Problem Details instead of ad-hoc error bodies. Batch and
  mutation routes gained idempotency handling where clients retry writes,
  while the CLI and MCP layers now preflight enum values, propagate
  idempotency headers on writes, and share contract fixtures / coverage
  registry tests so future endpoint drift is visible before release.
- **PAI-400** — **Projektbericht numeric columns are explicitly
  selectable and persistent.** The five numeric toggles (`SP`, `h`,
  `AR SP`, `AR h`, `AR EUR`) persist under the catalogued
  `paimos:lieferbericht:cols` key, flow into the PDF as the shared
  `cols=` query parameter, and are reflected by the JSON report response
  so preview/export clients use the same effective selection. Backend
  coverage pins default, partial, and empty column sets while preserving
  existing AR-EUR rounding.

## [3.7.1] — 2026-05-21

### Added

- **PAI-478** — **Time entries gained a date field; override
  strike-through now explains itself.** The manual "Log time" form on
  every issue gains a Date input (default = today) and persists
  `started_at`/`stopped_at` on the chosen day instead of `now`, so
  retroactive bookings actually carry the day the work happened.
  Existing entries gained click-to-edit on the Start date cell (a
  delta-shift that preserves time-of-day and applies the same shift to
  both timestamps, keeping duration intact for non-override timer
  rows). Strike-through styling for `override != null` stays — the
  semantics ("manual override; start/stop don't determine the hours")
  are still useful — but the date cells now carry a hover tooltip that
  spells out *why* they're struck through, so colleagues who read the
  visual as "deleted / wrong" get a clear explanation. Backend already
  accepted `started_at`/`stopped_at` on `PUT /api/time-entries/{id}`
  with `mutation_log` and existing self/super-admin gating; this is a
  pure frontend change.

## [3.7.0] — 2026-05-20

### Added

- **PAI-475** — **Comments now carry an `internal` / `external` visibility
  flag (default `internal`)**. Every comment is either internal
  (team-only) or external (also visible on the Customer Portal). The
  team must explicitly opt-in to share — safer-by-default. M111 adds
  `comments.visibility TEXT NOT NULL DEFAULT 'internal' CHECK (visibility
  IN ('internal','external'))` and a supporting index; existing rows
  backfill to internal. A new `PATCH /api/comments/{id}` endpoint lets
  the comment author or an admin flip visibility post-hoc; every flip
  goes through `mutation_log` (type `issue.comment.visibility.change`)
  and is undoable via the standard PUT-snapshot path. A new
  `GET /api/portal/issues/{id}/comments` endpoint filters to external
  only and 404s when the issue lacks the CUSTOMERPORTAL tag. The
  internal IssueComments composer gains a radio-style pill group with
  a helper line that swaps copy + color when external is selected;
  every rendered comment shows a visibility badge that doubles as a
  click-to-flip control for the author or admin (with a confirm step
  on internal→external). New `comments.visibility.*` i18n group in
  en + de.

### Changed

- **PAI-474 (v1)** — **Customer Portal project view, polished v1.**
  Addresses every concrete complaint from the v3.6.0 audit; the deeper
  IssueList reuse refactor moves to PAI-476.
  - **Stopped leaking internal cost / pricing fields.** The portal API
    previously serialised `cost_unit`, `release`, `estimate_hours`,
    `estimate_lp`, `ar_hours`, `ar_lp`, `estimate_eur`, `ar_eur` on
    every `/api/portal/projects/{id}/issues*` response. The UI showed
    them as "—" but any customer opening DevTools — or any cache,
    proxy, or audit log between us and them — could read the values.
    The `portalIssue` struct, both SELECT/Scan paths, the
    `portalSummary` pricing aggregates, and the now-unused `computeEur`
    helper are all gone. A regression test loads non-zero values for
    every removed field on a seeded issue and asserts none of the
    banned JSON keys appear in the wire payload. The Projektbericht /
    acceptance-report path is unaffected — that's the customer's
    contract document and still includes pricing on purpose.
  - **Click-to-open side panel** (`PortalIssueSidePanel.vue`). Loads
    via the cleaned portal endpoints, shows type + key + title, status
    + priority pills with customer-friendly labels, description /
    acceptance criteria / report summary in markdown, the
    customer-visible comments thread (PAI-475 filter), and Accept /
    Reject buttons inline. ESC + close button to dismiss.
  - **Working search.** The v3.6.0 IssueFilterBar collected `q` but
    `tabBoundIssues` never applied it; the backend FTS5 path also
    rejected 1-char queries and only matched whole tokens. A
    client-side case-insensitive substring match against title +
    issue_key sits on top now — partial searches just work.
  - **Customer-friendly status labels.** Internal
    `new`/`backlog`/`qa`/`in-progress`/`done`/`delivered`/`accepted`/
    `invoiced` map to four buckets in the table cell, the status
    filter dropdown, and the sidebar pill: Planned / In Progress /
    Ready for Review / Accepted. Type labels follow the same mapping —
    `ticket` reads as "Request", `bug` as "Issue", etc.
  - **Tag display stripped from the portal table.** The CUSTOMERPORTAL
    chip was internal plumbing customers should never have seen; the
    tag filter dropdown is gone from the portal IssueFilterBar.
  - **Estimate / AR table columns dropped.** Backend strip already
    removed the data; the v1 table no longer plays "render as —"
    theatre. The KPI strip now reflects the post-filter set instead
    of project-wide totals so it matches what the table shows below.
  - **Double-arrow bug fixed.** The `← ←` rendered on the "All
    Projects" crumb because both the i18n string AND the template
    prepended an arrow. Dropped from the i18n strings.
  - **IssueSidePanel readonly hardening.** Cost_unit / release /
    sprint membership / estimate / AR rows are now gated on
    `!readonly` — second line of defence on top of the backend strip.

### Internal

- **PAI-476 filed.** Carries the deeper architectural unification
  forward: `mode='customer'` prop on `IssueList.vue`, gating of
  internal-only features, IssueSidePanel API rerouting through portal
  endpoints, deletion of the bespoke `frontend/src/components/issue-list/`
  files. Estimated 2-3 sessions; explicitly out of scope for v1.

## [3.6.0] — 2026-05-20

### Changed

- **PAI-458** (umbrella + PAI-459..PAI-472) — **Customer-portal visibility
  is now opt-in.** The portal previously surfaced every non-deleted
  issue in projects a customer had access to, which leaked internal-only
  types (Memory, Guideline, Runbook, External_system, Related_project)
  along with cross-project notes and operational warnings. A new
  system-managed `CUSTOMERPORTAL` tag gates customer visibility: only
  tagged issues appear in any portal endpoint (overview, projects,
  projects/:id, issues list, issues detail, summary). Internal users
  toggle visibility from `IssueDetailView` (new eye-glyph toggle near
  status/priority) or from the IssueList multi-select bulk toolbar.
  Customer-submitted requests auto-tag on creation.

  Rollout is gated by a one-time migration backfill plus a dry-run env
  var. Migration M110 backfills every existing terminal-status issue
  (`delivered` / `done` / `accepted` / `invoiced`) so that nothing
  visible today disappears on rollout. Setting
  `PAIMOS_PORTAL_VISIBILITY_DRY_RUN=true` leaves the filter off but
  exposes a per-project `would_hide_count` on `/api/portal/overview`
  so operators can gauge the blast radius before flipping live. Unset
  the env var when ready. The rollout playbook lives in
  `docs/CUSTOMER_PORTAL.md`.

  Same release also rebuilds the portal project-detail page (formerly
  a bespoke 517-line view) on top of two new shared components:
  `IssueTable` and `IssueFilterBar` under
  `frontend/src/components/issue-list/`. Layout matches the v2 design
  mock — header card, KPI stat bar, filter card with sliding tab
  strip, accept/reject row actions, mobile slide-up filter sheet,
  responsive column drop to KEY / TITLE / STATUS / Action at <720px.
  Internal IssueList gains an always-visible eye glyph in the type
  cell (survives the tag-chip column being collapsed) and a three-
  state cycle filter chip (visible / hidden / any). An admin-only
  Customer Portal Visibility report at
  `/admin/projects/:id/portal-visibility` surfaces the current set
  plus a paginated audit feed from `mutation_log`, with CSV exports
  for compliance pulls.

  This is a Changed (not Added) entry because the visible surface
  shrinks. After the dry-run grace period customer portals will list
  fewer items per project than before. Per the 2026-05-20 CEO call,
  customer comms are silent — no real customers are in production yet
  on the second-operator instance.

## [3.5.3] — 2026-05-20

### Fixed

- Gosec baseline refresh for the PAI-451 portal-welcome surface
  (3 new G202 findings on portal.go's IN-clause placeholder builder
  — placeholders are `?,?,?` from a fixed int64 slice, no user input;
  same pattern is already baselined for 20+ other handlers) and three
  G703 line shifts on main.go from the new `/portal/overview` route.
  3.5.2's CI security-scan failed on these; 3.5.3 ships the same
  feature with the baseline updated.

## [3.5.2] — 2026-05-20

### Added

- **PAI-451** (umbrella + PAI-452..PAI-457) — Customer portal welcome
  screen, full rewrite. The portal landing page at `/portal` was a bare
  `<h1>Your Projects</h1>` plus a flat grid; it now mirrors the polish
  of the internal Dashboard with a warmer, customer-appropriate voice.
  Greeting hero with avatar and a day-of-year keyed warm-professional
  saying (36 messages × DE + EN parity, separate register from the
  internal "ship it" voice). KPI strip with four counters (active
  projects, open items, awaiting your acceptance, accepted this month);
  the awaiting-acceptance card is keyboard-focusable and scrolls to the
  matching list. "Awaiting your acceptance" section lists `delivered` /
  `done` issues across every accessible project with inline Accept /
  Reject for editors and a muted eye-icon marker (tooltip) for viewers;
  optimistic row removal + KPI recount on action. Refined project cards
  add a segmented status-breakdown bar (hover tooltip per status) and
  a "Letzte Aktivität vor 2h" footer. Recent Projektberichte card
  surfaces the top 5 snapshots across accessible projects with
  pending / accepted badges, linked to the existing `/accept/:code`
  acceptance page; the card hides entirely when the customer has no
  snapshots. One round-trip via new `GET /api/portal/overview` instead
  of N+1 per-project calls; access matches `PortalListProjects` (admins
  see every active project, members/external see only granted ones)
  with project-access gating asserted by three new handler tests.

## [3.5.1] — 2026-05-19

### Added

- **PAI-219** — Per-hunk Keep / Reject controls in the AI diff
  overlay (optimize, optimize_customer, translate, customer_rewrite,
  exec_summary, tone_check). The decision panel below the side-by-
  side diff lists each contiguous edit hunk with its removed / added
  text and a toggle button; Accept emits text assembled from the
  chosen sides instead of the full model output. Default state is
  "Keep all" so muscle-memory users see no behaviour change. Bulk
  "Keep all" / "Reject all" buttons and a "X of Y kept" counter
  round out the panel. 6 new lineDiff tests cover hunk grouping.

## [3.5.0] — 2026-05-19

Customer-facing Projektbericht summary layer and the AI infrastructure
behind it. Eight self-review fixes shipped alongside the feature; ten
bulk-modal hardening tickets landed on top before the release tag.

### Added

- **PAI-418** (umbrella + PAI-420..PAI-449) — `report_summary` column on
  `issues`, populated by two new admin-tunable AI actions:
  `customer_rewrite` (warm Apple-Notes-Stil German release-note copy) and
  `exec_summary` (technical TL;DR for executive readers). Authoring lives
  next to Description / Acceptance Criteria / Notes; the AI menu offers
  both styles on the same field. Projektbericht PDF gets a
  `text_source=tech|report` parameter; portal acceptance defaults to
  `report` so customers see customer-facing copy. Bulk generator
  (BulkGenerateSummaryModal) drives the same flow over a selection of
  issues with a 3-worker concurrent pool, retry-with-backoff for 429/5xx,
  ETA + items/min throughput, sliding log of recent results, resume after
  accidental close (localStorage queue), and pre-run cost estimate.
- **PAI-448** — `cost_micro_usd` now records correctly on every
  `ai_calls` row. The lookup was missing models outside the curated
  bucket list and required a UI hit to `/ai/models` before the cache
  hydrated; fixed by flattening the per-bucket scan and falling back to
  `staticFallbackPayload` when the live cache is nil.
- **PAI-220 / PAI-399 / PAI-401** — closing housekeeping for items that
  had landed in earlier releases (locale-aware Usage panel numbers,
  branded logo on the Projektbericht PDF, count-only subtotal when no
  numeric columns are visible). The implementations were already live;
  this release flips the tickets shut.

### Changed

- **PAI-227** — Settings → AI tab hero copy rewritten. The original
  blurb described only PAI-146 (the v1.8 optimize-only initial release);
  the new copy summarises the thirteen actions, the admin-tunable prompt
  catalog, the daily token cap, and the per-call audit invariant. All
  strings routed through vue-i18n at en + de parity.
- Projektbericht PDF header: title centred horizontally on the page;
  logo, title, and date share the same vertical mid-line at y=6.6mm so
  the row reads as a single line of metadata.
- Projektbericht PDF intro: acceptance clause rewritten to the new
  "21 Werktagen" contract language. The dynamic objection-period value
  is no longer rendered into the report intro (the cooperation config
  retains the field for other surfaces).

## [3.4.11] — 2026-05-18

### Fixed

- **PAI-419** — `paimos issue update --status <terminal> --close-note ...`
  now resolves issue keys to numeric ids before performing the status update,
  close-note comment, and lesson-capture follow-up. This avoids mixed key/id
  write paths and keeps close-note failures attributable to the comment step
  instead of the status update.

## [3.4.10] — 2026-05-18

Projektbericht acceptance workflow on top of the Lieferbericht report work.

### Added

- **PAI-407..PAI-417** — Lieferbericht is promoted to Projektbericht:
  generated PDFs can persist immutable report snapshots, render project
  metadata/permissions and a printable confirmation block, include QR/short
  acceptance URLs, and expose the saved reports in the customer portal.
  The acceptance page can batch-move included `done` / `delivered` tickets
  to `accepted` while leaving non-ready tickets untouched.

- **PAI-418** — Follow-up ticket filed for AI-assisted, German,
  positive-language customer-facing issue summaries. This release does not
  implement that content layer yet.

### Changed

- Report entry points and user-facing labels now use **Projektbericht** while
  the old Lieferbericht API/reporting paths remain as compatibility aliases.

## [3.4.9] — 2026-05-18

Lieferbericht QA follow-up for the v3.4.8 filter/export release.

### Fixed

- **BPOPS26-106** — IssueList Export → Lieferbericht PDF now preserves
  negated status and tag chips. The frontend serializes `!done` /
  `!<tag_id>` values, and the report endpoint applies them as SQL
  exclusions instead of treating them as literal values or dropping
  them.

- **BPOPS26-107** — IssueList active-chip negation now applies
  consistently to complex filter groups, including tags, projects,
  sprints, assignee, cost unit, release, and epic. Negated sprint
  filters are surfaced as unsupported in the Lieferbericht export
  modal instead of being sent to the sprint-scoped report endpoint.

- **BPOPS26-108** — Long Lieferbericht PDFs no longer emit blank
  header/footer-only pages in the middle of the document. The table
  renderer now owns its page-break decisions instead of mixing manual
  checks with fpdf's automatic page breaker.

- **BPOPS26-109** — Lieferbericht PDF tables now use the full A4
  landscape printable width. Hidden numeric columns and leftover page
  width are assigned to the Description column, so the table reaches
  the right margin.

## [3.4.8] — 2026-05-18

Lieferbericht filter + export polish: in-place tag/status filters on
the dedicated report screen, plus an Export → Lieferbericht PDF entry
point on the IssueList that reuses the user's already-active filter
state. Bundles fixes for two visual bugs introduced with v3.4.6's
column toggles, and hardens the branding logo path against corrupt
uploads.

### Added

- **PAI-404** — Lieferbericht filter bar gains two new chip-toggle
  rows: **Tags** (loaded from `/projects/{id}/tags`, project-scoped)
  and **Status** (the 9 canonical status keys, using existing
  `status.*` i18n labels and the report's row-color vocabulary).
  Backend accepts `?tag_ids=` and `?statuses=` on both
  `GET /api/projects/{id}/reports/lieferbericht` and its `/pdf`
  variant; filters AND on top of the scope preset rather than
  replacing it (status excluded by scope=all_open's default still
  doesn't show up — switch to scope=date_range for "delivered only"
  style narrowing).

- **PAI-405** — Export → Lieferbericht PDF action on the IssueList
  toolbar (project-scoped only; the cross-project view has no single
  project to bind to). Opens a small modal with the language picker
  and the 5 numeric column checkboxes, pre-populated from the same
  `paimos:lieferbericht:*` localStorage prefs the dedicated report
  view uses (so the user's prior choices carry over). On download,
  the IssueList's `filterStatus` → `?statuses=`, `filterTags` →
  `?tag_ids=`, and `filterSprints` → `?scope=sprint&sprint_ids=…`
  (or `scope=date_range` with no dates when no sprint is active, so
  the explicit status filter is authoritative). Active IssueList
  filters that don't yet map to the report endpoint (priority,
  type, assignee, cost unit, release, epic) are surfaced as a
  notice in the modal rather than silently dropped.

### Fixed

- **PAI-403** — Lieferbericht PDF visual regressions from v3.4.6:
  - **Header** — the branding logo and "Lieferbericht LB-XXX" title
    no longer collide / float vertically. Logo bumped to h=6mm (auto
    width) at origin (marginL, 4); title text moved to marginL+16,
    y=5.5 so both share the same vertical midline at y≈7.5.
  - **Hidden column headers ghost-extending** — the header loop used
    to call `pdf.CellFormat(0, …)` for hidden numeric columns; fpdf
    interprets `w==0` as "fill to right margin", so e.g. the `AR EUR`
    header painted across the page edge even with the column off. The
    loop now `continue`s past `c.w <= 0`.
  - **Count-only subtotal/grand-total overdraw** — with all numeric
    columns hidden, Description had absorbed their full width and
    `subLabelW == totalW`, so the count cell got width 0 (which fpdf
    treats as full-remaining-width) and overdrew the right-aligned
    label. Replaced with a fixed 38mm count cell carved off the right
    of `totalW`; both label and count stay right-aligned within
    their own cell.

- **PAI-406** — Branding logo path no longer 500s the PDF on corrupt
  or misclassified uploads:
  - Resolver in `reports_logo.go` switched on file extension alone
    and handed raw bytes to fpdf. New `sniffImageFormat([]byte) →
    "png"|"jpg"|"svg"|"ico"|""` validates by magic bytes; mismatch
    returns the embedded `logoPNG` fallback so the PDF always
    renders. Belt-and-suspenders: the renderer probes `pdf.Error()`
    after `RegisterImageOptionsReader` and re-registers the embedded
    mark if fpdf still rejected the bytes.
  - Upload classifier in `branding.go` no longer trusts the
    multipart-declared Content-Type (which a client can spoof) or
    the filename extension. Classifies strictly by the same byte
    sniff and rejects with 400 when nothing matches.

## [3.4.7] — 2026-05-18

### Fixed

- **PAI-400 follow-up** — Lieferbericht PDF: unchecking all five numeric
  column toggles no longer silently reverts to the default all-on
  layout. `url.Values.Get` returns `""` for both "param absent" and
  "param present but empty"; the handler conflated them and the
  frontend always sent `cols=` (empty) when nothing was ticked, so the
  PAI-401 count-only subtotal/grand-total path was unreachable via the
  UI. Now distinguished via the underlying slice — explicit `cols=`
  routes through `parseLBColSet("")` → zero set → count-only fallback.

## [3.4.6] — 2026-05-18

Lieferbericht polish pass — picks up the configured branding logo, adds
per-report column toggles + report-language picker, and gracefully
degrades the totals when no numeric columns are visible.

### Added

- **PAI-400** — `LieferberichtView.vue` gains five persistent checkboxes
  (SP / h / AR SP / AR h / AR EUR). Hidden columns disappear from both
  the on-screen preview and the PDF (via a new `?cols=` query param on
  `GET /api/projects/{id}/reports/lieferbericht/pdf`). State persists in
  `paimos:lieferbericht:cols`; default is all-on. Hidden numeric columns
  release their width to the Description column so the PDF still fills
  horizontally.
- **PAI-401** — When zero numeric columns are visible, the Subtotal and
  Grand Total rows collapse the numeric grid into a single right-aligned
  "{N} {issuesUnit}" cell ("3 issues" / "3 Tickets"). Locale-aware unit
  comes from the new PAI-402 message catalog.
- **PAI-402** — Report-language picker on the form, defaults to the
  authenticated user's `users.locale`. Manual override persists in
  `paimos:lieferbericht:lang`. Sent as `?lang=` on Generate + Download.
  The PDF respects the locale for the header title ("Delivery report" /
  "Lieferbericht"), status labels ("Delivered/In progress/Planned" vs.
  "Geliefert/Umsetzung/Geplant"), column headers ("Type/Summary" vs.
  "Typ/Zusammenfassung"), subtotal/grand-total/page-N-of-M labels, and
  the timestamp format ("January 2, 2026 at 15:04:05" vs.
  "2. Januar 2026 um 15:04:05"). German months are substituted in after
  formatting since Go's `time.Format` only emits English month names.
  Frontend chrome migrated to `useI18n()` keys under the new
  `lieferbericht.*` namespace in `en.ts` + `de.ts`.

### Changed

- **PAI-399** — Lieferbericht PDF header now uses the active instance
  branding logo (uploaded via `POST /api/branding/logo`) instead of the
  hard-embedded `assets/logo.png`. PNG and JPG bytes pass through; SVG
  uploads are rasterized server-side at render time via
  `github.com/srwiley/oksvg` + `rasterx` (pure Go, no cgo, target width
  256 px). Any failure path falls back to the embedded mark so PDF
  generation never breaks because of branding misconfiguration.

## [3.4.5] — 2026-05-18

Lieferbericht PDF download was returning HTTP 500 on any project whose
issues contained emoji or other non-BMP runes (PAI-398).

### Fixed

- **PAI-398** — `Lieferbericht → Download PDF` no longer 500s on
  emoji-bearing content. `github.com/go-pdf/fpdf`'s character-width
  table has 65 536 entries; supplementary-plane codepoints (e.g.
  🚨 U+1F6A8, 🕒 U+1F552 found in real legacy project descriptions) caused
  `SplitText` / `MultiCell` to panic with `index out of range`, which
  `chi.Recoverer` surfaced as 500. `backend/handlers/reports_pdf.go`
  now strips runes > 0xFFFF inside `smartTruncate` (the single
  chokepoint summary, description, and epic-label all flow through),
  replacing them with `?`. The embedded DejaVu Sans font has no emoji
  glyphs anyway, so no visual regression for legitimate text.
  Regression test in `reports_pdf_internal_test.go` renders a report
  with emoji in all three text paths.

## [3.4.4] — 2026-05-14

Knowledge editor toggle polish + archived-state dim (PAI-397
follow-up to PAI-395).

### Fixed

- **PAI-397** — Edit/Preview and Active/Archived toggles in
  `KnowledgeEntryEditor.vue` now show a visible active state.
  PAIMOS's `.btn-ghost.btn-sm + .active` rule is defined per-view
  (canonical at `IssueList.vue:1264`) and the editor was missing its
  copy, so the `.active` class was being applied invisibly. State was
  toggling correctly under the hood (Save persisted the right value),
  but the buttons looked identical regardless of selection.

### Changed

- **PAI-397** — Edit/Preview and Active/Archived render as a proper
  joined segmented control: outer rounded border, no gap, internal
  divider, active button filled with `bp-blue-pale` + `bp-blue-dark`.
  Subtle hover for inactive buttons. Replaces the previous "two loose
  btn-ghost buttons with a gap" treatment.
- **PAI-397** — Archived entries fade their content fields (title,
  slug, metadata, body textarea/preview) at 55% opacity, matching the
  existing `.pku-row--archived` treatment in the list. Status toggle,
  action chrome, and the Active button stay full-strength so
  un-archiving is one obvious click. Promote-to row deliberately
  unchanged (different semantic — action verbs, not a state toggle).

## [3.4.3] — 2026-05-14

Form polish — unified placeholder hint styling across native inputs
and custom-select empty states.

### Changed

- **PAI-396** — Global `::placeholder` rule in `App.vue` sets
  `opacity: 0.5` + `font-size: 0.9em` + `font-style: normal`.
  Normalizes browser-default placeholder rendering (Firefox ≈1.0,
  Chrome ≈0.54) and makes empty vs filled fields read distinctly
  different at a glance. Padding-driven row height means the input
  doesn't visibly resize when typing starts.
- **PAI-396** — `MetaSelect.vue`'s `.meta-select-placeholder` and
  `.ms-label.muted` drop the explicit `color: var(--text-muted)` and
  adopt the same opacity + size treatment so custom-select empty
  state matches native `<input placeholder>` visually.
- **PAI-396** — `IssueSearchInput.vue:299` drops its local
  `::placeholder` color override so the global rule applies (the
  combination of muted color + global opacity rendered too dim).

## [3.4.2] — 2026-05-14

Knowledge tab UX polish (PAI-395). The per-entry editor now opens in a
pinned side panel mirroring `IssueSidePanel`, the Body field's mode
switch and the Active/Archived control both render as segmented
two-button toggles using the global `btn-ghost btn-sm + .active`
vocabulary, and the markdown preview pane gets bounded height +
explicit list-marker padding so bullets and ordered-list numbers stop
clipping on the left.

### Added

- **PAI-395 phase 4** — `KnowledgeSidePanel.vue` mirrors the
  `IssueSidePanel` chrome (overlay vs pinned modes, resize handle on
  left edge with double-click reset, slide transition). Reuses
  `useSidePanelWidth` + `useSidePanelPinned` so AppLayout's
  right-inset math picks up the new panel for free. Selected
  knowledge row gets a `.pku-row--selected` highlight while the panel
  is open. Deep-link `?tab=knowledge&memory=<slug>` still
  auto-opens the matching entry — now into the panel instead of
  swapping the whole tab into editor mode.

### Changed

- **PAI-395 phase 1** — Body field's `Preview` / `Edit` toggle in
  `KnowledgeEntryEditor.vue` is now two adjacent `btn-ghost btn-sm`
  buttons with the active state highlighted. Identical idiom to the
  `Promote to: [Project] [User] [Instance]` row one section up in the
  same component (PAI-345).
- **PAI-395 phase 2** — `Active` / `Archived` control at the bottom
  of the editor is now a segmented toggle. Both states always visible;
  `setStatus('active' | 'archived')` makes the transition explicit.
- **PAI-395 phase 3** — `.ke-preview` gets `max-height: clamp(280px,
  50vh, 640px)`, `:deep(ul, ol) { padding-left: 1.5em }` so list
  markers stop clipping, border / background parity with the textarea
  on Edit ↔ Preview toggle, and minimal styling for blockquote, hr,
  table, th, td.
- **PAI-395 phase 4** — `ProjectKnowledgeUnified.vue` no longer swaps
  list ↔ editor mode; the list is always rendered and the panel is a
  fixed-right sibling. Dead code removed: the `pku-editor-head` chrome
  + the `mode` computed that drove the old branch.

### Fixed

- **PAI-395 phase 2** — Silent bug in the old `toggleArchived()` that
  flipped `active ↔ archived` based on `isArchived` alone, silently
  archiving a `proposed` entry when the toggle was clicked. The new
  segmented toggle uses an explicit `setStatus()`; on a proposed entry
  neither button is `.active` until the user picks a transition.

## [3.4.1] — 2026-05-14

Build-only patch on top of 3.4.0: the gosec baseline didn't follow the
line-number shifts introduced by PAI-394's URL refactor, so the 3.4.0
Docker image was never published. 3.4.1 carries the same code plus the
refreshed baseline.

### Fixed

- Refresh `.gosec-baseline.txt` for PAI-394's line shifts in
  `cmd/paimos/cmd_session_bundle.go`,
  `cmd/paimos/sync/knowledge_resource.go`, and `handlers/tags.go`. No
  new findings — only the (file, line, rule) tuples are updated.

## [3.4.0] — 2026-05-14

Minor release for two agent-discoverability fixes. HTTP-only and MCP
agents can now learn the tag color palette and the knowledge surface
from `/api/schema` alone — no more source-diving or trial-and-error
against `400 invalid color`. The five-alias knowledge plane collapses
into one resource so adding a new knowledge type costs zero new URLs.

### Added

- `enums.tag_colors` on `GET /api/schema` lists the canonical 12-value
  tag color palette. Sourced from `handlers.TagColorPalette` so the
  schema and the server-side validator can't drift (PAI-393).
- `knowledge` block on `GET /api/schema` documents the unified
  knowledge surface — registered types, per-type label and default
  status, request shape, and the full route map. Populated at init
  from the module registry so new knowledge types appear without a
  schema edit (PAI-394).
- `enums.knowledge_types` mirrors the same list as a plain enum for
  clients that prefer the older shape (PAI-394).
- `paimos knowledge` CLI family: `list`, `get`, `create`, `update`,
  `delete`, `promote`, plus `paimos knowledge memory bump-refs / stale
  / proposed-stale` for the memory-specific subroutes (PAI-394).
- OpenAPI surface (`/api/openapi.json`) registers the unified
  `/knowledge` paths with `KnowledgeEntry` and `KnowledgeEntryInput`
  schemas (PAI-394).

### Changed

- **Breaking — HTTP only.** The five per-type knowledge URLs
  (`/api/projects/{id}/memory`, `/runbooks`, `/external-systems`,
  `/related-projects`, `/guidelines`) collapse into one resource:
  `/api/projects/{id}/knowledge[?type=<seg>]` for collections,
  `/api/projects/{id}/knowledge/{type}/{slug}` for entries. Type is
  the kebab-singular URL segment (`memory`, `runbook`, `guideline`,
  `external-system`, `related-project`); the SQL discriminator is
  unchanged. Memory subroutes move under `/knowledge/memory/`. The
  CLI and the SPA migrate transparently; HTTP-only callers must
  update their paths. No data migration required — `issues.type` is
  already the discriminator (PAI-394).
- Schema version bumped 1.2.2 → 1.3.0.

## [3.3.1] — 2026-05-12

Patch polish for the issue table's inline-edit and saved-view ergonomics.

### Added

- Issue-table columns can now be resized from the header edge, double-clicked back to their default width, and restored from saved views through the view/filter snapshot.

### Changed

- Status, priority, and assignee table cells now render as compact read-mode values with a hover/focus edit pencil; the heavier dropdown control only appears while the cell is actively being edited.

### Fixed

- Assignee dropdowns now show a single `Unassigned` option while still allowing that option to clear an assignee.

## [3.3.0] — 2026-05-12

Minor release for the expanded agent/operator surface: tier-1 CLI gaps are
closed, super-admin work is explicit and auditable, and CRM sidecars can now
integrate through a documented HTTP contract.

### Added

- Agent CLI coverage for time entries and attachments. `paimos time start/stop/list/set/get` now covers timer workflows, and `paimos attach <issue> <file>` wraps pending upload + link in one command with rollback if linking fails.
- Project workspace metadata commands in the CLI for repos, cost units, releases, and tags, so agents can discover project context without dropping to REST.
- Super-admin role capabilities, audit events, and impersonation flow with a visible app banner while acting as another user.
- HTTP CRM sidecar provider with request signing, schema documentation, tests, and a runnable example server.

### Changed

- Issue side panel controls now reuse the same status, assignee, and tag interaction patterns as inline table editing, including quick tag removal/addition and dropdown-backed edits.
- Agent interface and install docs now document the current search/tag/time/attachment CLI surface and the remaining REST-only fallbacks.

### Fixed

- Added regression coverage around WAL setup and search-scope shortcuts so the recent SQLite and keyboard-path fixes stay stable.

## [3.2.5] — 2026-05-12

Patch polish for header search editing and undo activity context.

### Fixed

- Header search preserves literal trailing spaces while syncing the active route, so deleting the second word in a query such as `flaky sec` leaves `flaky ` ready for the next word instead of collapsing to `flaky`.
- Undo activity labels now derive the user-facing issue key from `projects.key + issue_number` and append a short title preview, replacing raw fallback labels such as `Issue 1674` with context like `PAI-381 - QA: cleanup push...`.

## [3.2.4] — 2026-05-11

Signed macOS CLI release workflow (PAI-99), per-user search-scope shortcut (PAI-368), CSRF cookie persistence (PAI-370), SQLite WAL test-flake fix (PAI-369), and Account-tab autosave (PAI-371).

### Added

- **PAI-99** — Signed + notarized macOS CLI releases. Every `v*` tag push now produces signed universal binaries for `paimos` and `paimos-mcp` attached to the GitHub Release, alongside Linux `amd64` / `arm64` tarballs and `sha256sums.txt`. Each asset uploads twice (versioned + unversioned alias) so `releases/latest/download/<name>` works in the install one-liner. New `.github/workflows/release.yml`. Pre-release tags (anything containing a hyphen, e.g. `-rc1`) are auto-marked and don't take over `/releases/latest/`.
- **PAI-99** — `docs/INSTALL.md` with `curl | tar` one-liners for macOS (signed) and Linux, signature/checksum verification, and a build-from-source pointer. README install section points at the signed binary first.
- **PAI-368** — Per-user search-scope shortcut. Settings → Account has a click-to-record button that captures any `Ctrl/Alt/Cmd` chord; the App header's matcher reads it from `users.search_scope_shortcut` (new column, M103). Empty string = disabled. Replaces the hard-coded `Ctrl+^` (PAI-364) which was unreachable on some layouts.

### Changed

- **PAI-368** — Header's keybinding hint `<kbd>Ctrl+^</kbd>` next to the scope pill is gone — the chord is now per-user, so a fixed glyph would mislead. The pill's tooltip surfaces the configured chord (or guides the user to set one in Settings).
- **PAI-371** — Account-tab settings autosave with a 600 ms debounce. Replaces the "Save Profile" button + form-error/ok-banner pair with a subtle inline "Saving… / Saved / <error>" indicator. Sequence guard discards stale responses; `onBeforeUnmount` flushes pending saves so navigating away mid-debounce doesn't lose changes.

### Fixed

- **PAI-369** — `PRAGMA journal_mode=WAL` moved out of the per-connection init hook in `backend/db/db.go` and set once at `Open()` instead. The hook ran the pragma on every new pool connection; each invocation briefly took an exclusive lock on the DB header and raced concurrent transactions, throwing `SQLITE_BUSY` in ~10–15% of CI runs (`TestBatchUpdate_AllScalarFields`). `journal_mode` is a database-level setting persisted in the file header, so subsequent connections inherit it without touching the pragma.
- **PAI-370** — CSRF cookie now persists for the full session-cookie lifetime (90 days) instead of being a browser-session cookie. Pre-fix, closing and reopening the browser left users logged in but with `X-CSRF-Token` blank on every POST → all mutations 403'd silently. Middleware additionally re-issues the CSRF cookie when a valid session arrives without one, so already-broken sessions heal on the next request without forcing a logout.
- **PAI-370** — `Save View` dialog now catches API errors and surfaces them inline. The previous `try { ... } finally { ... }` (no `catch`) let failures propagate silently, leaving the modal open with no feedback — that's how PAI-370's CSRF bug stayed invisible.
- **PAI-371** — `search_scope_shortcut` is now persisted across `GET /api/auth/me` (was reading back as empty even though the DB had the value). The users-table projection in `backend/auth/auth.go` (used by `MeHandler` / login / session middleware) is a twin of `backend/handlers/user_helpers.go`; PAI-368 updated only the handler twin. Both are now in lock-step.

### Notes

- Both PAIMOS instances (ppm + the then-second-operator instance) were running PAI-368 / 369 / 370 via per-commit sha images before the cut. PAI-371's auth.go twin fix means re-deploying to the v3.2.4 image is required for the search-scope shortcut to actually persist end-to-end.
- The macOS release workflow is signed under "Developer ID Application: Markus Barta (P66J39QV6V)" (personal Apple Developer account) — chosen so paimos's signing-cert revocation blast radius is isolated from any future externally-distributed binaries.

## [3.2.3] — 2026-05-10

Search-scope keybinding fix (PAI-364). Replaces Ctrl+Tab (browser-intercepted everywhere) with Ctrl+^ and moves the hint outside the pill.

### Changed

- **PAI-364** — Scope-toggle keybinding switches from `Ctrl+Tab` to `Ctrl+^`. Browsers capture Ctrl+Tab for native tab cycling before JS sees the event; Ctrl+^ is layout-portable (US Ctrl+Shift+6, German Ctrl+^ direct) and not reserved by any platform.
- **PAI-364** — Keybinding hint moves OUT of the pill into a sibling `<kbd>Ctrl+^</kbd>` element, muted-gray, smaller font, vertically centered to the pill. The pill goes back to a clean `<folder> PAI` (single-purpose toggle affordance); the hint reads as ambient help.
- **PAI-364** — `onKeydown` detects `Ctrl+^` via `e.ctrlKey && (e.key === '^' || e.code === 'Backquote')` so both US-layout-via-Shift+6 and German-layout-direct-key signals are caught. The German dead-key fallback uses `e.code === 'Backquote'` since `key` may arrive as `'Dead'` before composition resolves.

## [3.2.2] — 2026-05-10

Search-pill polish (PAI-363). Three small follow-ups on PAI-362.

### Changed

- **PAI-363** — Scope pill now shows `^⇥` (Ctrl+Tab) instead of bare `⇥`. Both glyphs render in their own spans so each can be optically centered against the label baseline (`^` lands high in its em-box, `⇥` slightly lower; per-glyph `transform` aligns them).
- **PAI-363** — Tooltip on the pill: "Press Control+Tab to switch to {Global|this project}".
- **PAI-363** — Keybinding rewired: `Ctrl+Tab` (from anywhere inside the search field, regardless of palette visibility) toggles project ↔ global scope. Plain `Tab` is no longer captured — native focus movement returns.

## [3.2.1] — 2026-05-10

Search header polish (PAI-362). Cosmetic patch on top of v3.2.0.

### Changed

- **PAI-362** — The search-context pill moves out of the search input and sits as a sibling chip to the right of it. The "Project:" caption is gone (the folder icon + standalone position already convey scope). The `×` icon is replaced with the tab-key glyph `⇥` since the pill toggles scope (Tab key already wired) rather than removes it. Right-padding hack on `.ah-search-input` for `has-scope` is dropped; pill height matches input at 32px so the row reads as two parallel chips.

## [3.2.0] — 2026-05-10

Layout structure cleanup (PAI-361). Three coordinated changes inside `<main>`: uniform 20px padding, drop the redundant `.view-body` wrapper, move the footer slot to be a peer of `AppHeader`.

### Changed

- **PAI-361** — `.main-content` padding is now uniform `1.25rem` (20px) on all four sides. The previous `2rem 2.5rem` (with a `:has()` override dropping bottom to `.5rem` for self-scroll views) collapses into one rule. Mobile breakpoint stays uniform at `1rem` (16px).
- **PAI-361** — `.view-body` and `.view-body--self-scroll` wrappers deleted. The `<slot />` is now a direct child of `.main-content`. Self-scroll routes (`/issues`, `/projects/:id`) toggle a class `.main-content--self-scroll` on `.main-content` itself which swaps `overflow-y: auto` for `overflow: hidden`. Each self-scroll route's root (`.pd-page`, `.issues-view-root`) already declares `flex: 1; min-height: 0` and owns the flex contract directly.
- **PAI-361** — `#project-footer-slot` moved out of `.main-content` to be a peer of `<AppHeader>` under `<main>`. The slot now naturally pins to the viewport bottom of `<main>` and spans the full main width without margin tricks. The `:has()` selector cascade and the negative-margin escape (`-2.5rem -2.5rem -.5rem`) are gone.
- **PAI-361** — `ProjectFooterBar`'s interior padding drops from `0 2.5rem` to `0 1.25rem` so the leftmost tab label aligns with the new 20px page-content gutter.

### Notes

- `<main>`'s flex column now reads cleanly: `<AppHeader>` (top chrome) / `.main-content` (flex:1, padded content) / `#project-footer-slot` (bottom chrome). Top and bottom chrome are structural peers.
- No visual regressions on `/`, `/projects/:id`, `/issues`, `/settings` (smoke-tested locally; SPA build clean; 193 frontend tests pass).
- IssueList sticky thead + frozen columns still work — table-wrap remains the bounded scroll viewport.

## [3.1.0] — 2026-05-10

Knowledge tab redesign (PAI-360). Unified list with type filter chips replaces the 5-pill sub-nav.

### Changed (BREAKING UX)

- **PAI-360** — Knowledge tab is now ONE unified list. The five-category pill sub-nav (Memory / Runbooks / External Systems / Related Projects / Guidelines) is **deleted**. Type is a *filter*, not a navigation primitive: a single scrollable list shows all entries interleaved by recency, with toggleable filter chips on top (click = toggle, shift-click = solo). Each row has a small left-side type badge with subtle type-coding.
- **PAI-360** — Search box queries title/slug/body across all currently-filtered types. Add Entry button has a dropdown picking the category before opening the editor.
- **PAI-360** — Editor opens in-place (list collapses, editor takes the panel area) with a Back button. Deep-link `?tab=knowledge&memory=:slug` preserved — the unified view auto-applies the `memory` chip filter and pre-opens the matching slug.

### Removed

- `frontend/src/components/project/knowledge/ProjectKnowledgeTab.vue` (205 lines)
- `frontend/src/components/project/knowledge/KnowledgeCategoryPanel.vue` (827 lines)

### Added

- `frontend/src/components/project/knowledge/ProjectKnowledgeUnified.vue` (~570 lines including styles).

## [3.0.2] — 2026-05-09

Footer-bar IA collapse (PAI-359). Six tabs in one continuous full-width row. Breaking UX change vs. v3.0.1.

### Changed (BREAKING UX)

- **PAI-359** — `ProjectFooterBar` extends from 3 to 6 tabs in one row: **Issues / Overview / Knowledge / Docs / Coop / Context**. Same neutral active-tint, same SSOT counts. The legacy `.pd-workspaces` rail (Docs/Coop/Context as slide-in dock toggles, PAI-279) is **deleted**. Users who hit the dock pattern now see Docs/Coop/Context as full-page peer views — clicking Docs replaces the IssueList view rather than opening a panel beside it. Trade-off accepted: the dock was already half-broken (only rendered on the Issues tab); the unified IA is one canonical project-navigation surface.
- **PAI-359** — Footer bar's full-width clip fixed via `<Teleport to="#project-footer-slot">`. The slot lives in `AppLayout` outside `.view-body--self-scroll`'s `overflow:hidden` so the bar spans the entire `.main-content` width without margin tricks.

### Removed

- `frontend/src/composables/useProjectAuxPanels.ts` — orphaned composable from PAI-279's dock pattern; not consumed outside its own test file.
- `.pd-workspaces`, `.pd-workspace-dock`, `.pd-workspace-rail`, `.pd-context-overview`, related transitions and media queries (~150 lines of scoped CSS).
- `useSidePanelExclusion` imports from `ProjectDetailView` — primaryTab is mutually exclusive by construction.

### Notes

- `?tab=docs|coop|context` deep-links resolve correctly against the new tab keys.
- Counters for Docs / Coop / Context populate via the always-mounted sentinel components (existing `@count` / `@populated` emits, just routed to the footer instead of the rail).

## [3.0.1] — 2026-05-09

CI-only fix on top of v3.0.0. v3.0.0's tag never produced a Docker image because the dogfood-anchor verifier (`paimos anchors verify`) flagged `PAI-72` as an orphaned anchor pointing at the now-deleted `backend/cmd/paimos/cmd_manifest.go:39`. PAI-72 was the manifest mirror command; the file was removed in PAI-358 along with the rest of the legacy manifest surface, but its entry in `.paimos/anchors.json` survived the deletion. v3.0.1 carries the same code as v3.0.0 plus the index update.

### Changed

- Removed PAI-72's entry from `.paimos/anchors.json`. The manifest mirror CLI verb (`paimos manifest pull`) was deleted in PAI-358; the anchor is permanently dangling.

## [3.0.0] — 2026-05-09

Major-version bump for the **breaking** removal of the legacy project_manifests surface (PAI-358), bundled with v2.9.1's footer-bar polish (count SSOT, full-width chrome, neutral active state).

### Removed (BREAKING)

- **PAI-358** — Legacy `project_manifests` table dropped via M102. Pre-flight assertion in M102 fails closed if any project still has non-empty manifest content lacking a `_migrated_at` marker; operators upgrading from v2.9.x with legacy data must run `paimos migrate manifest-to-knowledge --project KEY` against each populated project on v2.9.1 first.
- **PAI-358** — Endpoints `GET /api/projects/:id/manifest`, `PUT /api/projects/:id/manifest`, and `POST /api/projects/:id/migrate-manifest-to-knowledge` (the v2.9 transition helper) are gone.
- **PAI-358** — `paimos manifest pull` and `paimos migrate manifest-to-knowledge` CLI verbs removed.
- **PAI-358** — Frontend: `ProjectManifestTabs.vue` and the manifest editor in `ProjectContextSection.vue` deleted; `ProjectContextSection` now only owns the repo list. The `ProjectManifest` TS type is gone.
- **PAI-358** — AI actions `structure_manifest`, `structure_guardrails`, `structure_glossary`, `structure_dev`, `structure_ops` removed (no host).

### Changed

- **PAI-356/-358 polish** — Project footer bar now shows the same Issues count as the IssueList header (SSOT against the loaded list, not a separate `open_issues` aggregate). Bar spans the full project-page width by escaping the `.main-content` padding (no more white gutters at the edges). Active-tab styling moved from accent-color text + green/blue badges to a neutral `color-mix(--brand-blue 12%)` tint that matches the sidebar's `nav-item.active` family — no fresh accents, just the existing chrome highlight.

### Migration notes

Upgrade path from v2.9.1:
1. On v2.9.1, run `paimos migrate manifest-to-knowledge --project KEY` for every project with populated manifest content. Idempotent + dry-run-able.
2. Upgrade to v3.0.0. M102's pre-flight asserts that every populated manifest carries a `_migrated_at` marker; the migration aborts loudly otherwise.
3. The legacy editor and the migration helper are both gone in v3.0.0 — there is no second chance to migrate after upgrading.

## [2.9.1] — 2026-05-09

CI-only fix on top of v2.9.0. v2.9.0's tag never produced a Docker image because the gosec baseline gate flagged 3 line-shifted G703 findings in `backend/main.go` (the new `/migrate-manifest-to-knowledge` route at line 332 shifted three pre-existing path-traversal taints by 5 lines each). v2.9.1 carries the same code as v2.9.0 plus the refreshed baseline.

### Changed

- Refreshed `.gosec-baseline.txt` to absorb three line-shifted G703 findings in `main.go`. No new SAST exposure — same code, new line numbers. Same pattern as v2.7.0→v2.7.1 and v2.8.1→v2.8.2.

## [2.9.0] — 2026-05-09

Post-ship redesign on top of v2.8.x. Two tickets close out the v2.8 cycle's UX feedback loop: the project-page primary tab strip moves from top to bottom, and the legacy `project_manifests.data` blob gains a deterministic migration path into the PAI-338 knowledge plane.

### Added

- **PAI-356** — `<ProjectFooterBar>` replaces the top tab strip on the project page. Same three identities (Issues / Overview / Knowledge), 40px hairline strip with a 2px green top-border active marker, anchored at the bottom of the project content area via `margin-top: auto`. Counters next to "Issues" (open, excluding knowledge entries) and "Knowledge" (memory + runbook + external_system + related_project + guideline excluding cancelled). v-model preserves PAI-339's `primaryTab` API so PAI-342's `?tab=knowledge&memory=:slug` deep-link still resolves.
- **PAI-356** — `GET /api/projects/{id}` response gains a `counts` aggregate (`open_issues`, `knowledge_entries`). One indexed scan; absent on list responses.
- **PAI-357** — `POST /api/projects/{id}/migrate-manifest-to-knowledge` admin-only endpoint. Migrates the legacy `project_manifests.data` blob deterministically: top-level non-`_` keys → 1 runbook (`legacy_manifest`); `_guardrails[i]` → 1 guideline per entry; `_glossary[term]` → 1 memory with `category_metadata.type=reference`; `_dev`/`_ops` → `project_agents.body`. Idempotent via `data._migrated_at` marker. Knowledge writes flow through the canonical `knowledge.CreateEntryHook` (PAI-353) so migrated entries pick up `issue_history` snapshots, `mutation_log` rows, and SSE notifications.
- **PAI-357** — `paimos migrate manifest-to-knowledge --project KEY [--dry-run] [--force]` CLI verb, thin wrapper over the endpoint.
- **PAI-357** — Admin "Migrate to Knowledge…" button in the legacy banner above `ProjectManifestTabs`. Two-step: dry-run preview lists planned writes + conflicts, then Commit / Force commit applies.

### Changed

- **PAI-356** — `ProjectContextSection` now renders a soft-deprecation "Legacy" banner above the manifest editor when populated, linking to PAI-357. The legacy editor stays mounted; PAI-358 will delete it after the 30-day window.

### Notes

- **PAI-358** (legacy `ProjectManifestTabs` + `project_manifests` table delete) is filed but blocked until PAI-357 has been live for 30 days and every project's manifest has either been migrated (`data._migrated_at` set) or explicitly opted out of migration. Pre-flight assertion in PAI-358's eventual migration fails closed otherwise.

## [2.8.2] — 2026-05-09

CI-only fix on top of v2.8.1. v2.8.1's tag never produced a Docker image because of a third pre-existing CI gate: `gosec` flagged 24 new SAST findings on the v2.8.0 cycle's file-handling and SQL-concat patterns (G202 / G204 / G301 / G304 / G306 / G703). All are line-shift drift from accepted patterns plus a few new-but-equivalent ones from PAI-330's adapter exec, PAI-340's cache writer, and PAI-354's mutation_log queries. v2.8.2 carries the same code as v2.8.1 plus the baseline refresh.

### Changed

- Refreshed `.gosec-baseline.txt` to absorb the v2.8.0 cycle's new file-write / file-read / SQL-concat findings (110 → 134 entries). Same pattern as v2.7.0→v2.7.1 and v2.7.2→v2.7.3.

## [2.8.1] — 2026-05-09

CI-only fix on top of v2.8.0. v2.8.0's tag never produced a Docker image because of two pre-existing CI failures unrelated to the cycle's feature work: govulncheck flagged Go stdlib vulns published after v2.7.3 (`GO-2026-4971` in `net@go1.25.9`, `GO-2026-4918` in `net/http@go1.25.9` — both fixed in `go1.25.10`), and `TestBatchUpdate_AllScalarFields` flaked under concurrent test load with `SQLITE_BUSY` on the WAL PRAGMA. Both are addressed at the workflow layer; v2.8.1 carries the same code as v2.8.0 plus the workflow update.

### Changed

- `check-latest: true` on both `setup-go` steps in `.github/workflows/ci.yml` so the runner pulls the latest 1.25.x patch every run. Resolves both govulncheck findings without a manual workflow bump on each new stdlib advisory.
- `-p 1` on the backend `go test` invocation, serializing per-package execution. Eliminates the SQLITE_BUSY flake when the handlers and db packages set up their in-memory test DBs concurrently.

## [2.8.0] — 2026-05-09

Three-pillar agent metadata cycle. The work spans three sibling epics — caller-session attribution (who did what), skill scaffolding from project metadata (who can do what), and the knowledge plane (what they need to know). Together they move project metadata to be the durable upstream of any agent harness or machine: local files become a cache, paimos becomes SSOT.

Per the foundational decision in [PAI-346](https://pm.barta.cm/projects/6/issues/PAI-346), the knowledge plane is implemented by extending the existing `type` enum on `issues` rather than as separate tables. Memory entries inherit history snapshots, comments, tags, FTS, parent-child, soft-delete, and undo for free. Roughly 1/3 the implementation work the original spec called for.

### Added — [PAI-323](https://pm.barta.cm/projects/6/issues/PAI-323) epic (caller-session attribution)

- [PAI-324](https://pm.barta.cm/projects/6/issues/PAI-324) — `agent_name` + `session_id` columns on `issue_history` (M93). Every issue write reads `X-Paimos-Agent-Name` / `X-Paimos-Session-Id` headers and persists them on the snapshot. History panel renders a sub-line under "changed by" when either is set.
- [PAI-325](https://pm.barta.cm/projects/6/issues/PAI-325) — paimos CLI auto-forwards `PAIMOS_AGENT_NAME` / `PAIMOS_SESSION_ID` env vars (or `--agent-name` / `--session-id` flags) on every write. `paimos doctor` shows current attribution state with source provenance.
- [PAI-326](https://pm.barta.cm/projects/6/issues/PAI-326) — declarable `agents[]` per project (M94). CRUD endpoints + Edit Project modal panel. Each agent: name, description, slash command, lane tags, metadata.
- [PAI-327](https://pm.barta.cm/projects/6/issues/PAI-327) — `paimos session start --project --agent` mints a session UUID, validates the agent name, exports env vars. Companion `session show` / `session end` shipped.
- [PAI-354](https://pm.barta.cm/projects/6/issues/PAI-354) — attribution extended to comment-add / tag-add / relation-add via `mutation_log` (M101). Per-mutation feed becomes the authoritative attribution surface; history stays the snapshot view.

### Added — [PAI-328](https://pm.barta.cm/projects/6/issues/PAI-328) epic (skill scaffolding from project metadata)

- [PAI-329](https://pm.barta.cm/projects/6/issues/PAI-329) — extended `project_agents` schema with body / bootstrap_steps / non_negotiable_rules (M95). New project-level `project_environments` + `project_deploy_recipes` tables. Canonical agent artifact at `GET /api/projects/:id/agents/:name.json` + debug `.md` endpoint.
- [PAI-330](https://pm.barta.cm/projects/6/issues/PAI-330) — `paimos skill render --harness claude-code` with adapter dispatch. claude-code reference adapter ships in-tree at `backend/cmd/paimos/adapters/claudecode/`. `--check` drift detection (exit 0/1/2). `paimos skill list-adapters` enumerates registered adapters.
- [PAI-331](https://pm.barta.cm/projects/6/issues/PAI-331) — generic `paimos sync` module + Resource interface + Registry. `paimos sync init/pull/watch/check`. Server SSE at `/api/projects/:id/agents/events` + `.rev` polling fallback. `auto_watch_subscriptions` table (M98). Settings → Account "Auto-watch sync" panel with per-(device, project) toggle.
- [PAI-332](https://pm.barta.cm/projects/6/issues/PAI-332) — adapter SDK formalized: JSON manifest format with `protocol_version: "1"`, execution contract (render / describe / validate verbs, exit codes), Bosun-style SemVer, `$PAIMOS_ADAPTER_PATH` discovery. `paimos skill test-adapter` conformance suite. Public registry at `GET /api/registry/adapters`. Spec doc at `docs/adapter-protocol.md`.

### Added — [PAI-337](https://pm.barta.cm/projects/6/issues/PAI-337) epic (knowledge plane)

- [PAI-338](https://pm.barta.cm/projects/6/issues/PAI-338) — knowledge schema (M96): `type` enum extended with `memory | runbook | external_system | related_project | guideline`; `status` enum extended with `archived | proposed`; `slug` + `category_metadata` columns. Five thin convenience endpoints under `/api/projects/:id/{memory|runbooks|external-systems|related-projects|guidelines}`. Default-hide of knowledge types from project issue list.
- [PAI-339](https://pm.barta.cm/projects/6/issues/PAI-339) — workspace UI redesign: project page tabs (Issues / Overview / Knowledge). Knowledge tab with filter / search / sort / bulk-archive across all 5 categories. Inline markdown editor.
- [PAI-340](https://pm.barta.cm/projects/6/issues/PAI-340) — `paimos session start --bundle full` exports memory + runbooks + external_systems + related_projects + guidelines. `--format env|json|files`. Local cache manifest with rev-based invalidation. `--refresh` forces re-fetch.
- [PAI-341](https://pm.barta.cm/projects/6/issues/PAI-341) — knowledge sync plugs into PAI-331's generic module: 5 Resource implementations + `Publish<Kind>Changed` helpers + per-kind `.rev` endpoints.
- [PAI-342](https://pm.barta.cm/projects/6/issues/PAI-342) — bidirectional ticket↔memory linking via `issue_relations` (M97). Server-side auto-suggest scoring on `GET /api/issues/:id/applicable-memories?suggest=1`. UI on issue detail + memory editor.
- [PAI-343](https://pm.barta.cm/projects/6/issues/PAI-343) — lesson capture at ticket-close. Trigger detection on `GET /api/issues/:id/lesson-capture-prompt`. UI modal post-save. CLI `--draft-memory` flag for headless capture.
- [PAI-345](https://pm.barta.cm/projects/6/issues/PAI-345) — cross-scope memory promotion. `users.user_id` column on issues (M99). `/api/users/me/memory` + `/api/instance/memory` CRUD. `POST /api/memory/:slug/promote`. Bundle merge order: project > user > instance.
- [PAI-347](https://pm.barta.cm/projects/6/issues/PAI-347) — confidence + decay (M100). `reference_count` + `last_referenced_at` columns. Bundle excludes low confidence by default; `--include-low` opts in. `GET /api/projects/:id/memory/stale` returns archive proposals.
- [PAI-348](https://pm.barta.cm/projects/6/issues/PAI-348) — memory inheritance from `related_projects[]`. Bundle folds inherited memory / runbooks / guidelines from upstream projects (roles `upstream-tool` / `philosophy` / `infra`) with project-precedence on slug collision. Cross-instance fetch with graceful degradation.
- [PAI-349](https://pm.barta.cm/projects/6/issues/PAI-349) — bot-authored memory drafts. `paimos memory propose` verb. `proposed` status with Knowledge tab inbox + accept / edit / reject. Bundle excludes proposed by default; `--include-proposed` opts in. Per-session rate limit (default 5 / 24h, env-overridable).
- [PAI-352](https://pm.barta.cm/projects/6/issues/PAI-352) — `paimos onboard --project --agent` produces a human-readable briefing (md / html). `--check` drift mode. Reuses the bundle data.
- [PAI-353](https://pm.barta.cm/projects/6/issues/PAI-353) — knowledge writes flow through hooks that mint `issue_history` + `mutation_log` rows. Knowledge edits inherit PAI-324 attribution + PAI-209 undo for free.

### Migrations

Nine schema migrations run on first startup. All additive; existing data rolls forward unchanged.

- **M93** — `issue_history.agent_name` + `session_id` (PAI-324)
- **M94** — `project_agents` table (PAI-326)
- **M95** — `project_agents` extension columns + `project_environments` + `project_deploy_recipes` (PAI-329)
- **M96** — `issues` recreate to extend type CHECK + status CHECK + add `slug` + `category_metadata` (PAI-338, gated by PAI-346); also recreates `issue_anchors` and `ai_calls` to dodge the SQLite FK-rewrite bug for tables created after the prior issues recreate
- **M97** — `issue_relations.type` extended with `applies_to_memory` (PAI-342)
- **M98** — `auto_watch_subscriptions` table for per-(device, project) sync toggle (PAI-331)
- **M99** — `issues.user_id` + partial index for cross-scope memory (PAI-345)
- **M100** — `issues.reference_count` + `last_referenced_at` (PAI-347)
- **M101** — `mutation_log.agent_name` + `session_id` (PAI-354)

### Deferred to next cycle

Filed but not in v2.8.0:

- [PAI-333](https://pm.barta.cm/projects/6/issues/PAI-333) — extract claude-code adapter to its own repo
- [PAI-344](https://pm.barta.cm/projects/6/issues/PAI-344) — legacy-project migration script (gated by UI QA)
- [PAI-350](https://pm.barta.cm/projects/6/issues/PAI-350) — knowledge graph view (UI discovery)
- [PAI-351](https://pm.barta.cm/projects/6/issues/PAI-351) — memory dependency graph (UI discovery)

## [2.7.3] — 2026-05-07

CI-only fix on top of v2.7.2. PAI-321's `ClearMustChangePassword` call shifted two `SetCookie` sites in `auth.go` (LoginHandler + LogoutHandler) by one line each, tripping the gosec line-keyed baseline. Refreshed the baseline; same `Secure: cookieSecure` variable pattern that's already accepted on every other cookie call site. v2.7.2's tag never produced a Docker image because of the failing gate; v2.7.3 carries the same code plus the baseline refresh.

### Changed

- Refreshed `.gosec-baseline.txt` to absorb the line-shift drift introduced by PAI-321.

## [2.7.2] — 2026-05-07

Two bug fixes / feature follow-ups on top of v2.7.1.

### Added

- [PAI-335](https://pm.barta.cm/projects/6/issues/PAI-335) — **Super-admin can edit / add time entries on behalf of other users.** New `users.is_super_admin` column (M92), orthogonal to the existing role enum, with `mba` backfilled to 1 — the single super-admin per the operator's intent. Time-entry endpoints accept an optional `user_id`; non-super-admins get 403 when sending one. The rate snapshot reads the **target** user's `internal_rate_hourly` so the super-admin's rate never silently shadows the worker's hours in accruals reports. Every cross-user write emits a structured `audit: super_admin_act` log line. Frontend create form grows a "Log on behalf of" picker visible only to super-admins, with an amber "Acting as <username>" badge when the picked user is not self.

### Fixed

- [PAI-240](https://pm.barta.cm/projects/6/issues/PAI-240) — **`detect_duplicates` and `find_parent` no longer log a misleading "successful AI call" on projects with no peer issues.** Both handlers used to return an empty body which the dispatcher recorded as `outcome=ok / model=— / tokens=0` — operationally indistinguishable from a real provider call that found nothing. New `outcomeNoOp` enum value plus a `noOpResult` body marker that the dispatcher type-switches on: records `outcome=no_op`, skips the usage meter, and surfaces the human-readable reason via the response envelope. SPA renders the explicit reason in the modal + inline strip + result-summary composable instead of the old "No similar issues found." empty-results copy.

## [2.7.1] — 2026-05-07

CI-only fix release. Refreshes the `gosec` SAST baseline so the line-number shifts introduced by the v2.7.0 commits stop tripping the security-scan gate. The two genuinely new `SetCookie` findings (the expiry cookie in `clearSessionCookie` and the middleware slide-renewal cookie) are both benign — the first is an expiry response where the flags don't matter, the second uses the same `Secure: cookieSecure` pattern that's already accepted on `LoginHandler`'s cookie. No runtime change vs v2.7.0; the v2.7.0 tag never produced a Docker image because of the failing gate.

### Changed

- Refreshed `.gosec-baseline.txt` to match current line numbers and accept the two benign new SetCookie sites added in PAI-322.

## [2.7.0] — 2026-05-07

Auth + session reliability release. Sessions stop dying mid-task once a day; admin permission changes take effect on the next request without forcing the affected user to re-login; new accounts get a forced password-change flow before they can do anything; attachment uploads no longer carry SVG/HTML script payloads.

### Fixed

- [PAI-110](https://pm.barta.cm/projects/6/issues/PAI-110) — **Block active-content uploads + safe-serve attachments.** Upload path now sniffs the first 512 bytes regardless of client-supplied Content-Type and rejects HTML, SVG, JS, and executable types with HTTP 415; payload-shape detection catches `<svg>` / `<script>` / `<!doctype html>` even when the declared type is benign. Serve path re-sniffs on every request and forces `Content-Type: application/octet-stream` + `Content-Disposition: attachment` for anything outside a small inline allowlist (PNG, JPEG, GIF, WebP, PDF), with a per-response `Content-Security-Policy: default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox`. Closes the SVG-XSS path where `AttachmentLightbox` was rendering same-origin SVGs inline.
- [PAI-322](https://pm.barta.cm/projects/6/issues/PAI-322) — **Sessions no longer die mid-task once a day.** Replaces the 24h hard cap with a 30-day sliding window (renewed on every authenticated request, throttled to writes when remaining TTL falls below half) and a 90-day absolute cap measured from a new `sessions.created_at` (M89). Cookie `Expires` is anchored to the absolute cap so the browser keeps presenting it across renewals. Backend exposes `X-Session-Expires-At` on every authed response so the SPA can drive a low-key pre-expiry signal as the absolute cap nears.
- [PAI-322](https://pm.barta.cm/projects/6/issues/PAI-322) — **The "unauthorized" toast → "session expired" banner double-message is gone.** The old banner is replaced by a centered non-dismissible `SessionExpiredModal` that captures the current URL and deep-links the user back via `?redirect=` after sign-in. A new `SessionExpiredError` distinct from `ApiError` lets `errMsg` suppress the per-action toast that used to race the banner.
- [PAI-322](https://pm.barta.cm/projects/6/issues/PAI-322) — **Multi-tab session state is now consistent.** A `BroadcastChannel("paimos-auth")` broadcasts session-expired on first 401 and session-restored on successful login, so every open tab converges on the same modal and a single re-login dismisses it everywhere.

### Added

- [PAI-322](https://pm.barta.cm/projects/6/issues/PAI-322) — **Password change invalidates every other session for the user.** Mirrors GitHub / Google: the current session stays alive, all other devices and API keys are invalidated immediately. Plus a 5-minute `/auth/me` heartbeat in `App.vue` (gated by `document.visibilityState === "visible"`) catches slow-dying sessions for users who keep a tab open without clicking.
- [PAI-320](https://pm.barta.cm/projects/6/issues/PAI-320) — **Role / membership / status changes take effect on the next request without re-login.** New `users.permissions_epoch` counter (M90) is bumped on every change to a user's role, status, or project membership; middleware emits `X-Permissions-Epoch` on every authed response (session-cookie and API-key paths). The SPA watches the header and triggers `refreshMe()` on a change to re-hydrate `auth.user` + `accessibleProjects` — soft refresh, no password retype.
- [PAI-321](https://pm.barta.cm/projects/6/issues/PAI-321) — **Force password change on first login.** New `users.must_change_password` column (M91) and a `MustChangePasswordGate` middleware mounted on every authed route group. The admin user-create form grows a "Force password change on first login" checkbox (default ON, opt-out for service accounts). When the flag is set, the backend returns 403 `{"error":"must_change_password"}` everywhere except `/auth/me`, `/auth/password`, and `/auth/logout`; the SPA's 403 interceptor routes the user to a new `FirstLoginView` with inline rule list, "Why am I seeing this?" disclosure, and a sign-out escape hatch. `ChangePassword` clears the flag on success. Same flow works for external users via the same `POST /api/users` path.

### Database

- **M89** — `sessions.created_at TEXT NOT NULL` (UPDATE-backfilled in the same migration since SQLite forbids non-constant DEFAULTs on `ALTER TABLE`).
- **M90** — `users.permissions_epoch INTEGER NOT NULL DEFAULT 0`.
- **M91** — `users.must_change_password INTEGER NOT NULL DEFAULT 0`.

## [2.6.0] — 2026-05-06

### Fixed

- **Bulk Change modal: assignee/status/priority/parent/cost-unit/release no longer return "internal error" on parallel writes.** The modal previously fired one `PUT /api/issues/{id}` per selected issue via `Promise.all`, which raced the SQLite single-writer and failed 6-of-8 calls in reproduction. The modal now sends a single atomic `PATCH /api/issues` per chunk; `writeBatchError` returns a `error` summary alongside `errors[]` so the modal renders the real cause instead of `request failed`.
- [PAI-314](https://pm.barta.cm/projects/6/issues/PAI-314) — **Bulk Change sprint mode no longer silently swallows relation failures.** Per-issue sprint add/remove now stops on first error and surfaces `N/M updated before failure (issue PAI-X): <reason>` instead of completing without feedback.
- [PAI-315](https://pm.barta.cm/projects/6/issues/PAI-315) — **Issues can clear nullable columns via the API.** `assignee_id`, `parent_id`, `total_budget`, `rate_hourly`, `rate_lp`, `estimate_hours`, `estimate_lp`, `ar_hours`, `ar_lp`, `time_override`, and `color` accept explicit JSON `null` to clear them. Previously the SQL used `COALESCE(?, col)` with typed pointers, collapsing absent-key and explicit-null to the same nil and silently no-op'ing the unset; presence-based parsing distinguishes them and uses `CASE WHEN ? = 1 THEN ? ELSE col END` for the truly-nullable columns.
- [PAI-316](https://pm.barta.cm/projects/6/issues/PAI-316) — **`POST /api/undo/request/{requestID}` now reverts every row of a bulk batch in one call.** The previous loader used `LIMIT 1`, so undoing a 100-row bulk required 100 undo calls; the new anchor-then-expand loader detects the shared `batch_id` and atomically reverts the whole batch (with redo as the symmetric mirror).

### Added

- [PAI-317](https://pm.barta.cm/projects/6/issues/PAI-317) — **Bulk Change modal aborts in-flight chunks when closed mid-bulk.** A per-execution `AbortController` is wired through the API client's new `RequestOptions.signal`; closing the modal during a long bulk now stops new PATCH calls and shows `Cancelled — N/M issues updated`. Already-committed chunks stay committed.
- [PAI-318](https://pm.barta.cm/projects/6/issues/PAI-318) — **`+ Select all N matching` chip on IssueList for cross-page bulk selection.** Backed by a new `GET /api/issues?ids_only=1` shortcut that returns just the matching id set (capped at 5,000) so bulk modal can act beyond the visible page without paying for full hydration. The Bulk Change modal already chunks selections into 50-id batches with an `Applying X of Y issues…` progress bar.
- [PAI-319](https://pm.barta.cm/projects/6/issues/PAI-319) — **UI tests cover the Unassigned and No-parent (Orphan) bulk dropdown options end-to-end**, asserting the wire payload carries the explicit `null` that PAI-315's backend fix now honors.
- **`mutation_log.batch_id` is populated for bulk PATCH** so undo treats the bulk as one logical user action and stack-depth accounting groups them as one slot.

## [2.5.2] — 2026-05-04

### Fixed

- [PAI-313](https://pm.barta.cm/projects/6/issues/PAI-313) — **Issue-list freshness priming consumes its path-skip marker exactly once.** Returning to a previously primed issue-list URL after a different search path now primes correctly instead of accidentally suppressing the request.

## [2.5.1] — 2026-05-04

### Fixed

- [PAI-313](https://pm.barta.cm/projects/6/issues/PAI-313) — **Scoped issue search validation hardening.** Search palette requests now invalidate in-flight responses immediately when query or scope changes, project-detail loads are sequence-guarded against stale route responses, and issue-list freshness priming only suppresses the exact path it just primed.

## [2.5.0] — 2026-05-04

### Changed

- [PAI-313](https://pm.barta.cm/projects/6/issues/PAI-313) — **Issue search now has one consistent scoped model.** Project pages default the header search to the current project, global search remains an explicit toggle, palette results and “see all” navigation follow the selected scope, and issue-list counters distinguish server-loaded rows from progressively rendered rows so `100` no longer means three different things.

## [2.4.11] — 2026-05-04

### Added

- [PAI-312](https://pm.barta.cm/projects/6/issues/PAI-312) — **Issue list counters now include an inline `show all` action.** When a filtered flat table is capped by progressive rendering, the muted toolbar copy reads like `443 issues · showing 100 · show all`; clicking the link renders every filtered row immediately without adding another toolbar button. Tree view no longer shows the misleading progressive-render counter because it already renders the full tree.

## [2.4.10] — 2026-05-04

### Fixed

- [PAI-311](https://pm.barta.cm/projects/6/issues/PAI-311) — **Release artifacts are now assignable anywhere an issue exposes Release.** Project release lookups now include both `type='release'` issue artifacts and legacy text values already assigned on issues, so newly created release artifacts appear immediately in bulk change, table inline edit, create/edit modals, and side-panel controls. Release editing now uses bounded selects instead of free text, and bulk change distinguishes the placeholder from the explicit `None` value.
- [PAI-247](https://pm.barta.cm/projects/6/issues/PAI-247) — **Issue parent updates work through the public API and CLI.** `PUT /api/issues/{id}` accepts parent fields and the CLI can set or clear issue parents, matching the backend model and documented OpenAPI contract.

### Changed

- Security gate maintenance refreshed the gosec baseline after the parent-update API work so CI remains aligned with the audited findings.

## [2.4.9] — 2026-05-04

### Added

- [PAI-309](https://pm.barta.cm/projects/6/issues/PAI-309) — **Changed issue lists now auto-refresh with visible timing controls.** When a polling response detects that the current issue list changed, the header status pill is paired with muted countdown copy (`refreshing in 60s`) that ticks every 10 seconds. Hovering or focusing the copy reveals a settings icon and clicking it opens the account settings auto-refresh control, where users can disable the feature or set a minimum 10-second interval. Preferences persist per user and the default preserves the old 60-second behavior.
- [PAI-285](https://pm.barta.cm/projects/6/issues/PAI-285) — **CLI authentication no longer depends on plaintext config files.** The `paimos` CLI can resolve instance API keys from the OS keyring or from environment variables, which lets operators and agents run against `ppm` / `pmo` without storing bearer tokens in `~/.paimos/config.yaml`.

### Changed

- [PAI-310](https://pm.barta.cm/projects/6/issues/PAI-310) — **Issue assignee pickers now offer only active internal users.** Table inline assignment, create/edit forms, the side panel, bulk assignment, sprint-board assignment, and assignee filter options share one active-internal user predicate. Disabled, deleted, and external accounts are no longer offered for new assignment, while existing assignee display still resolves against the full user list.
- Issue search and routing internals were tightened: ranked issue search now has a safer SQL shape, deep links load by key more reliably, the result-count copy better reflects the active result set, and the search palette selection/refresh behavior was restored after the header refresh prompt move.

### Fixed

- `paimos issue update --assignee` now sends numeric `assignee_id` values instead of strings, matching the API behavior that raw `curl` already accepted.
- Main-branch deploys now require explicit untagged targets when `HEAD` is ahead of the latest release tag, so deploy scripts no longer guess between a release image and a green `sha-*` image.
- Backend and database hardening continued with atomic-by-default migrations, HTTP server timeouts, handler splitting, refreshed release-hygiene checks, and security gate maintenance.

## [2.4.8] — 2026-05-02

### Changed

- [PAI-284](https://pm.barta.cm/projects/6/issues/PAI-284) — **Search palette: first row auto-highlighted; Enter opens it; Cmd-Enter for full results.** Reported in QA: *"after a search and 'enter' (whatever this really does, could not figure it out)…"*. The palette previously used `activeIndex = -1` (no selection) until the user manually pressed ArrowDown — Enter then jumped to `/issues?q=…` (the full-results page) with the only hint buried at the bottom of the palette. Now matches the convention of every modern command palette: results watcher pins `activeIndex` to the first visible row (direct match if present, else `items[0]`), `↵` opens the highlighted row, `⌘↵` / `Ctrl↵` navigates to `/issues` for the full-results view. Footer hint shows the affordance with proper `<kbd>` glyphs and is itself clickable. ArrowUp now clamps at index 0 (no deselect-to-fallback path). Verified via Playwright on `/projects/4`: typing "log" auto-selects LOGS-1, plain `↵` opens it, `⌘↵` goes to `/issues`, `LOGS-3` exact-key search highlights the direct match.

## [2.4.7] — 2026-05-02

### Fixed

- [PAI-283](https://pm.barta.cm/projects/6/issues/PAI-283) phase 2 — **Search no longer 500s on FTS5 special characters.** The Phase 1 logging deployed in v2.4.6 caught the actual error: `fts5: syntax error near "/"` — typing `doc/` (or anything with FTS5 operator chars: `/`, `"`, `(`, `)`, `:`, `*`, `^`, `-`) crashed the parser inside `WHERE search_index MATCH ?`. Not a SQL-injection vector (params still flow through `?` placeholders / prepared statements), but a reliability + DoS bug — any user could 500 the issues endpoint with one keystroke. Added `sanitizeFTS5Token` in `backend/handlers/search_util.go` that strips non-alphanumeric characters, collapses whitespace, and appends `*` for prefix matching. When the cleaned input is empty (input was purely symbolic), callers drop the FTS5 branch and rely on the LIKE fallback alone. Applied at all 5 FTS5 query sites: `/api/projects/{id}/issues`, `/api/issues` (data + count), `/api/portal/timesheets`, and `/api/search` (the SearchPalette feeder). Bonus: the count query in `ListAllIssues` was previously FTS5-only — under-counted LIKE-only matches — now mirrors the data query's FTS+LIKE union so the rendered list and the "N matching" header agree.

## [2.4.6] — 2026-05-02

### Fixed

- [PAI-283](https://pm.barta.cm/projects/6/issues/PAI-283) — **`/api/issues` 500 "etag computation failed" now logs the underlying SQL error.** Found during QA on `pm.barta.cm`: the issues list endpoint returned a 500 with this generic message after a global search, with no log line to triage. Four call sites of `applyIssueListConditionalGET` (in `backend/handlers/issues.go`) mapped any `computeIssueListETag` error to the same client-facing string, swallowing the SQLite/FTS5 detail. Added `log.Printf("computeIssueListETag: %v (whereSQL=%q args=%d)", …)` in the helper itself so every call site benefits without behaviour change. Phase 2 root-cause fix follows once we capture the actual error from prod.

## [2.4.5] — 2026-05-02

### Fixed

- [PAI-282](https://pm.barta.cm/projects/6/issues/PAI-282) — **Search palette no longer clipped by AppHeader's `overflow: hidden`.** Found in QA: typing into the global header search showed the result palette starting under the header but only ~4 px of border peeked through before the rest was hidden by page content. The cause wasn't z-index (palette already had `z-index: 9999`) but `.app-header { overflow: hidden }` (load-bearing for the structural 52 px chrome) clipping any absolutely-positioned descendant. Same pattern as PAI-265: `SearchPalette` now `<Teleport to="body">` and positions `fixed` against the search-wrap's `getBoundingClientRect()`, recomputed on visible-change, resize, and capture-phase scroll. Click-through, keyboard nav (`ArrowUp/Down/Enter/Escape`), and the focused-search width transition all survive the teleport.

## [2.4.4] — 2026-05-02

### Fixed

- [PAI-281](https://pm.barta.cm/projects/6/issues/PAI-281) — **Workspace rail anchors to viewport bottom on self-scroll views.** `.main-content`'s symmetric `padding: 2rem 2.5rem` left ~32 px of dead space below bottom-anchored elements on `scrollMode: 'self'` views (after PAI-274) — the rail floated visibly above the viewport bottom instead of reading as anchored. Used `.main-content:has(.view-body--self-scroll) { padding-bottom: .5rem }` to scope the trim to the existing self-scroll opt-in: `/projects/:id`'s rail now sits ~14 px above the viewport edge (was ~38 px), `/issues`'s table-wrap gains +24 px of visible rows, while page-scroll views (Settings / IssueDetail / Customers / Dashboard) keep the generous 2 rem bottom for content-end breathing room.

## [2.4.3] — 2026-05-02

### Changed

- [PAI-279](https://pm.barta.cm/projects/6/issues/PAI-279) — **Project workspace rail aligned to global toggle vocabulary.** The Docs / Coop / Context buttons at the bottom of `/projects/:id` had their own custom pill style (`.pd-workspace-rail__btn`, `border-radius: 999px`, custom hover/active rules) that didn't match the rest of the app. Replaced with the global `btn btn-ghost btn-sm` + `:class="{ active: ... }"` pattern — same vocabulary as IssueList's `⌥ Tree`, the Filter / Views / Columns toggles, and every other secondary toggle in the app. Added a local `.btn-sm.active` rule mirroring `IssueList.vue`'s so the active workspace toggle paints with the same brand-blue-pale fill, preserving the visual pairing with the workspace dock above. Tightened the rail's outer padding so the strip lands at ~33 px (was ~42–48 px), giving the issue table back ~10–15 px of vertical real estate. ~25 lines of custom CSS deleted; future button-style refresh in `App.vue`'s `.btn` system propagates here automatically.
- [PAI-280](https://pm.barta.cm/projects/6/issues/PAI-280) — **AppFooter dropped; colophon consolidated into SidebarFooter.** `AppFooter` had been a duplicate strip ever since `SidebarFooter` matured (brand link + version + AGPL + GitHub/git-hash). It carried only `branding.company` (already in the sidebar's brand row) and a hardcoded paimos.com logo link, while costing ~30–48 px of vertical space on every authenticated route. Migrated the only thing it owned uniquely — the upstream paimos.com outbound link — to a meta-badge in `SidebarFooter.vue`'s `.sidebar-meta-row`, alongside AGPL-3.0-only and the git-hash badge. Removed `<AppFooter />` from `AppLayout.vue`, deleted `AppFooter.vue`, dropped `RouteMeta.hideAppFooter` from the router, and removed the matching opt-out on `AccrualsPrintView`. Trimmed the long PAI-262/PAI-274 CSS comment block in `AppLayout.vue` since the AppFooter-bleed concern is now moot. Net diff: −58 lines (one component file deleted), every authenticated route gains ~30–48 px of bottom space, sidebar is now the single source of truth for chrome.

## [2.4.2] — 2026-05-01

### Fixed

- [PAI-274](https://pm.barta.cm/projects/6/issues/PAI-274) phase 2 — **Sticky thead + frozen columns now also survive scroll on `/projects/:id`.** The v2.4.1 fix opted only `/issues` into `RouteMeta.scrollMode: 'self'`, but PAI-274's stated scope was every IssueList consumer (Issues view, Project Issues, …). Extending the meta to `/projects/:id` and giving `.pd-page` the same flex-bounded participation (`flex: 1; min-height: 0`) as `.issues-view-root` re-establishes the bounded scrolling viewport for IssueList's `.issue-table-wrap` inside ProjectDetailView. Verified at 1440×900 against `/projects/4` (LOGS): with table inflated to 20577px, `firstTh.top` stayed at 318px after scrolling the wrap by 800px (header pinned), AppFooter remained at viewport bottom, `.pd-workspaces` still rendered at the end of `.pd-page` (workspace-dock UX untouched). `IssueDetailView` (`/projects/:id/issues/:issueId`) intentionally stays page-scroll — it uses IssueList in `compact` mode (`overflow:hidden`, no internal scroll), so its sticky thead inherits `.main-content`'s scroll context. Router test extended to assert `scrollMode: 'self'` on every embed-IssueList route, parametrised so adding the next one is a one-line change.

## [2.4.1] — 2026-05-01

### Added

- [PAI-276](https://pm.barta.cm/projects/6/issues/PAI-276) — **`/api/health` exposes the running app version.** `Dockerfile` now copies `VERSION` into the go-build stage and stamps it into the binary via `-ldflags "-X main.appVersion=$(cat VERSION)"` (whitespace stripped); the existing reproducibility flags (`-trimpath -buildvcs=false -buildid=`) are preserved, so the same source tree + same `VERSION` still produces identical bytes. The handler returns `{status, service, version}` instead of `{status, service}` — local non-Docker builds report `"dev"` so checkouts can't masquerade as a release. OpenAPI schema documents the new field. Operator workflow: `curl https://host/api/health | jq .version` answers "what's actually running?" without SSH or `docker inspect`.

### Fixed

- [PAI-274](https://pm.barta.cm/projects/6/issues/PAI-274) — **Issue list sticky thead + frozen columns survive scroll again.** `IssueTable` already had the right CSS (`thead th { position: sticky; top: 0 }`, `.col-key { sticky; left: 0 }`, `.col-actions { sticky; right: 0 }`), but [PAI-262](https://pm.barta.cm/projects/6/issues/PAI-262) removed `flex: 1; min-height: 0` from `.view-body` to fix a different bleed issue, which starved `.issue-table-wrap`'s `overflow: auto` of a bounded scrolling viewport — sticky's nearest-scrolling-ancestor became the wrap itself, but the wrap had no internal scroll, so the entire table moved with the page-level `.main-content` scroll and the column headers scrolled out of view. Reconciled both regressions with a per-route opt-in: `RouteMeta.scrollMode: 'self'` on `/issues` adds `.view-body--self-scroll` (`flex: 1; min-height: 0; overflow: hidden`) to AppLayout's view-body wrapper. Default `'page'` mode is unchanged, so Settings / IssueDetail / CustomerDetail still page-scroll without bleed. Verified at 1440×900 against seeded fixtures: thead pinned at top of `.issue-table-wrap` after 1000px internal scroll; `/settings` (scrollHeight 2387 vs viewport 848) page-scrolls cleanly with no footer bleed; `/customers` short content keeps AppFooter pinned at bottom. New `frontend/src/router/router.test.ts` guards the `scrollMode` meta so this regression cannot silently return.

## [2.4.0] — 2026-04-29

### Added

- [PAI-272](https://pm.barta.cm/projects/6/issues/PAI-272) — **CustomerDetailView redesign.** Identity slab on the left of the hero (monogram tile with `--brand-blue` corner-mark accent + name + industry chip + provider link) and a stat / sync rail on the right (€/h + €/LP cards over a ghost Sync row + ⋯ overflow menu collapsing Edit / Delete). Asymmetric body grid splits primary content (Contacts → Projects → Documents) from a sticky About / Notes / Sync provenance side rail (8/4 ≥1024px, 7/5 768–1023px, single-column <768px). Empty states share a dashed-box vocabulary with centered Lucide icon + one-line copy + CTA link. The ⋯ menu teleports to `<body>` to escape the existing `overflow:hidden` clipping pattern (mirrors PAI-246/265). `dev_viewer` correctly hides Sync, Edit/Delete, the "New project" CTA, and the contact-row hover pencil.
- [PAI-273](https://pm.barta.cm/projects/6/issues/PAI-273) — **Contact (Ansprechpartner) entity + customer metadata expansion.** New `contacts` table holds many contacts per customer (one `is_primary` at a time, enforced in transactional code); CRUD endpoints — `GET|POST /customers/:id/contacts`, `GET|PUT|DELETE /contacts/:id`, atomic `POST /contacts/:id/promote-primary`. `customers` extends with `website` / `domain` / `vat_id` / `employee_count` / `annual_revenue_cents` / `description` / `phone` / billing_address quartet / visit_address pair — all nullable, no destructive migration. M87 backfills a primary contact for every customer with non-empty legacy `contact_name` / `contact_email`. Read-compat: `GET /customers/:id` continues to expose `contact_name` / `contact_email`, populated from the primary contact when present. Write-compat: `PUT /customers/:id` carrying legacy fields routes the writes back into the primary contact (creating one when missing) — v1 callers keep working unmodified for one release.
- [PAI-273](https://pm.barta.cm/projects/6/issues/PAI-273) — **HubSpot extended sync.** Companies fetch now requests the full property set (`domain, website, numberofemployees, annualrevenue, description, phone, address, address2, city, state, zip, country`) and maps onto the new customer columns during Import + Sync. Associated contacts are pulled via `/crm/v3/objects/companies/{id}/associations/contacts` followed by `/crm/v3/objects/contacts/batch/read` (paginated in 100-id chunks) and upserted into the `contacts` table keyed on `(external_provider, external_id)` — re-syncs are idempotent. The contacts call soft-fails: a slow or rate-limited contacts endpoint never fails the whole company fetch. The first associated contact takes the primary slot only when the customer has no primary on the PAIMOS side (admins set primaries; we only fill the slot when empty).
- **CustomerDetailView wires the slots PAI-272 left open.** The Contacts card renders a real multi-contact list with the primary-contact pill badge, Ansprechpartner-Funktion (role) label per row, hover-revealed promote-primary (★) / edit / delete actions, and an Add-contact modal. The About card surfaces the new metadata as icon-led data-driven rows (`link` website, `phone`, `hash` VAT, `users` employees, `trending-up` revenue) — each row hides when empty, so sparsely-populated customers stay clean and additive fields don't re-flow the layout. Description renders as a paragraph below the row list with a separator.

### Changed

- DocumentsSection's empty state restyled to the dashed-box pattern (centered icon, one-line copy, "click to upload" CTA when writable) so all three customer-detail empty states share a vocabulary.

## [2.3.0] — 2026-04-28

### Added

- [PAI-261](https://pm.barta.cm/projects/6/issues/PAI-261) — **Encryption-at-rest for user-entered secrets.** New `backend/secretvault` package: AES-256-GCM with per-domain HKDF-SHA256 subkeys, versioned envelope (v1 = `0x01 || nonce(12) || cipher || tag`), v0-fallback read path so existing CRM provider ciphertexts keep decrypting untouched. CRM provider configs (`crm:provider_configs` domain) and the OpenRouter `api_key` (`ai:openrouter` domain, M86 adds `ai_settings.api_key_encrypted` BLOB) both consume the package. AI api_key migration is **lazy** — pre-existing plaintext rows decrypt via fallback until the next admin save, which encrypts under the new domain subkey and clears the plaintext column. Cross-domain ciphertext replay (e.g. a leaked CRM blob played against the AI store) fails AEAD verification at decrypt time. Master-key sourcing is unchanged (`PAIMOS_SECRET_KEY` env > `$DATA_DIR/.secret-key` disk file); operators move from Tier 1 (default, key on volume) to Tier 2 (key in secret manager, env-only) by reusing the same key bytes — no rotation needed for the move.
- [PAI-261](https://pm.barta.cm/projects/6/issues/PAI-261) — **`paimos secrets rotate --new-key <b64> [--dry-run]` operator subcommand.** Decrypts every secret-bearing row under the current `PAIMOS_SECRET_KEY` and re-encrypts under the new key in a single SQLite transaction across both consumer tables. Partial failure (any row that fails to decrypt or re-encrypt) rolls back cleanly — the service keeps working on the OLD key, no recovery needed. `--dry-run` decrypts every row to confirm rotation can proceed, reports counts, writes nothing. Operator workflow on success is `service stop → rotate → update env → service start` and the CLI prints the recipe. Today, swapping `PAIMOS_SECRET_KEY` without first running rotate corrupts every existing ciphertext — that gap is now closed.

### Changed

- [`HARDENING.md` § 3.6](HARDENING.md#36--secrets-management) rewritten with explicit **Tier 1 / Tier 2** framing: T1 = master key auto-generated to `$DATA_DIR/.secret-key` (default, suitable for dev / single-node, does NOT defend against stolen backup tarballs); T2 = master key from secret manager via `PAIMOS_SECRET_KEY` env (recommended for production, backup tarballs are useless without the env-supplied key). Plus a T1→T2 migration recipe that re-uses the same key bytes — no rotation needed, just a location move.
- [`THREAT_MODEL.md` § 5](THREAT_MODEL.md) — the "physical attacker with disk access" clause now acknowledges the field-level encryption that PAI-261 introduces, with explicit "T2 protects against backup theft, T1 doesn't" framing so an operator reading the model gets the honest picture rather than the obsolete "PAIMOS doesn't encrypt at rest" wording.

## [2.2.1] — 2026-04-28

### Added

- [PAI-267](https://pm.barta.cm/projects/6/issues/PAI-267) — **Build-tag-gated dev login and fixture seeding for local agent/UI work.** Development builds can expose the dev-login route and seed minimal fixtures without shipping those symbols in production binaries. The implementation keeps the route behind the `dev_login` build tag and pairs it with seed data so agents can get an authenticated local session without hand-building database state.
- [PAI-269](https://pm.barta.cm/projects/6/issues/PAI-269) — **Richer devseed fixtures for ACME, BUGZ, and LOGS.** Local development data now better exercises real issue lists, customer/project surfaces, and search-heavy workflows.
- [PAI-271](https://pm.barta.cm/projects/6/issues/PAI-271) — **DEV_LOGIN.md documents the dev-login security model.** The guide covers token requirements, the build-tag boundary, fixture users, and the intended local-only operator/agent workflow.

### Changed

- [PAI-267](https://pm.barta.cm/projects/6/issues/PAI-267) — **Development shells self-bootstrap Go through direnv when needed.** This makes local commands more reliable on machines where the interactive shell has not already loaded the expected toolchain.

### Fixed

- [PAI-262](https://pm.barta.cm/projects/6/issues/PAI-262) — **Tall views no longer bleed into AppFooter.** Dropping the problematic `view-body` flex behavior restored clean page layout for long content.
- Deploy backups now use containerized `tar` for bind-mounted storage, avoiding host/container path and permission mismatches during backup creation.

## [2.2.0] — 2026-04-28

### Added

- [PAI-266](https://pm.barta.cm/projects/6/issues/PAI-266) — Customer search with CRM fan-out. Typing into the existing customer search field now triggers a 300ms-debounced query against the local DB; when local matches are zero and the field has 2+ characters, the search fans out in parallel to every enabled + configured CRM provider that implements the new optional `crm.Searcher` interface. Remote results render as a dropdown beneath the search input, grouped per provider with its logo, with per-group loading + error states inline (one broken integration cannot kill the dropdown). Clicking "Import" opens an inline confirm step and reuses the existing `/customers/import` endpoint. Hits already imported locally — joined on `(provider_id, external_id)` — render muted with an "Open in PAIMOS" link to the existing customer detail page. The legacy URL/ID paste flow is demoted to an "Advanced" affordance in the empty state. **HubSpot** is the first reference implementation, against `POST /crm/v3/objects/companies/search` — same `crm.objects.companies.read` scope as `ImportRef`, no new permissions for operators to grant. New endpoint: `GET /api/integrations/crm/search?q=&limit=` (admin-only). Backend tests cover happy path, 401/403/500/network failure modes, parallel fan-out, per-provider error isolation, and `already_imported` dedup.

### Changed

- HubSpot integration help-text is now blunter about which token format works in practice: only Private App tokens (`pat-na1-…`) have authenticated reliably; Personal Access Keys and Service Account keys are accepted by the form but have failed against HubSpot in our testing. Required scope is documented as the single `crm.objects.companies.read` checkbox — PAIMOS does not write to HubSpot or read contacts, deals, schemas, or sensitive-classified fields.

## [2.1.25] — 2026-04-28

### Changed

- [PAI-263](https://pm.barta.cm/projects/6/issues/PAI-263) — `AppFooter` hoisted into `AppLayout` as a single source of truth. The 15 top-level views that each imported and rendered `<AppFooter />` (`SettingsView`, `ProjectsView`, `CustomersView`, `CustomerDetailView`, `DashboardView`, `IssuesView`, `IssueDetailView`, `SprintsView`, `SprintBoardView`, `ReportingView`, `IntegrationsView`, `ImportView`, `LieferberichtView`, `UsersView`, `DevelopmentView`) no longer do. AppLayout now wraps `<slot />` in a `flex:1; flex-direction:column; min-height:0` `view-body` div and renders `<AppFooter />` after it, so every authenticated view gets the footer for free at the bottom of `.main-content`. Side benefit: `ProjectDetailView` and the loaded state of `IssueDetailView` — the only authenticated views that previously rendered no footer at all — now get one. `AccrualsPrintView` opts out via `route.meta.hideAppFooter` since it ships its own colophon.

### Fixed

- [PAI-264](https://pm.barta.cm/projects/6/issues/PAI-264) — `scripts/release.sh` no longer deadlocks when called from a non-TTY shell. Three new opt-outs short-circuit the CHANGELOG-editor step and commit the auto-generated draft as-is: `--no-edit` flag, `RELEASE_NO_EDIT=1` env var, and dummy `$EDITOR` detection (empty / `true` / `:` / `cat` / `tee`). Plus an idempotent-recovery branch: if a prior run already bumped `VERSION` and prepended the `CHANGELOG.md` entry but bailed before committing (the failure mode hit cutting v2.1.23 and v2.1.24 by hand), a re-run with the same mode now picks up where it left off instead of failing the `working tree clean` gate. Bounded strictly — only `VERSION` + `docs/CHANGELOG.md` may differ, and they must already match the targeted bump. Interactive behaviour is unchanged: with a real `$EDITOR`, the editor still opens for review.

## [2.1.24] — 2026-04-28

### Fixed

- [PAI-265](https://pm.barta.cm/projects/6/issues/PAI-265) — Project Detail: the **⋯ More project actions** dropdown (Export CSV / Import CSV / Edit project) appeared to do nothing on click. State was actually toggling correctly — the panel was just rendered-and-clipped. Both trigger and panel lived inside `<Teleport to="#app-header-right">`, and `.ah-right-slot` carries `overflow:hidden` (load-bearing for long customer-pill / tag-chip ellipsis behaviour, can't be removed). The absolutely-positioned `.pd-overflow-menu` was therefore clipped to the slot's box and invisible. Fix: split the teleport — trigger stays in `#app-header-right`, panel teleports separately to `<body>` with `position:fixed`, anchored to the trigger's `getBoundingClientRect()` on open and recomputed on `window` `resize` + `scroll` (capture phase, so a `.main-content` scroll also re-anchors). Outside-click handler now checks both `triggerRef` and `panelRef` since the panel is no longer a descendant of the wrapper.

## [2.1.23] — 2026-04-28

### Fixed

- [PAI-262](https://pm.barta.cm/projects/6/issues/PAI-262) — `AppFooter` no longer floats into mid-page when the active view is shorter than the viewport. `AppLayout.vue`'s `.main-content` is a `flex-direction: column` scroller, but `AppFooter.vue` was a plain block with `margin-top: 2rem`, so on short views (Integrations Jira tab pre-preview, Reporting, Users, Settings, etc.) the footer rendered flush against the last content block — visually bleeding into the page mid-screen instead of pinning to the bottom. Swapping the static `2rem` for `margin-top: auto` lets the flex item consume leftover column space and pin to the bottom; long-content views are unaffected (auto collapses when no slack remains, and the `1.25rem` `padding-top` still provides separation from the content above the `border-top`). One-line CSS change in `AppFooter.vue:30`. Follow-up [PAI-263](https://pm.barta.cm/projects/6/issues/PAI-263) tracks hoisting `<AppFooter />` out of the 14 view templates and into `AppLayout.vue` as a single source of truth.

## [2.1.22] — 2026-04-28

### Added

- [PAI-260](https://pm.barta.cm/projects/6/issues/PAI-260) — `paimos issue tag add` / `paimos issue tag rm` subcommands. Closes a long-standing gap where a client repo's operator runbook documented these as the canonical lane-management recipe but the CLI never shipped them, forcing agents to fall back to a racy read-modify-write of `tag_ids` over raw `PUT /api/issues/{id}`. The new verbs sit parallel to `issue relation add/rm`, accept either `--tag <key>` (resolved against `/api/tags`) or `--tag-id <int>` (mutually exclusive), and are idempotent server-side via `INSERT OR IGNORE` / no-op `DELETE`. `--json` mode mirrors `issue update`. `<ref>` accepts both an issue key and a numeric DB id. Unknown tag-key / tag-id surfaces a 404 here rather than a silent no-op against the upstream endpoint. That runbook needed no edit — its existing recipe (`paimos issue tag-add 2445 --tag dev`) keeps working with the new spelling once `tag-add` is rewritten as `tag add` in client repos that adopt the new verb.

## [2.1.21] — 2026-04-28

### Fixed

- [PAI-259](https://pm.barta.cm/projects/6/issues/PAI-259) — CRM Settings: first-time save now correctly persists the secret. `SettingsCRMTab.vue:120` previously gated all secret writes on `replacing[p.id]?.[f.key]`, which is only set when the admin clicks **Replace** on an already-set secret. On first-time setup `f.has_value` was false, the password input rendered, the admin typed the token, but the value never landed in the patch — the backend then rejected the save with "access token must not be empty" against a clearly non-empty input. The save logic now also sends the secret on first-time setup (`!f.has_value && draftValue !== ''`).

### Added

- [PAI-259](https://pm.barta.cm/projects/6/issues/PAI-259) — CRM Settings: **eye / eye-off toggle** on every secret input so admins can verify what they pasted before saving. Toggle re-masks automatically on Save, Cancel-replace, or when the panel is closed.
- [PAI-259](https://pm.barta.cm/projects/6/issues/PAI-259) — CRM Settings: **Test integration** button + inline log panel. New optional `crm.ConnectionTester` interface lets each provider opt in to a structured smoke test; HubSpot's implementation hits `/crm/v3/objects/companies?limit=1` (same scope as the real import flow, so OK ⇒ genuinely usable). New endpoint `POST /api/integrations/crm/{id}/test` (admin-only) returns `{ok, message, lines}`. The frontend keeps the last 20 attempts per provider in a scrollable card; pass / fail is colour-coded; the test never accepts or echoes the secret on the wire — it round-trips through the same persisted config that powers real imports.

## [2.1.20] — 2026-04-28

### Fixed

- [PAI-258](https://pm.barta.cm/projects/6/issues/PAI-258) — HubSpot CRM integration accepts the new **Personal Access Key** format. The `ValidateConfig` check in `backend/handlers/crm/hubspot/provider.go` previously gated on `strings.HasPrefix(token, "pat-")`, which rejected HubSpot's newer opaque-base64 keys (e.g. `CiRldTEtN…`) even though they authenticate the exact same Bearer-token endpoints. Replaced the prefix gate with a permissive sanity check (non-empty, no whitespace, ≥20 chars, surfaces the "you pasted `Bearer …` by mistake" case explicitly), and updated the Settings UI hint + placeholder to advertise both flavours. Added `provider_test.go` to pin the invariant — the next time HubSpot changes the format, this test fails with a clear signal rather than silently breaking integrations.

## [2.1.19] — 2026-04-28

### Fixed

- [PAI-255](https://pm.barta.cm/projects/6/issues/PAI-255) — Hovering a row in the global-search dropdown no longer scrolls the page and hides `AppHeader`. Root cause: the `activeIndex` watcher in `SearchPalette.vue` called `el.scrollIntoView({ block: 'nearest' })`, which walks the DOM looking for any scrollable ancestor; when the active row was already mostly visible the browser scrolled the page (not the palette), pushing the docked header off-screen. Replaced with a manual `palette.scrollTop` adjustment scoped strictly to the palette's own scroll container, so hover/keyboard navigation can never affect the page scroll.
- [PAI-256](https://pm.barta.cm/projects/6/issues/PAI-256) — `AppHeader` "updated N ago" prefix no longer butts against the issue-key chip on the right. Vue's default template-whitespace rule strips the source-level space between `<span class="ah-meta-prefix">` and `<RouterLink class="ah-meta-link">` when they're on different lines, so a `margin-right` on `.ah-meta-prefix` (0.35rem) restores the visual gap without breaking the Tier 2 `display: none` rule that hides the prefix entirely on narrow `.main` widths.

## [2.1.18] — 2026-04-28

### Added

- [PAI-254](https://pm.barta.cm/projects/6/issues/PAI-254) — Backend handlers for the `structure_*` AI actions exposed by the Project Manifest tab editor (frontend shipped in 2.1.16, backend was the deferred follow-up). Five action keys are now registered with real handlers + admin-overridable system prompts in Settings → AI prompts: `structure_manifest`, `structure_guardrails`, `structure_glossary`, plus two new ones for the new tabs — `structure_dev` and `structure_ops`. Each takes free-form prose and returns a JSON candidate scoped to the right manifest slice; the diff overlay UX is unchanged. Field allow-list extended with `manifest_json` / `guardrails_json` / `glossary_json` / `dev_json` / `ops_json`. `ProjectManifestTabs.vue` gains the **Dev** (`_dev`, terminal icon) and **Ops** (`_ops`, server icon) tabs after Glossary, mirroring the Guardrails/Glossary save + AI-structure flow exactly. The new slices are positioned as future LLM-command sources.

### Changed

- Project-management workflow: paimos product tickets now live in PAIMOS itself (https://pm.barta.cm, project `PAI`, id 6) — the local `+pm/backlog` and `+pm/done` markdown directories are deprecated and the five entries that lived under `+pm/done/` were migrated to PAI-249 through PAI-253. The `+pm/` directory is retained for long-form product framing only (PRD, FIELD_MATRIX). Documented in `+pm/README.md`, `docs/DEVELOPER_GUIDE.md`, and the repo `README.md`.

## [2.1.17] — 2026-04-27

### Changed

- `+pm/P35.62b5b4e` — Settings → Users action column collapses the legacy "Projects" + "Access" buttons into a single matrix dialog. Every grant now flows through `/api/users/{id}/memberships` (none / viewer / editor + reset-to-default per project) and produces one audit-trail entry — the legacy `/users/{id}/projects` UI flow is gone. The dialog adapts to role: a segmented "Explicit grants" / "All projects" filter (default ON for externals, OFF for staff) keeps the ~30-row "None" noise out of the way for sparse externals, an "Add project" composed bar at the top makes assignment discoverable, a hairline "Defaults — {role-default}" divider separates explicit overrides from defaulted rows, and the Editor pill is hidden everywhere (per-row + Add bar) for external users since they can only be Viewer. Backend `/users/{id}/projects` endpoints stay alive for now (still exercised by `backend/handlers/portal_test.go`); endpoint deprecation is a follow-up.

## [2.1.16] — 2026-04-27

### Added

- `+pm/P40.854a7db` — Manifest editor in `ProjectContextSection` is now a three-tab editor (Manifest / Guardrails / Glossary) with a per-tab "Structure with AI" button. Guardrails persist under `manifest.data._guardrails`, Glossary under `manifest.data._glossary` (reserved keys); the Manifest tab edits everything else. Save merges all three slices through the existing `/projects/{id}/manifest` endpoint — no backend changes for persistence. The AI button routes through `useAiOptimize.runRewriteAction()` + `AiSurfaceFeedback` so accept/reject UX matches every other AI rewrite. Per-tab drafts survive tab switching; tab dots indicate which slices have content. The PAI-178 sentinel contract on `ProjectContextSection` is preserved — `hasManifest` now means "the manifest area is populated" (any of the three slices). Backend follow-up (separate ticket): three new action handlers (`structure_manifest`, `structure_guardrails`, `structure_glossary`) under `backend/ai/`, registered in the dispatcher + `/api/ai/actions` catalog. Until those land, the AI buttons render but error out at request time with the standard red-pill UX.

## [2.1.15] — 2026-04-27

### Changed

- `+pm/P30.1567bd8` — AppHeader now degrades gracefully when its container shrinks (pinned side panel, narrow viewport, etc.). Header height is locked at 52px and the right cluster never wraps to a second row. `.main` is a container-query root (`container-type: inline-size`); `.app-header` collapses through four `@container` tiers at 1100 / 920 / 760 / 600px of `.main` width: T1 drops the project subtitle and tag chips and shrinks the search; T2 ellipsis-truncates the title at 14ch, strips the "updated Xh ago" prefix from the meta (keeping the issue-key link), and reduces the customer pill to icon-only; T3 collapses the search to a 36px icon-button (it expands inline on focus) and drops the "Undo" label; T4 hides the title entirely. A soft right-edge fade mask on `.ah-left` softens breadcrumb truncation. Mobile viewport (< 900px) is unchanged — the multi-row stack is preserved with `height: auto`. `ProjectDetailView` wraps the timestamp prefix in a new `.ah-meta-prefix` span so T2 can hide it without nuking the issue-key link.

## [2.1.14] — 2026-04-27

### Fixed

- `+pm/P30.c6aad81` — AppHeader now shrinks alongside the main content when the issue side panel is **pinned**. The panel is `position: fixed`, so it never pushes layout; the previous shrink-when-pinned logic only ran on the inner `.issue-list-root` and the top `AppHeader` stayed full-width and got covered by the panel. A new `useSidePanelPinned` singleton lifts the pinned + visible state out of `IssueList`, and `AppLayout` consumes it (together with `useSidePanelWidth`) to apply `padding-right` on `.main`. Both the header and the main content now reflow as one column.

## [2.1.13] — 2026-04-27

### Changed

- PAI-246 — App-header right cluster polish: Undo button now inherits `.btn-sm` like the per-view Edit button next to it, so the two read at the same size and weight (the global `.btn` padding was previously winning over the missing `.btn-sm` rule in `AppHeader.vue`).
- PAI-246 — Project Detail header collapses the Export CSV / Import CSV / Edit project trio behind a single `⋯` menu. The menu items show icon + label, respect the existing admin/edit permission gates, and close on outside click, Esc, and selection.
- PAI-246 — Within that menu, Export CSV uses an up/out arrow and Import CSV uses a down/in arrow, following the data-flow convention (data *leaves* on export, *enters* on import); the labels in the menu disambiguate any icon-only ambiguity that convention introduces vs. the more common user-device convention.

## [2.1.12] — 2026-04-27

### Changed

- PAI-245 — App-header refinements: the global Undo control moved from the center cluster to the far right of the header, immediately next to the per-view Edit button, and is now styled as a ghost button matching its neighbours instead of a rounded pill. Right-slot status badges (`active` / `archived`) and the `updated …` meta line now read with the same muted weight as the ghost buttons next to them, so the right cluster no longer outweighs Edit visually.

### Fixed

- PAI-245 — Search-input placeholder no longer overlaps the magnifying-glass icon. The `:not()` chain on the global form-control rule (added in 2.1.8) was bumping its specificity to (0,5,1) and overriding component-scoped padding; the negations are now wrapped in `:where()` so the rule keeps single-element specificity.

## [2.1.11] — 2026-04-27

### Fixed

- PAI-220 — `Settings → AI → Usage today` table headers (Prompt + completion / Calls / Cap) now share right-alignment with their numeric values. The previous `.ai-usage-table thead th` rule's specificity was beating `.ai-usage-num`, leaving headers left-aligned while values rendered right-aligned. Numeric headers also use `tabular-nums` so digits line up cleanly.

## [2.1.10] — 2026-04-27

### Fixed

- PAI-242 — Sidebar search field is now cleared on logout instead of bleeding the previous user's last query into the next session via the persisted `paimos:search:lastQuery` localStorage key.
- PAI-243 — `MetaSelect` (status / priority / type / assignee dropdowns) now flips above the trigger when there isn't enough room below the viewport, and the option list's `max-height` is clamped to the available space so options never disappear off the bottom of the screen.
- PAI-244 — Timer `start()` now refreshes running state from the server before checking, so the "other timers running — switch / both / cancel" prompt is no longer raised against stale local cache when another tab/session has already stopped the timer. Same-browser tabs additionally sync via a `BroadcastChannel('paimos:timer')` so stopping/starting in one tab updates peer tabs immediately.

## [2.1.9] — 2026-04-27

### Fixed

- AI result modals (`AC checklist`, AI suggestions, sub-tasks, UI spec) now layer above the issue side panel and project workspace dock instead of being clipped by them. The `AppModal` overlay z-index moved from 100 to 1000, putting it above every right-edge sidebar while staying below confirm dialogs.

## [2.1.8] — 2026-04-27

### Fixed

- AI suggestions modal (`suggest_enhancement`, `generate_subtasks`) no longer collapses each suggestion into a one-word-per-line column on the right edge; the row checkbox stays compact and the suggestion title, impact pill, target pill and body render on a normal flex row.
- Global `input,select,textarea` form-control style is now scoped to text-input types, so checkboxes, radios, file/range/color inputs are no longer forced to `width: 100%` with text-input padding/border.

## [2.1.4] — 2026-04-26

### Fixed

- Issue-list freshness banners now sit with proper top spacing instead of crowding the toolbar cluster.
- AI result details now render in a wide, readable modal layout instead of collapsing into a narrow strip.
- Header/title geometry now preserves left-side project context cleanly beside the pinned sidebar.
- Footer project workspace status now resets and rehydrates correctly when switching between projects.

## [2.1.5] — 2026-04-26

### Fixed

- Saved views now restore flat/tree mode reliably, including older and fallback views that previously lacked an explicit `treeView` value.

## [2.1.7] — 2026-04-26

### Changed

- Undo activity now opens as a right-edge sidebar (shared width with the issue panel and project workspace dock) instead of a popup card; only one sidebar is visible at a time.
- App header keeps a single explicit Undo button — the duplicate rewind icon inside the search field is gone.
- AppFooter logo is right-aligned and links to paimos.com; on the project detail view the footer no longer doubles up with the workspace rail.

## [2.1.6] — 2026-04-26

### Changed

- Project detail now anchors the Docs / Coop / Context rail at the true bottom edge, with a compact brand footer above it so the issue list gets more vertical room.

## [2.1.3] — 2026-04-26

### Changed

- Project Detail now uses a footer-level workspace rail for Context, Docs, and Coop instead of toolbar/sidebar project workspace controls.
- Project workspace panels now open as one docked footer-adjacent workbench with shared status presentation.

## [2.1.2] — 2026-04-26

### Added

- Project Detail now exposes Project Context as a bottom-docked workbench with a compact toolbar status chip instead of a floating right-side panel.
- Issue and global issue lists now support opt-in freshness banners backed by weak ETags and conditional polling.
- A reusable locale-aware number-formatting helper now drives the AI usage panel and other follow-on surfaces.

### Changed

- The global undo affordance is now visible in the app header instead of being hidden inside the search input.
- The AI provider configuration surface moved from `Settings -> AI` to `Integrations -> AI`, while `AI prompts` stays in Settings.
- The app header and shell now wrap more gracefully on narrow widths and align correctly against the pinned sidebar.

## [2.1.1] — 2026-04-26

### Fixed

- Issue side-panel AI result and decision UI now appears directly under the header action cluster instead of lower in the form area.
- AI surface inventory regression test updated to the corrected side-panel host mount.

## [2.1.0] — 2026-04-26

### Added

- Durable undo and redo foundation with `mutation_log`, conflict resolution, per-issue activity, and admin stack-depth controls.
- AI UX completion across shared result strips, surface feedback, paper trail views, and durable AI-applied undo.
- Undo documentation and configuration references, including runtime system settings and retention guidance.

### Changed

- Issue and side-panel AI flows now participate in the durable request-correlated undo path.
- Recent activity surfaces now unify AI and non-AI issue mutations on top of `mutation_log`.

## [2.0.4] — 2026-04-26

### Added — Project context substrate and hybrid retrieval

- Merge branch 'feat/pai-29-pai-30-substrate'
- docs(security): operator hardening guide + reference architecture (PAI-133)
- Complete hybrid retrieval and symbol graph flow
- docs(security): threat model + named security invariants per domain (PAI-125)
- Add deterministic embedding retrieval substrate
- Add lexical project-context retrieval index
- Build project-context tooling substrate
- docs(security): solo-maintainer continuity plan + tabletop (PAI-144)
- docs(security): incident response runbook + tabletop exercise (PAI-131)
- fix(settings): move CRM tab from Settings to Integrations (PAI-179)
- docs(2.0): audit + decision report and planning-hierarchy review (PAI-189 close-out)

## [2.0.3] — 2026-04-26

### Fixed

- **AI action dispatcher route was not mounted after the catalog
  refactor in v2.0.2.** The action menu loaded but `POST
  /api/ai/action` returned 404, breaking every AI surface end-to-
  end. Re-mounted the route on the chi router; verified against
  the catalog flow + catalog test (PAI-189).

## [2.0.2] — 2026-04-26

### Changed — Wave 2/3/4 service-extraction refactor (PAI-189)

The 2.0 architectural consolidation continued through three more
merge waves on top of v2.0.1, all behavior-preserving. No REST
contract changes.

- **Backend** — issue-detail sidecar concerns (anchors, attachments,
  comments, history, relations, time entries, group members, epic
  completion) lifted from the issue handler into per-domain seams,
  reducing the issue-handler's coupling to a single responsibility
  per call site.
- **Project context + AI catalog seams** — `project_context_service.go`
  and `ai_action_catalog_service.go` extracted; handlers delegate
  rather than orchestrate.
- **Detail-view mutations** — view-level mutation orchestration
  moved out of the chunky `IssueDetailView` Vue file into typed
  service modules under `frontend/src/services/issue*.ts`, each
  with its own Vitest suite.
- **AI response parsing** — hardened against malformed provider
  responses; the parser now refuses unknown shapes loudly rather
  than silently degrading.
- **Schema audit utility** — `backend/db/schema_audit.go` ships
  small introspection helpers used by the new schema regression
  tests; catches accidental schema drift between development and
  production migrations.

Audit & decision record: see [`docs/2.0_AUDIT.md`](2.0_AUDIT.md).

## [2.0.1] — 2026-04-26

### Changed

- Hardened test reporting so deployments now expose honest missing, partial, and ready states and support admin-side report bundle ingestion.
- Added security regression coverage, authorization fuzz checks, and stronger CI scanning for backend invariants and release trust surfaces.
- Mounted and validated the AI action catalog flow so empty menu states now reflect real assignment state instead of missing API wiring.
- Extracted backend service seams for project context, AI action catalog, and development reporting to reduce handler coupling without changing behavior.
- Refactored frontend settings, project detail, and issue detail orchestration into typed registries and helper modules for clearer state ownership.
- Added schema regression coverage and deterministic release plumbing to improve upgrade safety and release provenance.

## [2.0.0] — 2026-04-26

The Project Context layer for code-aware agents — built up incrementally
across the v1 series under PAI-29 (context-in) and PAI-30 (context-out)
— promotes from internal/experimental to a v1-stable agent contract.
The schema (`project_repos`, `project_manifests`, `issue_anchors`,
`entity_relations`) has been live for a while; this release finalizes
the handlers, completes the typed entity-graph traversal, and publicly
documents the surface in `AGENT_INTEGRATION.md` and `api-minimal.md`.
That public agent contract is what the major bump marks.

### Added — Project Context for code-aware agents (PAI-29 / PAI-30)

Coding agents (Claude Code, custom build/triage bots, anything that
operates on the repo as well as the issue tracker) now have a
first-class read-and-write surface for **structured project facts**
that go beyond markdown. The intent: an agent shouldn't have to grep
six issues to figure out which repo to clone, which command to run,
or where in the source tree the issue it's working on actually lives.

**Six new endpoints**, all per-project / per-issue with the usual
view-or-edit gating:

- `GET /api/projects/{id}/repos` · `POST` · `PUT /{repoId}` · `DELETE`
  — declare the linked repositories (URL, default branch, label,
  sort order). The list is what `paimos-mcp` and ad-hoc agent skills
  look at to decide where to clone.
- `GET /api/projects/{id}/manifest` · `PUT` — structured project
  truth: stack, commands, services, owners, NFRs, ADR refs.
  Recommended v1 keys: `repos`, `commands`, `stack`, `services`,
  `owners`, `nfrs`, `adrs`. Free-form JSON beyond that — admins can
  shape it for their own agents without a schema migration.
- `POST /api/projects/{id}/anchors` — bulk-ingest issue→file/line
  locations from a repo-side scanner. Each anchor carries
  `schema_version`, `repo_revision`, and `generated_at` so a deep
  link in a ticket can be trusted to either resolve at the recorded
  revision or fail loudly.
- `GET /api/issues/{id}/anchors` — read-side: every recorded
  file/line for one issue, across every linked repo, with the
  per-anchor revision/schema metadata.
- `GET /api/projects/{id}/graph?root=issue:42&depth=2` — typed
  entity-graph traversal. Returns the relations rooted at the given
  node (issue, repo, anchor, project) up to `depth` hops. Backed by
  `entity_relations` rows that are populated incrementally as
  issues, anchors, and repos move through the system.
- `POST /api/projects/{id}/retrieve` `{q, k}` — mixed-context
  retrieval. Combines issue full-text hits, manifest matches,
  anchor matches, and one hop of graph-neighbor expansion into a
  single ranked result list. Designed to be the one call an agent
  makes when it needs context for a question.

This release adds the typed graph traversal (`fetchEntityGraph`,
`expandContextNeighbors`) and the relation-maintenance helpers
(`upsertIssueEntityRelation`, `deleteAnchorEntityRelationsByRepo`,
etc.) that keep the `entity_relations` table consistent as anchors
and repos churn.

No schema migration in this release — `project_repos`,
`project_manifests`, `issue_anchors`, and `entity_relations` were
added in earlier v1 milestones; v2.0.0 is the contract-promotion
release for the surface that sits on top of them.

### Added — `just doc-sync` (release follow-up workflow)

A new `scripts/release-doc-sync.sh` files a single PAIMOS ticket per
release with a four-surface checklist — README, `docs/`, the
`../paimos-site` repo, brand/screenshots — plus a diff summary since
the previous tag and a snapshot of `paimos-site`'s git state. Run as
`just doc-sync` after `just release`; the release script now prints
the reminder as part of its closing "Next:" output so the step is
hard to miss. `docs/DEPLOY.md` is updated to "the four commands"
with the standard **release → deploy → doc-sync** sequence
documented.

The intent is to close the long-running drift gap between code and
user-facing surfaces: ship the code, deploy it, and within the same
session decide (and record on a ticket) which of README / internal
docs / public site / screenshots actually need a refresh.

### Compatibility

No breaking REST changes. Every v1.x endpoint continues to work
unchanged. The major bump marks the **public agent contract** for
the project-context surface — PAIMOS commits to keeping these
endpoint shapes stable for the v2 series. Agents that integrated
against the experimental shape during v1 should re-verify field
names against `docs/api-minimal.md`; the documented v2 contract is
the canonical reference going forward.

### Brand — Phase 1 → Phase 2 transition

v2.0.0 also marks the brand's Phase 1 → Phase 2 transition (see
[`docs/brand/BRAND.md`](brand/BRAND.md#phasing-plan)). The brand
guide reserved the **Platform reading** of the acronym (the
"OS" in PAIMOS resolving to *Operating System*) until two of
four trigger criteria held. This release cleared two:

- **Multi-workflow orchestration** — the `POST /api/ai/action`
  dispatcher with 11 admin-tunable actions (sub-actions, per-row
  placement, prompt CRUD, dry-run), composed across three control
  planes (`paimos` CLI, `paimos-mcp`, REST + in-app surfaces).
- **Public API for integration** — `/api/openapi.json` (PAI-119),
  the self-describing `/api/schema` (PAI-87), the v2.0 agent-context
  layer (`/projects/:id/{repos,manifest,anchors,graph,retrieve}`,
  `/issues/:id/anchors`), and the `paimos-mcp` MCP facade.

The remaining two trigger criteria — third-party plugin loop and
marketplace/template store — frame the open Phase 2 roadmap. Brand
colour and DE wordmark trademark are Phase 2 *deliverables*, not
Phase 2 *gates*, and stay deferred until they earn their own
commits. Phase 1 (FOSS) stays active alongside Phase 2; transitions
in the brand model are additive, never destructive.

Visible signals: `paimos.com` banner now reads `phase 2 - platform · v2.0.0`,
the homepage rotator cycles through four co-equal readings (FOSS /
Services / System / Platform), and `about.html` exposes the
Platform reading and the trigger-criteria reasoning.

## [1.10.3] — 2026-04-26

### Changed — Settings → AI prompts edit modal (PAI-183)

Full UX rewrite of the edit modal. The first cut had four real
problems: the "Enabled" checkbox sat centered while its label
right-aligned, the dry-run was buried in a `<details>`, the Cancel /
Run / Save buttons were three different shapes and sizes, and the
dry-run "Issue ID" was a raw number input.

The redesign organises the modal into card-shaped sections with
small uppercase tracked-out titles + one-line hints — Identity (for
custom rows) · Placement · Status · Prompt template · Dry-run
console. The Enabled control became a proper iOS-style switch with
the label on the left. Cancel + Save are now visually identical
40px buttons in a sticky footer that pins to the modal viewport
bottom with a subtle backdrop-blur.

The variable chips above the prompt textarea now insert at the
**cursor position** (and restore focus + caret) instead of
appending to the end — feels considerably better when an admin is
composing a prompt and wants to drop a variable mid-sentence.

The dry-run console is now a permanent panel, not a `<details>`,
with the issue picker on the left and a "Run preview" button on
the right (both 40px tall, aligned). Result renders as a meta
strip (model · latency · tokens · "code default" pill) plus a
2-pane grid: rendered prompt | model response, each in monospace
with a 320px scroll cap.

### Added — Smart issue search picker (PAI-183)

New `IssueSearchInput.vue` component replaces the raw number
input in the dry-run console:
- Empty state: input with leading magnifier and placeholder
- 200ms debounced `GET /api/issues?q=…&fields=list&limit=10`
- Results popover renders type icon + key (mono pill) + status dot
  + title per row
- Keyboard nav: ↑/↓/Enter/Esc; outside-click and Esc dismissal
- Selected state: bordered chip with the same row shape and a ✕ to
  clear, focusing the input back on clear so admins can immediately
  type a new query
- Self-contained: v-model is the id (number | null), the component
  manages its own `selectedIssue` ref for chip rendering

### Changed — AI Settings sticky action bar (PAI-182)

The Last saved + Test connection + Save changes footer moved to
the top of the AI Settings tab and is pinned with `position:
sticky; top: 0`. Test result + save banners sit just below the
bar so the response of clicking either button shows up where the
user just clicked, without scrolling.

### Fixed — Test connection accepts the saved key (PAI-180)

The endpoint had been demanding model + API key in the form payload,
which forced admins to re-paste the key just to run a smoke test
(the SPA never echoes the saved key back). Now the backend falls
back to `ai_settings` for any blank form field; the frontend's
`canTest` flips on as soon as the form has a model AND either a
typed key or a previously-saved one.

### Note on commit-message ticket numbers

Commits in v1.10.1 and v1.10.2 use the labels `PAI-178` / `PAI-179`
in their messages. Those labels collide with real existing tickets
(PAI-178 is the parked AI web-tool epic; PAI-179 is the CRM-move
ticket). The canonical backing for the polish work is **PAI-180**
(v1.10.1) and **PAI-181** (v1.10.2). The commit labels remain as
historical record.

## [1.10.2] — 2026-04-26

### Fixed
- **AI menu was empty on every text field after login.** The action
  catalogue is loaded once at module import; the very first call
  fired before login and 401'd, leaving the cache permanently empty
  and every menu showing "No AI actions are configured for this
  surface yet." Two changes: failed loads no longer flip
  `actionsLoaded` (the next caller retries), and the AiActionMenu
  component nudges a refresh on mount when the catalogue is empty.

### Added — Action placement (PAI-179)

Each AI action now has a `placement` field — `text`, `issue`, or
`both` — that controls where it appears in the UI:

- **Text** actions sit inline next to text fields (textareas).
  Examples: Optimize wording, Translate, Tone check, Suggest
  enhancement, Spec-out, UI generation.
- **Issue** actions sit in issue-level menus only — the issue
  header (full view) and the side-panel header. Examples: Find
  parent / sibling, Generate sub-tasks, Estimate effort, Detect
  duplicates.
- **Both** shows everywhere (no built-in defaults to this; reserved
  for custom actions admins want surfaced broadly).

Defaults are set on each action and are admin-overridable per row
in **Settings → AI prompts → Edit**. The list view shows the
effective placement as a pill next to the surface pill.

#### Schema

- Migration **M79** adds `ai_prompts.placement` (TEXT, default
  empty). Empty means "use the registry default" — admins editing
  to "" via the Settings UI get the registry default back.

#### Endpoints

- `GET /api/ai/actions` now includes `placement` per item, with
  admin overrides folded in server-side.
- `GET /api/ai/prompts` exposes both the admin override
  (`placement`) and the registry default (`default_placement`) so
  the editor can surface "(default: text)" next to the radio.
- `PUT /api/ai/prompts/{id}` accepts a new `placement` field —
  validates against `"" | "text" | "issue" | "both"`. Built-in
  rows now allow placement edits (it's a UX choice, not structural).

#### Frontend

- `<AiActionMenu>` accepts a new `placement` prop (`"text"` | `"issue"`).
  The filter combines surface + placement so text-field hosts only
  see text actions and issue-level hosts only see issue actions.
- `IssueDetailView` mounts an issue-level AI menu in the header
  (both view-mode and edit-mode toolbars).
- `IssueSidePanel` mounts an issue-level AI menu next to the
  pin / next / prev / clone buttons.
- Settings → AI prompts edit modal grows a placement radio cluster
  with `Default / Text / Issue / Both`. List rows show a placement
  pill so admins see which actions land where without opening the
  modal.

## [1.10.1] — 2026-04-26

### Changed — AI polish round

- **Test connection now works with the saved key.** The endpoint
  was demanding a model + API key in the form payload, which made
  admins re-paste the key just to run a smoke test (the SPA never
  echoes the saved key back). Now the handler falls back to the
  saved settings for any missing field; pasting in the form still
  overrides them. Friendlier failure copy when nothing is set up
  on either side.
- **Frontier model picks are vendor-diverse.** The Frontier
  category used to cluster on whichever vendor was trending that
  week; now the picker explicitly takes the top frontier-priced
  model from each of Anthropic, OpenAI, xAI, and Google in that
  order, with vendor pills on the cards so the row is scannable
  at a glance. Trending order still drives the choice WITHIN each
  vendor's bucket.
- **Model picker grid pinned to 4 cards per row** at the new
  1200px tab width; 3/2/1 column responsive overrides kick in on
  narrower viewports.
- **Settings → AI tab widened from 920px → 1200px** to fit the
  4-up grid without squeezing cards below the readable minimum.
- **Project Context moved into the toolbar toggle cluster** next
  to Docs and Coop, instead of rendering full-width above the
  issue tabs. The section now slides in from the right like the
  other aux panels; the toggle button shows a small "i" badge
  when at least one repo or any manifest content exists.

### Added — Real prompts in the prompt editor

- **Built-in action prompts now seed into `ai_prompts.prompt_template`
  on first list call**, so admins see the actual default in the
  Settings → AI prompts editor instead of an empty textarea. Backfill
  for instances that seeded under v1.10.0 (which left the column
  empty) runs idempotently on the next list.
- **Each action handler now reads its system prompt from the
  ai_prompts row**, falling back to the code-defined constant when
  the row is missing or empty. Net effect: edits in Settings →
  AI prompts actually take effect at request time.
- **Reset writes the current code default into the row** instead
  of clearing it. The editor stays useful after reset and
  benefits from any future default changes shipped in code.
- **All 11 built-in prompts rewritten / sharpened** with explicit
  invariants, output schemas, named-entity preservation rules,
  and per-action style guidance (verb-first sentences for optimize,
  testable single conditions for spec-out, vendor-honest
  confidence tiers for find-parent, etc.). The prompts live in
  `backend/handlers/ai_action_prompts.go` as the single source of
  truth; the handlers are now thin and admin-tunable.

## [1.10.0] — 2026-04-25

### Added — AI action suite (PAI-159 → PAI-177)

The single-purpose AI optimize button (PAI-146) is now a **multi-action
dropdown menu** with 9 actions on the issue editor surface and 2 on the
customer surface. Behind it sits a unified action dispatcher, an
admin-editable prompt store, and live model recommendations from
OpenRouter — all configurable through the Settings → AI tabs.

#### New endpoints

- `POST /api/ai/test` (admin) — fixed-prompt smoke test that grades
  via a literal `OK` / `FAIL` whole-word marker. 15 s timeout, 50 token
  budget. Audited under `audit: ai_test ...`. **PAI-159**
- `GET /api/ai/models` (admin) — server-cached top-3 OpenRouter
  models in 6 categories: Frontier, Value, Fastest, Cheapest, Open
  weights, Free. 1 h cache; `?force=1` busts it. Falls back to the
  last-known-good snapshot (`stale: true`) and finally to a curated
  static list when OpenRouter is unreachable. **PAI-160**
- `GET /api/ai/usage` (admin) — per-user daily token totals, request
  counts, effective cap, and admin-override flag. Surfaced in the
  Settings → AI usage panel. **PAI-161**
- `POST /api/ai/action` — unified dispatcher with action registry
  (`ai_action_<key>.go` per action). Replaces the legacy
  `/api/ai/optimize`. Per-action handlers receive a populated
  `aiActionContext` so they focus on prompt + provider + response.
  **PAI-163**
- `GET /api/ai/actions` — catalogue endpoint so the menu renders from
  server data and stays in sync as actions ship. Each entry carries
  `implemented: bool`. **PAI-163**
- `GET/POST/PUT/DELETE /api/ai/prompts` + `/{id}/reset` +
  `/{id}/dry-run` (admin) — full CRUD for the new `ai_prompts`
  table. Built-in rows lazily seed from the action registry; custom
  rows added by admins appear in the menu via the catalogue.
  **PAI-175 / PAI-177**

#### New schema

- M77 `ai_usage(user_id, day, prompt_tokens, completion_tokens,
  request_count)` + `users.ai_cap_override_tokens`. **PAI-161**
- M78 `ai_prompts(id, key UNIQUE, label, surface, parent_action,
  sub_action, prompt_template, enabled, is_builtin,
  default_template_hash, created_at, updated_at)`. **PAI-175**

#### New actions (issue surface)

| Action                   | Sub-actions                                                  | Result UX |
| ------------------------ | ------------------------------------------------------------ | --------- |
| **Optimize wording**     | —                                                            | diff overlay (default click) |
| **Suggest enhancement**  | security · performance · ux · dx · flow · risks              | checklist modal, append to AC/notes |
| **Spec-out**             | —                                                            | categorized checklist (4 categories) |
| **Find parent/sibling**  | —                                                            | top-3 candidate cards |
| **Translate**            | de_en · en_de                                                | diff overlay |
| **Generate sub-tasks**   | —                                                            | editable checklist, batch-create children |
| **Estimate effort**      | —                                                            | popover with h + LP + reasoning |
| **Detect duplicates**    | —                                                            | top-5 cards with similarity tag |
| **UI generation**        | —                                                            | markdown preview, append/replace |

#### New actions (customer surface)

- **Optimize wording** (existing, ported)
- **Tone check (de-sales)** — strips persuasive / sales-y phrasing
  while preserving every named entity, quote, and markdown structure
  verbatim. Available on `customer_notes`,
  `cooperation_sla_details`, `cooperation_notes`. **PAI-173**

#### Settings → AI

- **Test connection** button next to Save — pings the *unsaved* form
  values so admins can verify a (provider, model, key) triple
  before persisting. **PAI-159**
- **Live model recommendations** replace the static 5-card list. Six
  category sections, each with up to 3 cards showing name, slug,
  context window, $/Mtok pricing, and tags. Manual **Refresh** button
  bypasses the 1 h cache. Manual model-id input stays always-visible
  (per the answer to "should manual override be hidden?" — no). **PAI-160**
- **Usage today** panel: org tokens, request count, default cap, UTC
  day pill, plus a per-user table that highlights over-cap rows in
  red. **PAI-161**

#### Settings → AI prompts (new tab)

- List view grouped by built-in / custom. Each built-in row exposes
  Edit + Reset (when overridden); custom rows expose Edit + Delete.
- Edit modal with monospace template editor, per-surface variable
  picker (clicking a variable inserts `{{.Var}}`), enabled toggle,
  and dry-run launcher. **PAI-176**
- Dry-run preview renders the template against a real issue using
  Go `text/template`, calls the LLM once, and shows the rendered
  prompt + response side-by-side without mutating any state.
  **PAI-177**

#### Audit

The audit prefix moved from `audit: ai_optimize ...` to
`audit: ai_action action=<key> sub_action=<sub>? ...`. Operators with
grep patterns on `ai_optimize` need a one-line update. PAI-153
invariant unchanged: NO body content ever appears in audit lines.
A separate `audit: ai_test ...` covers the test-connection ping.

#### Compatibility notes

- The legacy `POST /api/ai/optimize` endpoint is **removed**. The
  frontend `useAiOptimize` composable now POSTs to `/api/ai/action`
  with `action="optimize"` (issue surface) or `action="optimize_customer"`
  (customer surface).
- Frontend `<AiOptimizeButton>` is **deleted**. All 13 host surfaces
  use the new `<AiActionMenu surface="issue|customer">`.

## [1.9.1] — 2026-04-25

### Fixed
- **AI error banner stuck on screen with empty message.** The 1.8.2 fix
  to `errMsg()` was correct, but the banner's `v-if="aiOptimize.lastError"`
  was checking the Vue `Ref` object itself — which is always truthy —
  instead of its unwrapped value. Vue auto-unwraps refs in templates
  only when they're top-level `<script setup>` bindings or live on a
  `reactive()` proxy; nested access on the plain object returned by
  `useAiOptimize()` skipped that. Symptom: a permanent red banner
  reading "AI optimization failed:" with no detail, even on cold
  page-loads where no optimize call had ever happened. The interpolation
  `{{ ... }}` rendered empty correctly (Vue's `toDisplayString` does
  unwrap refs), which masked the v-if defect. Fixed by destructuring
  `lastError` (and `clearError`) into top-level bindings in
  `AiOptimizeBanner.vue` and `IssueDetailView.vue`. Documented the
  gotcha in `useAiOptimize.ts` so future consumers don't repeat it.

## [1.9.0] — 2026-04-25

### Added — AI optimize on project / customer / cooperation fields

The PAI-146 button now appears wherever a user enters longer-form
prose, not just on issue editors. Each new field gets its own
per-field prompt reminder so the model writes at the right register
for the audience and format. The fixed PAIMOS wrapper and the
admin-editable instruction stay shared (safety + tone are global);
only the per-field reminder differs.

New surfaces:

- **Project description** — both the new-project modal and the
  edit-project modal. Reminder: stakeholder audience, preserve
  scope / out-of-scope markers / deadlines / contractual language;
  do not add scope.
- **Customer notes** — both the create-customer modal and the
  edit-customer modal. Reminder: CRM tone, preserve PII and verbatim
  quotes, do NOT invent titles / dates / decisions.
- **Cooperation → SLA details** (PAI-61). Reminder: preserve every
  number verbatim (uptime %, response-time targets, hours-of-coverage,
  escalation steps); "4 hours" stays "4 hours", not "a few hours".
- **Cooperation → Cooperation notes** (PAI-61). Reminder: preserve
  named systems, contractual lines, ownership boundaries, and
  exceptions exactly as written.

Backend allow-list extended (`project_description`, `customer_notes`,
`cooperation_sla_details`, `cooperation_notes`); each new field is
audited under its own name in the `audit: ai_optimize` line so
operators can attribute usage per surface.

No schema or env changes.

## [1.8.2] — 2026-04-25

### Fixed
- **AI optimize button now appears on every multiline issue editor** —
  the v1.8.0 rollout only wired it into the issue-detail edit form.
  Added to **CreateIssueModal** (the per-project + global "New issue"
  flow) and **IssueSidePanel** (slide-out edit panel) so the action
  is reachable wherever a user enters description / acceptance
  criteria / notes text. For new issues `issue_id=0` is sent, which
  the backend already handled by skipping context lookup.
- **Empty error banner.** `errMsg()` could pass an empty/whitespace
  string through to the UI, producing "AI optimization failed:" with
  no message. Replaced with a guard that always returns a non-empty
  string and, for `ApiError` with an empty payload, surfaces the HTTP
  status as `request failed (HTTP <status>)` so admins have something
  to grep. New `<AiOptimizeBanner>` component used by all three host
  surfaces so the banner experience is consistent.

## [1.8.1] — 2026-04-25

### Fixed
- **Settings → AI** tab redesigned. The first 1.8.0 cut shipped with a
  broken toggle row (checkbox, label, and hint distributed across the
  page width) and form inputs stretched to the full tab area instead
  of capping. Replaced with a card-stack layout: hero strip with
  status pill (Ready / Configured · Off / Disabled / Needs
  configuration), enable as a real switch, provider cards with
  PAI-122 placeholders, model preset grid with category tags
  (Fast / Quality / Open weights / Cheap), and a `<details>` disclosure
  listing the six wrapper invariants admins cannot override. UI only —
  no backend or payload-shape changes.

## [1.8.0] — 2026-04-25

### Added — PAI-146 epic: LLM text optimization for multiline fields

An inline AI-assisted writing affordance for the multiline fields
authors reach for the most. Off by default; opt-in per deployment.

- **PAI-147** — Reusable `<AiOptimizeButton>` (ghost-style "AI" pill)
  appears on description, acceptance criteria, and notes editors in
  the issue detail view. Disabled with a tooltip when the feature is
  not configured.
- **PAI-148** — Diff-preview overlay (`<AiOptimizeOverlay>`) compares
  the current and optimized text side-by-side, anchored on unchanged
  lines and tinted on changed ones. Accept / reject / retry, plus
  Esc-closes-as-reject. Works on desktop and mobile (≥720px is two
  columns; below stacks vertically). Inline LCS line diff — no new
  npm dep.
- **PAI-149** — Admin **Settings → AI** tab. Persisted in M74
  `ai_settings` (singleton row): provider, model, API key,
  optimization instruction, enabled flag. API key uses the same
  "currently set / replace" pattern as the CRM secret fields so an
  unrelated edit can't silently clear it.
- **PAI-150** — Fixed PAIMOS-owned system wrapper carries the
  invariants the product enforces: preserve technical meaning,
  preserve markdown structure, preserve architecture-significance
  phrasing (architecture change / breaking change / schema change /
  infra change / new component, plus version-and-migration tokens
  like `M74` / `v1.7.0`), do not translate, do not add scope. Admin
  instruction layers inside via `{{INSTRUCTION}}`. Per-call context
  block carries issue key/type/title, project name, parent epic,
  and field-aware reminders (acceptance_criteria stays a checklist,
  notes keeps the author's voice).
- **PAI-151** — Provider abstraction (`backend/ai/Provider`) keeps
  every vendor-specific concern off the editor flow, the prompt
  wrapper, and the audit pipeline. PAI-122 (local backends) plugs in
  by registering a new `Provider` — no other change required.
- **PAI-152** — OpenRouter provider behind the abstraction. Talks the
  OpenAI-compatible `/chat/completions` endpoint (no SDK, no extra
  deps), maps 401/403 → `ErrProviderUnconfigured`, 429/5xx →
  `ErrProviderUnavailable`, other 4xx surfaces the upstream message
  verbatim ("model not found: foo/bar"). 256 KiB body cap, 60 s
  per-call timeout owned by the handler.
- **PAI-153** — Structured `audit: ai_optimize …` line per call with
  user_id, field, issue_id, model, outcome (`ok` / `fail` / `denied`),
  latency_ms, prompt_tokens, completion_tokens. Prompt and response
  bodies are NOT logged. A regression test
  (`backend/handlers/ai_optimize_audit_test.go`) enforces this.
- **PAI-154** — Rollout to the three target fields in the issue detail
  editor and operator documentation under
  `docs/CONFIGURATION.md` ("AI text optimization (PAI-146)").

**Operator quick-start:** Settings → AI → toggle Enabled → paste an
OpenRouter API key from `openrouter.ai/keys` → pick a model
(`anthropic/claude-3.5-haiku` is a reasonable default) → Save. The
"AI" pill on multiline editors lights up immediately.

## [1.7.0] — 2026-04-25

### Added — PAI-109 epic: Enterprise Security & 03-Specs Readiness (8.5/10 target)

Twelve of fourteen children land in this release; the remaining two
(PAI-110 active-content upload hardening, PAI-122 paimos.com wording
rollback) are tracked separately. The audit at `audit.md` is the
companion document.

**Shipped security defects:**

- **PAI-111** — `GET /api/documents/{id}/download` enforces scope-aware
  authorization. Project-scoped documents require project view access;
  customer-scoped require admin OR view access to a project belonging
  to that customer. 404 on deny so id enumeration cannot probe
  existence.
- **PAI-112** — `PATCH /api/attachments/link` requires `uploaded_by =
  current_user` for non-admin callers, closing the cross-user
  pending-attachment hijack window.
- **PAI-113** — Per-session CSRF defenses for cookie-authenticated
  browser flows. New `csrf_token` column on `sessions` (M72), bound at
  login/TOTP-verify and exposed via a non-HttpOnly `csrf_token` cookie.
  `auth.CSRFMiddleware` enforces same-origin (`Origin`/`Referer` host
  matches `Host`) plus `X-CSRF-Token` match for every session-cookie
  mutation. API-key callers and pre-existing sessions (created before
  M72) are handled via a lazy-upgrade path that issues a token on the
  first authenticated request after deploy.
- **PAI-114** — Global response-header middleware applies `nosniff`,
  `X-Frame-Options=SAMEORIGIN`, `Referrer-Policy`, `Permissions-Policy`,
  conditional HSTS (when `COOKIE_SECURE=true`), and a
  Content-Security-Policy in **Report-Only** mode that posts violations
  to `/api/csp-report`. Non-breaking: in-app PDF and dev-report iframes
  continue to render.
- **PAI-115** — Password-reset link logging requires explicit
  `PAIMOS_DEV_MODE=true` opt-in. Without it, the handler refuses to
  send when SMTP is unconfigured and surfaces the misconfiguration
  rather than silently routing magic links into log aggregators.

**Compliance / audit:**

- **PAI-116** — `PAIMOS_AUDIT_SESSIONS` defaults to **on** (operators
  opt out with `=false` / `=0`). New `incident_log` table (M73) plus
  admin-only CRUD and JSON/CSV export at `/api/incidents/export` for
  SIEM ingestion. Status transitions auto-stamp `resolved_at`.
- **PAI-117** — GDPR ops pack. Background retention sweeper (24h loop)
  with env-tunable windows for sessions, password reset tokens, access
  audit, session activity, closed incidents, and pending TOTP. New
  admin endpoints: `GET /api/users/{id}/gdpr-export` (full per-subject
  JSON dump), `POST /api/users/{id}/gdpr-erase` (anonymisation rather
  than cascade-delete, preserves historical project data), and `GET
  /api/gdpr/retention` (introspect the active policy).
- **PAI-118** — Bricolage Grotesque, JetBrains Mono and DM Sans now
  bundle via `@fontsource` and ship as content-hashed `/assets/*.woff2`
  files. All `fonts.googleapis.com` / `fonts.gstatic.com` runtime
  requests are gone. CSP-Report-Only is now strictly self-only.

**API contract / SSO:**

- **PAI-119** — `/api/openapi.json` publishes a real OpenAPI 3.1
  contract, embedded at build time so the document always matches the
  binary it ships with. Coverage focuses on the canonical surface;
  internal admin one-offs are intentionally omitted.
- **PAI-120** — Single-provider OpenID Connect SSO end-to-end with
  PKCE (`S256`) and JIT user provisioning. New routes:
  `GET /api/auth/oidc/{status,login,callback}`. The login page renders
  an "SSO" button only when `enabled=true` is reported by `/status`.
  JIT matches existing users by case-insensitive email; new users land
  as role=member with seeded project access. Configuration via
  `OIDC_*` env vars, documented in `docs/CONFIGURATION.md`.

**Release evidence:**

- **PAI-121** — CI tag-push generates Go and npm CycloneDX SBOMs,
  signs the published image keylessly via cosign + GitHub OIDC, and
  attaches each SBOM as a cosign attestation against the image
  digest. `scripts/sbom.sh` (`just sbom`) for local generation;
  `docs/RELEASE.md` documents the verification commands.

**Governance:**

- **PAI-123** — `docs/claim-matrix.md` ties every paimos.com
  `/03-specs` claim to its in-repo evidence and follow-on tickets.
  `scripts/check-claims.sh` is wired into `scripts/release.sh` and
  refuses to cut a release if any `aspirational` row lacks a follow-on
  ticket reference. `--yolo` bypass is supported with the reason
  recorded in the release commit message.

### Configuration changes

New environment variables documented in `docs/CONFIGURATION.md`:

- `PAIMOS_DEV_MODE` — gate password-reset link logging.
- `PAIMOS_AUDIT_SESSIONS` — default flipped to `true`; opt out with
  `false` / `0`.
- `PAIMOS_RETENTION_DAYS_*` — per-class retention windows.
- `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`,
  `OIDC_REDIRECT_URL`, `OIDC_SCOPES`, `OIDC_BUTTON_LABEL`,
  `OIDC_POST_LOGIN_REDIRECT`.
- `HSTS_INCLUDE_SUBDOMAINS` — opt-in HSTS subdomain inclusion.

### Migrations

- **M72** — `ALTER TABLE sessions ADD COLUMN csrf_token TEXT NOT NULL
  DEFAULT ''`. Existing sessions are upgraded lazily on first use; no
  forced re-login.
- **M73** — `incident_log` with status/severity CHECK constraints,
  plus indexes on `status` and `detected_at`.

### Notes

- The `id_token` signature is intentionally **not** verified locally;
  trust comes from the TLS userinfo round trip back to the IdP. Adding
  JWKS-based verification is straightforward when a deployment
  requires it.
- CSP runs in Report-Only mode in this release. After a clean
  violation feed, a follow-up release will flip it to enforce.
- PAI-110 (active-content upload hardening) is **postponed** because
  the cleaning would reject SVG/markdown/text uploads existing UIs
  depend on. It is the only Critical from the audit still open and
  carries into the next phase.

## [1.6.1] — 2026-04-24

### Fixed
- HubSpot provider tile in Settings → CRM and the customer-import
  dropdown were rendering a broken-image placeholder — `LogoURL()`
  pointed at `/assets/crm/hubspot.svg` but the asset wasn't shipped.
  Added a stylised neutral mark under `frontend/public/assets/crm/`
  so the tile reads as intentional. New providers ship their own
  SVG under the same path; see `docs/CRM_PROVIDERS.md`.

## [1.6.0] — 2026-04-24

### Added — PAI-28 epic complete: cooperation metadata + CRM provider docs (PAI-61 / PAI-62 / PAI-107)

This release closes the **PAI-28 customer-management epic**: data
model + plugin layer + HubSpot provider + frontend + cooperation
profile + developer docs all shipped. Bumped to **1.6.0** to mark the
epic as feature-complete.

What landed across 1.5.3 → 1.6.0:
- 1.5.3 — backend foundation (customers, documents in MinIO, plugin
  layer, HubSpot provider)
- 1.5.4 — frontend (sidebar, customer views, admin Integrations CRM
  tab, project polish)
- **1.6.0 (this release)** — cooperation metadata + CRM developer docs

### This release in detail (PAI-61 / PAI-62 / PAI-107)

Closes the PAI-28 epic frontend work plus the deferred plugin-layer
docs.

- **Per-project cooperation metadata** (PAI-61). New
  `project_cooperation` table (1:1 with projects, M71): structured
  fields for engagement type, code ownership, environment
  responsibility, plus an SLA bundle (uptime, response time, backup
  + on-call flags) and two markdown freeform fields. Two endpoints:
  `GET /api/projects/:id/cooperation` returns the row or zero-value
  defaults; `PUT /api/projects/:id/cooperation` upserts (admin-only).
  CHECK constraints validated server-side; informational in v1, no
  behavioural effects elsewhere.
- **Cooperation profile section on project detail** (PAI-62).
  View/edit toggle; structured fields shown as labelled value pills;
  SLA section conditional on `has_sla`; freeform fields rendered
  through the existing `useMarkdown` composable. Empty state shows a
  "Set up profile" button for admins so first-time use isn't a
  hidden form.
- **CRM provider developer docs** (PAI-107). New
  [`docs/CRM_PROVIDERS.md`](CRM_PROVIDERS.md) — interface
  walkthrough, copy/paste skeleton, config schema field types, error
  handling conventions, and a worked Pipedrive example. Linked from
  `docs/AGENT_INTERFACE.md` and `docs/DEVELOPER_GUIDE.md`.

## [1.5.4] — 2026-04-24

### Added — customers + CRM provider UI (PAI-28 frontend; PAI-101 admin)

Frontend for the customer / document / CRM-plugin layer that landed in
v1.5.3. Manual customers and CRM-linked customers render through the
same components — provider affordances are conditional, not a
missing-state stub.

- **Customers in the sidebar** (PAI-57). New nav entry below Projects.
  Card-grid list view at `/customers` with a search box and a
  split "+ New customer" button: primary action is always manual
  create (the no-CRM path); the dropdown only lights up when at
  least one CRM provider is enabled + configured.
- **Customer detail** (PAI-58). Sticky identity header with the
  customer name, optional provider badge, default rates, and a
  state-machine sync button (idle → loading → success / error).
  Two-column contact + notes section. Project cards listed inline
  with effective rates and an inheritance indicator. Documents
  section reuses the same component as the project page.
- **DocumentsSection** — scope-agnostic component used by both
  customer and project detail. The whole section is the drop-target
  (no separate dropzone modal). PDFs preview lazily via inline
  `<iframe>` thumbnails; status pills (active / expired / draft);
  20 MB cap; admin-only writes.
- **Admin CRM tab** (PAI-105). New Settings → CRM tab. Each
  registered provider gets a card with status pill (`Disabled` /
  `Enabled` / `Needs configuration`), an enable toggle, and an
  expandable config form rendered from the provider's
  `ConfigSchema`. Secret fields are never echoed: stored values
  show as `••••• currently set · Replace · Clear`. The toggle
  refuses to enable a misconfigured provider both client- and
  server-side.
- **Project detail polish** (PAI-59 / PAI-60). Customer pill in the
  page header (links through to the customer); customer
  assignment dropdown in the edit modal; rate inputs show an
  inline "Inherits €X from {Customer}" hint when left blank;
  project documents section appended below the issue list.
- **Projects view** (PAI-63). Customer filter dropdown in the
  toolbar (with an "Unassigned" option); each project card shows
  a small linked customer pill when assigned.
- **`useExternalProvider`** composable (PAI-106). Single shared
  fetch of `/api/integrations/crm`; consumers ask for a provider
  by id, get back logo / name / deep-link template — no place in
  the UI hardcodes a CRM name.

Visual language stays consistent with the existing app — DM Sans,
existing color tokens, card hover-lift, monospace numerals, sticky
headers.

## [1.5.3] — 2026-04-24

### Added — customers + documents + CRM provider plugin layer (PAI-28 + PAI-101)

Backend foundation for the customer-management epic, refined for CRM
independence. PAIMOS now owns the customer / document / rate-cascading
data model directly; external CRM sync (HubSpot is the first provider)
plugs in through a small Go interface.

- **Customers** (PAI-53): new `customers` table with CRM-agnostic
  `external_*` columns (NULL = manual customer, fully supported as a
  primary mode). Full CRUD endpoints under `/api/customers`. FTS5
  indexed for search; admin-only writes; pair-validation triggers
  enforce that `external_id` and `external_provider` are both set or
  both null at the DB layer.
- **Project ↔ customer FK + rate cascading** (PAI-54): legacy
  freeform `projects.customer_id` column renamed to `customer_label`;
  new `customer_id` is an INTEGER FK to `customers.id`. List + detail
  responses now carry `effective_rate_hourly`, `effective_rate_lp`,
  and `rate_inherited` so the UI can show inherited-vs-overridden
  without computing it client-side.
- **Documents** (PAI-55): new `documents` table with a `scope`
  (`customer` | `project`) constraint that enforces exactly one of the
  two FKs is set per row. File bytes go to MinIO under the
  `documents/<scope>/<id>/…` key namespace (same bucket as
  attachments — single object store for both). 20 MB cap; PDF / image
  / Office MIME allowlist; status badges (`draft` | `active` |
  `expired`) + validity dates.
- **CRM provider plugin layer** (PAI-101 + children): new
  `backend/handlers/crm` package defining the `Provider` Go
  interface, in-process registry, generic
  `POST /api/customers/import`, `POST /api/customers/:id/sync`, and
  the admin Integrations endpoints (`/api/integrations/crm`).
  Provider configs persist in a new `provider_configs` table with
  AES-GCM-encrypted secrets at rest (key from `PAIMOS_SECRET_KEY` env
  or auto-generated under `$DATA_DIR/.secret-key`). Secrets are never
  echoed in API responses or log lines — only a `has_value` flag per
  field so the admin UI can render "••••• has value".
- **HubSpot provider** (PAI-56): first reference implementation.
  Imports a HubSpot company by URL or bare ID, supports manual
  re-sync (preserves PAIMOS-only fields), builds deep-link URLs back
  into HubSpot. Wired in `main.go` via blank import — adding a new
  provider is one line there + one new subpackage.

Frontend types updated for the renamed `customer_label` column;
sidebar / customer list / customer detail views (PAI-57 / PAI-58) and
the admin CRM Integrations UI (PAI-105) ship in a follow-up.

## [1.5.2] — 2026-04-24

### Changed — appearance-state ownership (PAI-84)

Carved out of PAI-40. Three categories of appearance state previously
had ~3 owners each, with `useBranding` and `useTableAppearance` racing
to set the same `--table-row-*` CSS vars on first paint.

- New `useTypeColors` composable: sole owner of `--type-{epic,ticket,task}`.
  `SettingsAppearanceTab` now consumes it via thin computed get/set
  wrappers instead of holding duplicate refs + watchers.
- `useTableAppearance` is now sole owner of `--table-row-border` and
  `--table-row-alt`. Reset via the settings tab now reverts the CSS
  vars to branding defaults live (was a silent bug — required a page
  reload).
- New `useSidePanelWidth` composable shared by `IssueSidePanel` and
  `IssueList`. `IssueList` no longer reaches past the panel boundary
  to read `LS_SIDEBAR_WIDTH` directly. Double-click reset on the
  resize handle now also updates the IssueList offset (previously
  unsynced).
- `useBranding.applyToDOM` no longer touches type or table-row CSS
  vars — it only supplies the defaults the two composables fall back
  to when no user override is set.

Pure structural cleanup. No user-facing behavior changes.

### Fixed — deploy script resilience

- `scripts/deploy.sh` now tolerates fish-shell quirks in remote SSH
  paths, strips PTY artifacts from captured output, and handles
  root-owned bind mounts without aborting the backup step.

## [1.5.1] — 2026-04-24

### Added — agent interface docs (PAI-96, PAI-85 epic step K)

- New `docs/AGENT_INTERFACE.md`: the single place to learn how to
  drive PAIMOS as an agent. Covers install + `auth login`, core
  patterns (keys-or-ids, file-first multi-line, `--dry-run`,
  `--json`), an end-to-end transcript of a real session (ticket →
  branch → deploy → close-with-note), bulk ops (`batch-update`,
  `apply`), schema discovery, MCP wiring for Claude Desktop, and
  explicit failure-mode guarantees.
- Linked from `DEVELOPER_GUIDE.md` as the top-of-document pointer
  for agent-driving readers.

### Closes the PAI-85 epic

All 12 children (PAI-86 → PAI-97) shipped across 12 PRs in one day:

- **API** (A–D): keys everywhere, `/api/schema`, bulk endpoints,
  new relation types.
- **CLI** (E–I): bootstrap, write commands + close-note,
  batch-update + apply, schema + doctor, shell completions.
- **MCP** (J): 6-tool stdio facade.
- **Docs** (K): this file.
- **Session audit** (L): opt-in UUIDv7 mutation trail.

The shell-quoted-JSON foot-gun, the numeric-id lookups, the
project-ID mismatch between instances, and the status-enum guessing
are all gone from the agent hot-path.

## [1.5.0] — 2026-04-21

### Added — MCP facade (PAI-95, PAI-85 epic step J)

- New `paimos-mcp` binary at `backend/cmd/paimos-mcp/`. Hand-rolled
  JSON-RPC 2.0 over stdio (newline-delimited frames). Spawned by
  Claude Desktop / other MCP clients as a subprocess.
- **6 tools** exposed in v1 (allowlist from the epic pickup doc):
  - `paimos_schema` — fetch `/api/schema` for pre-call validation.
  - `paimos_issue_get` — ref (key or id).
  - `paimos_issue_list` — project_key + status/type/priority filters.
  - `paimos_issue_create` — title + project_key required, full field set.
  - `paimos_issue_update` — partial update by ref.
  - `paimos_relation_add` — all 7 PAI-89 relation types.
- **Deliberately NOT exposed**: `batch-update`, `apply`. MCP context
  grows fast — if an agent needs bulk, it shells out to the
  `paimos` CLI.
- Shares `~/.paimos/config.yaml` with the CLI (same file authored
  by `paimos auth login`). `PAIMOS_CONFIG` / `PAIMOS_INSTANCE` /
  `PAIMOS_SESSION_ID` env vars for overrides.
- Errors from tool bodies surface as `isError=true` MCP results so
  agents see "issue not found" without the JSON-RPC envelope
  eating the message.

#### Example: register with Claude Desktop

```json
{
  "mcpServers": {
    "paimos": {
      "command": "/path/to/paimos-mcp",
      "env": {
        "PAIMOS_INSTANCE": "ppm"
      }
    }
  }
}
```

Live smoke (stdio): initialize → tools/list (6 tools) → 4 tools/call
invocations (schema, issue_get, issue_list, issue_get error path)
all return correct MCP-shaped responses.

## [1.4.1] — 2026-04-21

### Added — CLI batch-update + apply (PAI-92, PAI-85 epic step G)

- `paimos issue batch-update --from-file ops.jsonl` — streams JSONL
  (one `{"ref": …, "fields": {…}}` per line), chunks at 100 (the
  server's batch cap), each chunk is one `PATCH /api/issues`
  transaction. Reports per-chunk progress + a final summary. `-`
  reads stdin.
- `paimos apply --from-file plan.yaml` — declarative scaffolding:
  a single command creates an epic + N children + relations in
  one go. Named refs (`name: epic` on a create item) let later
  rows reference the same-plan item; the CLI translates to the
  server's positional `parent_ref: "#N"` before POSTing the batch.
- Both support `--dry-run` to print the resolved payload(s) without
  sending. **Not idempotent in v1** — running `apply` twice
  duplicates. Use `ensure-status` / `batch-update` for subsequent
  changes to scaffolded work.

#### Example plan

```yaml
project: PAI
create:
  - name: epic
    type: epic
    title: Quarter refactor
  - name: child1
    type: ticket
    title: Extract auth module
    parent: epic
relations:
  - source: epic
    type: related
    target: PAI-85
```

## [1.4.0] — 2026-04-21

### Added — session-scoped mutation audit (PAI-97, PAI-85 epic step L)

- New table `session_activity` (migration M68).
- `SessionAuditMiddleware` records mutation requests (POST / PUT /
  PATCH / DELETE) with method, path, status code, user id, and the
  value of the `X-PAIMOS-Session-Id` header. Reads are skipped so
  UI browsing noise doesn't bloat the table.
- **Off by default in v1.** Enable via `PAIMOS_AUDIT_SESSIONS=true`
  env var (reads per-request so flipping it at runtime works).
  Rationale: single-user instances don't need the data yet; flip
  the default once multi-agent use proves it valuable.
- Missing/malformed header is non-fatal: mutation succeeds, row
  written with `session_id = NULL` (operators can still spot
  untagged traffic).
- `GET /api/sessions/{id}/activity` (admin only): keyset-paginated
  by `id > cursor`, default limit 100, max 1000. Response includes
  `next_cursor` pointing past the last returned row (null on last
  page). Keyset not offset — audit tables grow unboundedly.

### Added — CLI session tagging

- The `paimos` CLI generates a UUIDv7 per invocation and sends it as
  `X-PAIMOS-Session-Id` on every request. `PAIMOS_SESSION_ID` env
  var overrides for multi-step shell flows that need to share one
  session across invocations.

## [1.3.3] — 2026-04-21

### Added — CLI shell completions (PAI-94, PAI-85 epic step I)

- `paimos completion bash|zsh|fish|powershell` — Cobra-native.
  Emits the completion script to stdout; install once per shell.
- **Enum-aware completions** driven by the schema cache:
  - Flags `--status`, `--type`, `--priority` on `issue list` /
    `issue create` / `issue update`.
  - Positional args: `issue ensure-status <ref> <status>` (second
    arg), `relation add <source> <type> <target>` (middle arg).
  - Zero network on tab-press — reads `~/.paimos/schema-<instance>.json`.
    Run `paimos schema` once per instance to populate.

#### Install examples

```sh
# fish
paimos completion fish > ~/.config/fish/completions/paimos.fish

# zsh (persistent)
paimos completion zsh > "${fpath[1]}/_paimos"

# bash
paimos completion bash > /etc/bash_completion.d/paimos   # or ~/.bash_completion
```

## [1.3.2] — 2026-04-21

### Added — CLI schema + doctor (PAI-93, PAI-85 epic step H)

- `paimos schema` — shows enums/transitions/conventions from the
  local cache; fetches transparently on first run. `--refresh`
  forces re-download and reports whether the server-side version
  moved. Cache file: `~/.paimos/schema-<instance>.json` (per-
  instance so multi-env setups don't clobber each other).
- `paimos doctor` — read-only preflight. Checks: config readable,
  `/api/health` reachable, API key valid (`/api/auth/me`), schema
  version current. Exit codes 0/1/2 (ok / warn / fail) per
  convention — CI-safe. `--json` emits the same result array for
  programmatic use.

## [1.3.1] — 2026-04-21

### Added — CLI write commands (PAI-91, PAI-85 epic step F)

- `paimos issue create --project PAI --type ticket --title "..." --description-file/--ac-file/--notes-file`
- `paimos issue update <ref> --status done --close-note-file note.md` —
  when the status is terminal (done/delivered/accepted/invoiced/cancelled),
  appends a **Close note** comment so the "why" is captured alongside
  the status change. Errors out if `--close-note` is passed without a
  terminal `--status`.
- `paimos issue ensure-status <ref> <status>` — idempotent: GETs current
  state, PUTs only if different; second run prints "already X" with
  exit 0. JSON mode reports `{changed: bool, previous, status}`.
- `paimos issue comment <ref> --body-file comment.md`.
- `paimos relation add <source> <type> <target>` — accepts all 7
  relation types (dogfoods PAI-89).

### Conventions

- **Multiline inputs are always file-first.** Inline `--foo "…"` is
  single-line by design. `--foo-file <path>` reads UTF-8 (or `-` for
  stdin). Mutually exclusive — combining inline + file errors out
  rather than silently preferring one.
- **`--dry-run`** on create/update prints the resolved request payload
  (method, path, body) as JSON to stdout and exits 0 without calling
  the API. Makes it safe to script without fear.
- Usage errors → stderr + exit 2. API errors → exit 1 (message in
  chosen format — pretty or `--json`). Nothing ever dumps HTML.

## [1.3.0] — 2026-04-21

### Added

- **`paimos` CLI — bootstrap** — PAI-90 (PAI-85 epic, E). New binary
  at `backend/cmd/paimos/`. Cobra + Viper-lite + `golang.org/x/term`
  (hidden API-key input) + `gopkg.in/yaml.v3` (config). Read-only
  surface for v1:
  - `paimos auth login` — interactive; prompts for URL + API key,
    verifies against `/api/auth/me`, writes `~/.paimos/config.yaml`
    (atomic temp+rename, mode 0600).
  - `paimos auth whoami` — shows active instance + user.
  - `paimos project list` — compact table or `--json`.
  - `paimos issue get <ref>` — ref is key or numeric id; pretty by
    default, `--json` pipes the server payload verbatim.
  - `paimos issue list --project PAI --status backlog --limit 20`.
  - `paimos issue children <ref>`.

  Global flags work on every subcommand: `--instance <name>` (multi-
  instance config), `--json` (machine output), `--config <path>`.
  Missing/invalid config → exit 2 with a pointer to `auth login`.
  API errors → exit 1, `--json` emits `{error, code}` (never HTML
  or unstructured dumps). `paimos` with no subcommand prints usage
  to stderr with exit 2.

### Note: CLI is bundled in the backend Go module (monorepo layout).
  Shared types with the server prevent schema drift. Distribution
  via `go install github.com/inspr-at/paimos/backend/cmd/paimos@latest`
  or a release binary — not via the PAIMOS Docker image.

## [1.2.8] — 2026-04-21

### Added

- **Three new relation types** — PAI-89 (PAI-85 epic, D):
  - `follows_from` (spin-off / carved-out ticket),
  - `blocks` (hard blocker, semantically distinct from `depends_on`),
  - `related` (loose "see also").

  Migration M67 extends the `issue_relations.type` CHECK constraint;
  existing rows unaffected. `GET /api/issues/{id}/relations` now tags
  every row with `direction: "outgoing" | "incoming"` so the UI can
  render inverse labels ("follows up on X" vs "followed up by Y")
  without a second stored row. Issue detail page picks up new form
  options + grouped rendering. `SchemaVersion` bumped to **1.1.0**;
  `/api/schema` `enums.relation` lists all seven types.

## [1.2.7] — 2026-04-21

### Added

- **Bulk issue endpoints** — PAI-88 (PAI-85 epic, C). Three new
  admin-only operations, all atomic under a single SQLite transaction
  with a 100-item hard cap (413 on exceed):
  - `POST /api/projects/{key}/issues/batch` — create N issues at once.
    Accepts project key or numeric id. Supports `parent_ref: "#N"`
    to point a child row at an earlier same-batch item, so the
    canonical "create an epic + all its children in one call" flow
    works without a round-trip-per-child.
  - `PATCH /api/issues` — update N issues at once. Body shape
    `[{ref: "PAI-83"|123, fields: {...}}, ...]`; any row failing
    validation rolls back the whole batch and returns per-row errors
    with `rolled_back: true`.
  - `GET /api/issues?keys=PAI-1,PAI-2,PAI-3` — pick list, response
    order matches request order, missing/inaccessible keys surface
    as `{ref, error: "not found"}` entries (never silently dropped).
- **Side-effect note** — bulk ops deliberately SKIP the auto-promote
  parent-epic / cascade-children / billing-timestamp logic that
  single-issue CreateIssue and UpdateIssue run. Bulk is mechanical;
  the CLI calls single endpoints when it wants the full lifecycle.

## [1.2.6] — 2026-04-21

### Added

- **`GET /api/schema` — self-describing discovery endpoint** — PAI-87
  (PAI-85 epic, B). Public, no auth required. Returns a versioned JSON
  payload with `enums` (status, priority, type, relation), recommended
  `transitions` (any→any still accepted server-side — these are hints
  for clients), `entities` (required/optional field lists per type,
  with `key_shape` for issues), and `conventions` (AC checkbox
  format, key case-sensitivity, multiline-input guidance). Strong
  ETag + `Cache-Control: public, max-age=300`; `X-Schema-Version`
  header mirrors the body's `version` string. The CLI (PAI-90+) and
  MCP (PAI-95) consume this before POSTing so typos like
  `status: "completed"` get caught client-side with a suggestion.
  Regression test hashes the payload — editing the schema without
  bumping `SchemaVersion` fails CI.

## [1.2.5] — 2026-04-21

### Added

- **API: accept issue keys everywhere** — PAI-86 (PAI-85 epic, A). Every
  `/api/issues/{id}/*` route now accepts either the numeric id or an
  issue key like `PAI-83` / `ACME26-639`. Keys resolve server-side via
  `(project.key, issue.issue_number)`; numeric ids keep working
  unchanged. Malformed refs return 400, key-shapes with no match 404.
  Soft-deleted issues still resolve so `restore` / `purge` can target
  them by key. New helper `auth.ResolveIssueRef` + `auth.IsIssueKey`;
  table-driven test in `keyresolve_test.go`.

## [1.2.4] — 2026-04-21

### Changed

- **Issue-status constants centralized + `IssueStatus` type completed**
  — PAI-44 (scope c). New `frontend/src/constants/status.ts` exports
  `STATUSES` (all 9 in canonical workflow order),
  `ACCRUALS_DEFAULT_STATUSES` (done / delivered / accepted / invoiced),
  and `ACCRUALS_EXTRA_STATUSES` (new / backlog / in-progress /
  cancelled — `qa` deliberately excluded). Four call sites migrated:
  `IntegrationsView.vue`, `ImportView.vue`, `AccrualsPrintView.vue`,
  `ProjectsView.vue` — the previously-duplicated literal arrays are
  gone.

### Fixed

- **`IssueStatus` type now reflects the full 9-status workflow**
  (`types.ts:73`). Previously omitted `qa` and `delivered`, which the
  backend already emits and the frontend handles at runtime — the
  type was silently wider than declared.

### Not changed (deliberately)

- Single-value equality checks like `if (s === 'done')` were left
  alone: TypeScript's `IssueStatus` union already gives compile-time
  safety there, so importing a const would be noise.
- `SprintBoardView`'s narrower "completed" arrays and the
  `useInlineEdit` / `IssueEditSidebar` "terminal" sets (which
  disagree on whether `delivered` counts as terminal) were not
  migrated. That's a semantic question for product review, not a
  centralization question. Flagged in the PAI-44 close note.
- `PT_FACTOR = 8` (`useTimeUnit.ts:38`) and the two `10 * time.Second`
  literals in `backend/handlers/integrations.go` — mentioned in
  PAI-44's description but not in its acceptance criteria; leaving
  to a separate ticket if ever warranted.

## [1.2.3] — 2026-04-21

### Changed

- **State-management pattern documented + localStorage keys
  centralized** — PAI-40. `docs/DEVELOPER_GUIDE.md` now names all
  three state tiers (Pinia store / module-scope composable singleton /
  component-local `ref`) with criteria for when each is appropriate,
  replacing the previous two-tier summary that didn't mention
  singleton composables despite their heavy use. New single source of
  truth `frontend/src/constants/storage.ts` exports every
  localStorage key the app touches — 22 static keys + 4 dynamic
  factory functions, migrated across 16 files. Legacy outliers
  (`sidebar-color-bg`, `sidebar-color-pattern`, `issue-display-type-*`,
  `paimos_time_unit`) keep their non-standard names to avoid wiping
  existing user preferences on upgrade; all documented in the module
  header. Pure refactor — no behavior change. Triple-ownership of
  type-color / table-row-color / sidebar-width state carved out to
  PAI-84 for separate treatment.

## [1.2.2] — 2026-04-21

### Fixed

- **"Session expired" banner stuck on after login** — PAI-83. Any 401
  from a non-auth endpoint flipped the global `sessionExpired` ref to
  `true`, but nothing ever cleared it. A user logging back in after a
  stale session saw the dashboard correctly populated — but the
  "Session expired. Your content may be out of date — please sign in
  again." banner persisted at the top. Fix: clear `sessionExpired` in
  `auth.setUser()` (which both password and TOTP login paths converge
  on) and in `auth.login()`. Added a regression test in
  `src/stores/auth.test.ts`.

## [1.2.1] — 2026-04-20

### Fixed

- **Issue-list scroll containment** — PAI-16. Two related scroll bugs
  in the issue list are gone:
  - In **tree view**, `AppFooter` no longer bleeds into the last rows.
    `<IssueTreeView>` now lives in a `.issue-tree-wrap` scroll container
    mirroring the flat table's `.issue-table-wrap` pattern; the
    `.tree-active` `overflow: visible` opt-out rules on
    `.issue-list-root` / `.issue-list-main` are dropped.
  - With the **side panel open in unpinned (floating) mode**, the list
    behind the panel stays scrollable via wheel / trackpad. Previously
    the transparent full-viewport `.sp-backdrop` intercepted wheel
    events and — being `position: fixed` — terminated the scroll chain
    at the viewport (`<html>` is `overflow: visible`). Now a
    `@wheel.passive` handler on the backdrop forwards the scroll to
    the element visually beneath it, preserving the existing
    click-to-close behaviour.

## [1.2.0] — 2026-04-20

### Added

- **Soft-delete (Trash) for issues** — ACME26-639. `DELETE /api/issues/{id}`
  now moves the issue to a Trash instead of hard-deleting. Child tasks
  cascade into the Trash too; `issue_relations` (groups / sprint /
  depends_on / impacts) stay intact so a later Restore re-attaches them
  automatically.
- `POST /api/issues/{id}/restore` — admin-only; clears `deleted_at` on
  one issue. Children stay in Trash — restore is deliberately explicit.
- `DELETE /api/issues/{id}/purge` — admin-only; hard-deletes a trashed
  issue (cascades comments, history, tags, time_entries, attachments,
  issue_relations). Two-step flow: must be in Trash first.
- `GET /api/issues/trash` — admin-only list of soft-deleted issues.
- **Settings → Trash** now lists deleted issues alongside deleted users
  and projects, with Restore and "Delete forever" buttons.
- `issues.deleted_at` (TEXT NULL) and `issues.deleted_by` (INTEGER NULL)
  columns with an `idx_issues_deleted_at` index (migration 66).
- 9 handler tests covering delete / restore / purge / cascade / relation
  survival / cross-project leak protection.

### Changed

- Every user-facing issue query (list, tree, recent, search, sprint,
  reports, aggregation, CSV export, cross-project list, distinct
  cost_unit / release, FTS, portal) now filters `deleted_at IS NULL`.
  Trashed issues only appear via the Trash endpoint.
- `GET /api/issues/{id}` and `PUT /api/issues/{id}` return `404` for
  trashed issues — restore first, then edit.
- Issue delete confirmation dialogs now say "Move to trash" and mention
  that child tasks are moved too and the action is recoverable from
  Settings → Trash.

### Notes

- Protection applies to **new** deletions only. Any issue hard-deleted
  before this release remains unrecoverable.

## [1.1.1] — 2026-04-20

This release bundles the per-project access-control feature with the
follow-on audit fixes and Docker/CI repairs. No `v1.1.0` tag was cut —
the feature landed on `main` and shipped together with the hotfixes
under `v1.1.1`.

### Added

- **Per-project access control.** Every user now has a per-project
  access level (`viewer`, `editor`, or `none`) stored in a new
  `project_members` table. Admins bypass. Members default to editor on
  every non-deleted project. External users get no access until granted
  explicitly. Access changes are written to a new `access_audit` log.
- **Settings → Permissions** tab renders the capability matrix
  (viewer vs. editor vs. admin) straight from the backend, so the
  documentation stays in lockstep with the code.
- **Settings → Users → Access** button opens a per-project matrix
  editor (`viewer` / `editor` / `none`) for any user, backed by the new
  `GET/PUT/DELETE /api/users/{id}/memberships` endpoints.
- `GET /api/permissions/matrix` — public to logged-in users for UI
  rendering.
- `GET /api/access-audit` — admin-only audit trail of grant / update /
  revoke events.
- Backend helpers: `auth.CanViewProject`, `auth.CanEditProject`,
  `auth.AccessibleProjectIDs`, `auth.WithAccessCache`. Frontend store
  exposes `canView()` and `canEdit()` helpers plus a hydrated
  `accessibleProjects` map.
- Per-project chi middlewares: `RequireProjectView`,
  `RequireProjectEdit`, `RequireIssueAccess`, `RequireAttachmentAccess`,
  `RequireTimeEntryAccess`, `RequireCommentAccess` (and their `Edit`
  counterparts). 404 on no-view; 403 on view-only; bypasses for
  orphan sprints.

### Changed

- `/api/auth/login`, `/api/auth/me`, and `/api/auth/totp/verify` now return a
  `{ "user": {...}, "access": {...} }` envelope. The `access` field
  hydrates the frontend's per-project permission cache in a single
  round-trip.
- `CreateUser` (internal roles) and `CreateProject` auto-seed editor
  rows in `project_members` so internal users never lose visibility on
  projects they didn't pre-exist.
- Cross-cutting list endpoints (`/api/projects`, `/api/issues`,
  `/api/issues/recent`, `/api/cost-units`, `/api/releases`,
  `/api/search`, `/api/users/me/recent-projects`) are now filtered by
  the caller's accessible project set.
- `ListIssueRelations` redacts the target title/key (`"RESTRICTED"`)
  when the relation's target lives in a project the caller can't view.

### Removed

- `user_project_access` table. Migration 65 drops it after a
  safety re-insert into `project_members`.

### Fixed

- 10 critical audit findings around access control enforcement
  (PAI-6..PAI-15) — see commit `899dd09`.
- Test suite green after audit fixes; migration test suite sped up.
- Dockerfile: copy `VERSION` and `docs/` into the SPA build stage so
  `__APP_VERSION__` and in-app docs links resolve in the built image.
- CI: sync `VERSION` from the git ref before the docker build so
  tagged images (`v1.1.1` → `1.1.1`) and main-branch images
  (`<base>-dev+<sha>`) have accurate version strings.

## [1.0.0] — 2026-04-19

### Initial release

PAIMOS v1.0.0 is the first public release. It provides a self-hosted
project management system with first-class support for tracking both
humans and AI agents as participants.

**Core features**

- Hierarchical issues (epic → ticket → task) with parent-child relations
- Rich issue metadata: status, priority, assignee, cost unit, release,
  dependencies, impacts, tags, attachments, comments, history
- Sprints, accruals, cost units, releases
- Per-user time tracking with inline timers and billing lifecycle
- Full-text search with partial issue-key matching
- Custom views (saved filter + column sets), per-user view ordering
- Admin panel: users, projects, tags, integrations
- External-user portal with read-only projects + acceptance workflow
- PDF delivery reports (Lieferbericht) with configurable column layout

**Security**

- Session cookies (HttpOnly, SameSite=Lax, optional Secure)
- TOTP 2FA with QR setup, reset, disable
- API keys (`paimos_` prefix, sha256-hashed storage)
- Magic-link password reset with 60-minute token TTL
- Rate-limited auth endpoints (login / forgot / reset / totp-verify)

**Integrations**

- Jira import (project list, field mapping, issue relations)
- Mite time-tracking import (DE/AT; user mapping via note field)
- CSV import/export (per-project and cross-project)

**Branding & configuration**

- **Live-editable branding** via admin-only **Settings → Branding** tab:
  edit product name, tagline, website, page title, full colour palette,
  upload custom logo + favicon — all applied live with no restart.
- Admin API: `PUT /api/branding`, `POST /api/branding/logo`,
  `POST /api/branding/favicon`. Public `GET /brand/<filename>` for
  pre-auth login-page assets (SVG served with restrictive CSP).
- Runtime branding config at `$DATA_DIR/branding.json`, plus
  `branding-<slug>.json` for multi-brand installs.
- Operator-configurable identity via `BRAND_*` env vars (product name,
  company, website, TOTP issuer, email from, API key prefix, DB filename,
  MinIO bucket — see `docs/CONFIGURATION.md`).
- Optional MinIO-backed attachments (graceful-disable when unset).
- Optional SMTP for password-reset emails (dev mode logs links to stdout).

### Provenance

PAIMOS is forked from an internal prototype and published under AGPL-3.0-only.
All upstream branding, CI, and deployment infrastructure have been removed.
Git history starts fresh with this release.
