# Repository audit and recovery plan — 13 October 2026

## Source and evidence boundary

The audit began at local `main` revision `1765582d9fbcefd338eb9a8acd69f60c549ace24`. The two commits made during the earlier handoff are `18188eab07ec25ad81a852d179cf8cb8f73b80b6` (explicit project-memory copy, captured retrieval reports, scorer, onboarding) and `1765582` (support, preview and GC physical quota). Both were ahead of remote `main`, `af70426fe61756c63d8722b6221fe31f26a3f087`. They had local Windows evidence, but no hosted exact-source CI or public packages. The earlier 16-job PASS belongs to `af70426` and cannot be inherited.

The review covered the complete intervening file inventory and diff; the PRD requirement, acceptance and release sections; the changed production paths for protection, memory copy/privacy lineage, retrieval/scoring, quota writers, onboarding/ConPTY and distribution compatibility; their test contracts; and active installation/release documentation. Separate reviews checked memory, quota, retrieval, PRD/docs and the remaining owner quota integration design. This is a targeted engineering/security audit of the handoff and its dependencies, not a claim that every line of every historical helper or every possible attack has been reviewed.

Existing unrelated user deletions and the modified `PRD_STATUS_ANALYSIS.md` are excluded from this work. Neither an old status percentage nor a large test count establishes PRD completion.

## Confirmed defects found before resuming feature work

| ID | Priority | Trigger and consequence | Required correction |
|---|---|---|---|
| A01 | High | GC publishes `complete.json`, then dies before allocation Commit. Exact retry sees completion and skips settlement, leaving WRITING charged indefinitely. | Reconcile the exact matching own reservation even when the completion record already exists; retained exact retry must be idempotent. |
| A02 | High | Support/preview dies after Begin and before output creation. Retry treats the same-root reservation as an enclosing parent and never settles it. | Distinguish own-root recovery from a proper enclosing transaction and settle only the writer's own allocation. |
| A03 | High | Path-only reservation adoption does not bind writer/family/class/request/envelope. A changed request or unrelated reservation may authorize output under an earlier upper bound; GC may also commit an outer reservation. | Match own allocation authority and upper bounds before writing; require valid ancestor coverage and preserve the ancestor's settlement ownership. |
| A04 | High | Lightweight task Load no longer checks `Digest(Protection) == Spec.ProtectedOrigin`; altered metadata can load and misleadingly advertise check protection. Full use-point verification still re-derives bytes, so this is not evidence of a demonstrated false VERIFIED final. | Restore metadata digest, check-plan, source identity and requirement mapping validation without hydrating denied/purged source blobs. |
| A05 | Medium | Go captures valid search arguments up to 64 KiB but the paired Python importer rejects above 8 KiB. | Share the 64 KiB contract and exercise a valid larger import. |
| A06 | Medium | Scorer stats a bounded file, then performs an unbounded read; concurrent growth can exceed its memory profile. | Read at most limit+1 from a regular opened file, reject growth/oversize, and keep exact-byte hashing. |
| A07 | Medium | A list/object dataset repository ID is used in a set before validation and raises an uncaught exception. | Validate structure and ID type before hashing/indexing; malformed input must fail through the declared error contract. |
| A08 | Medium | ConPTY constructor spawns its helper and then fails while waiting for READY. The caller never receives the terminal handle for cleanup. | Constructor owns failure cleanup: close input for helper/child shutdown, bounded wait and pipe cleanup; do not infer child absence merely from killing a helper. |
| A09 | Documentation | README/INSTALL/CAPABILITIES point to older public packages, and live apply is described as an unconditional platform blocker. | Point to the verified public engineering release, distinguish unreleased source, and require exclusivity evidence for advertised LIVE_APPLY profiles. Preserve valid candidate/patch fallback requirements. |
| A10 | High, inherited | A completed evaluation report physically measures `owner/**`; privacy `ExcludedStore` does not exclude physical charge. While WRITING it suppresses nested child accounting, and after settlement a reopened child can import the same files as new receipts. | Implement an atomic, non-overlapping owner/report handoff. This predates the two handoff commits and remains open; a plan is not a fix. |
| A11 | High, inherited | Fresh privacy metadata precedes ledger attachment; later record/checkpoint/migration writers use independent caps/probes. Per-instance native Docker capacity counts currently present IDs, rather than a durable global lifetime admission ledger. | Implement authority-bound bootstrap/mutable-tree accounting and persistent engine identity charges. Preserve UNKNOWN engine byte use and existing secondary bounds. These remain open. |
| A12 | Medium | Fresh memory-copy admission calls full Load before checking context.read/edit; Load internally hydrates memory reference bodies for validation. No disclosure from this path was demonstrated, but the stated policy-before-hydration invariant is not met. | Perform an exact journal-bound metadata/policy preflight before full loading or hydrating source/target memory references; keep full integrity validation after admission. |
| A13 | Medium | Offline source enumeration rejects symlink directories but does not reject Windows junctions. | Reject both linked directory types before traversal and retain exact frozen inventory checks. |

The corrections and their regression evidence must be recorded separately from these original findings. An implemented fix is not a hosted CI PASS until the corrected immutable revision is tested.

Support recovery has an additional limitation: its canonical report contains a live available-disk observation. After reservation/reopen, that observation can legitimately change. Exact-envelope matching then refuses the retry before creating output and retains the earlier charge. That is safer than adopting a changed payload, but does **not** close complete support crash recovery. A durable original payload/intent is required; merely excluding changing fields from the request digest would weaken exact-byte binding. The focused reproduction records this distinction.

The quota review also found inherited lifecycle gaps: managed-copy privacy purge does not settle each physical free receipt, legacy managed copies are not imported by current archive-only upgrade attachment, and path-only enclosing-reservation callers remain outside the three repaired writers. GC admission correctly refuses plans beyond the remaining CONTROL-entry budget (`objects+4`; default 1,024 entries), but bounded batching/maintenance-record retirement is not implemented by this patch. None of these gaps is an API credential dependency.

## Memory and retrieval limits that are not silently widened

Explicit memory copy is a useful new slice: same-owner store, current project/policy, exact active user assertion, durable PREPARED lineage before target bytes, typed target journal mutation and ACTIVE receipt. It is advisory, not task or verification authority. Copy recovery at the target-journal-before-ACTIVE boundary needs direct close/reopen/retry evidence, beyond the successful copy/restore/deletion test.

Automatic context remains SAME_TASK_ONLY plus explicitly copied origin scope. A later conflicting assertion in a third task can appear CONFLICT in project-wide operator search while the automatically loaded copied item remains ACTIVE within its narrower scope. Reading all third-task bodies would violate the current bounded/privacy contract. A bounded metadata-only conflict revision index is a separate design requirement if project-wide automatic conflict freshness is advertised; this audit does not equate a scoped ACTIVE label with a global consensus claim.

Captured retrieval reports bind retained call/reply bytes to current candidate, current admitted policy, source inventory, spans and task cursor. The offline scorer checks supplied frozen labels/source bytes and reports missing/partial attempts. Imported build, time, usage and human-label provenance remain declarations. `release_evidence=false`, `adoption_allowed=false`, and `quality_claim=false` must remain false. Synthetic self-tests are not independent relevance, model-quality or hardware performance measurements.

## Distribution evidence checked during this audit

[Public engineering release af70426](https://github.com/ixayldz/Viber/releases/tag/engineering-af70426fe61756c63d8722b6221fe31f26a3f087), release ID `410937496`, follows [core CI 38294014619](https://github.com/ixayldz/Viber/actions/runs/38294014619), all 16 jobs SUCCESS, and publisher `38298192596` SUCCESS.

A separate consumer downloaded all 11 assets without download authentication, checked the API sizes/digests and complete SHA256SUMS inventory, and performed nine actual GitHub attestation verifications restricted to the exact repository, workflow, main ref and source SHA. All three attested native installation reports matched their archive bytes and actual downloaded baseline `90bc97ebc9318e3542ffbc2507b8cb346dae69e5` archives for upgrade/rollback/upgrade. Local report: `.cache/public-readback-af70426-audit/anonymous-acceptance.json`, PASS.

This is public download/provenance/install-report readback. The consumer did not execute downloaded binaries. Native execution belongs to the attested CI reports. It does not establish certificate signing, live model acceptance, independent pilot acceptance or stable readiness. Neither `18188ea` nor `1765582` is in that public package.

## PRD remaining work: API keys are not the only dependency

The PRD makes A–D the first stable scope; E–G remain later experiments. [Release gates](RELEASE_GATES.md) retain family-level PARTIAL status because a passing fixture is not the full family. [Delivery plan](KEYLESS_DELIVERY_PLAN.md) contains the implementation order and acceptance obligations.

| Scope | Remaining work | Dependency type |
|---|---|---|
| FR-06/13/26, NFR-01/07/09 | Complete interrupted lifetime, catalog/legacy/UNKNOWN, restore/deletion/checkpoint/migration and owner-retirement closure across advertised profiles. | Keyless software and native acceptance |
| FR-07/08/09, NFR-02/05/06 | Whole writer quota, process/fence lifetime, hostile backend interval and authority exhaustion/reopen matrix. | Keyless software and native acceptance |
| FR-11/21, NFR-03 | Protected discovery/authorized revision/full dependency closure, V0–V5 boundaries, read-only verification interval/quiescence, fresh delivery verification. | Keyless software and native acceptance |
| FR-12/19/22/23/25, NFR-04/08/10 | Complete reconnect/restore/retention-gap, terminal paste/resize/control, stale owner/child and cursor/platform matrix; reference performance. | Keyless software; reference hardware for measurements |
| FR-14–18/27, NFR-06/09/12 | Bounded continuity and required obligation preservation; source-bound supported language outline; actual independently measured effectiveness. | Keyless software; independent dataset/model/hardware acceptance |
| FR-24 | Actual ChatGPT subscription-login first task and actual local Ollama model journeys. | External account/local model; **no API key required** |
| FR-24 | Actual direct OpenAI/Anthropic endpoint protocol, usage, rate/quota, interruption/cancel/charge and model-quality conformance. | API credentials and live endpoint allowance |
| Packaging/release | Windows signing identity, Apple Developer ID/notarization, independent users/pilot and reference performance. | External identity/people/hardware; **not API-key work** |
| E: FR-28–32 | General resolved language graph/runtime coverage, retrieval/history/generation experiments and independent ablations. | Later keyless software plus real measurement inputs |
| F: FR-34–37 | Model AgentLease/TTL/generation/mailbox, parent budget, isolated writer integration and measured routing/external worker. Deterministic read analyzers do not fulfill this scope. | Later keyless software; real model acceptance separately |
| G: FR-39–42 | Evaluated skills/learned policy, authenticated team policy/remote execution and scoped opt-in data export acceptance. | Later keyless software and external deployment/evaluation inputs |

It is inaccurate to say only API-key work remains. A single percentage would mix partial implementation, incomplete acceptance and later experiments without a preregistered scoring denominator. The first stable gate remains CLOSED; engineering releases remain installable previews.

## Execution order after the audit

Local correction status: A01/A03/A04/A05/A06/A07/A08/A09/A12/A13 have implemented changes and affected regression evidence in [VALIDATION](VALIDATION.md). A02 has exact-own-allocation settlement fixes but remains incomplete for full interrupted support/partial-output recovery; changed numeric payloads are conservatively refused. A10/A11 and the wider quota lifecycle remain open. No corrected source is considered hosted-accepted by this local status.

1. Fix A01–A08 and preserve bounded/policy-denied hydration behavior. Review exact changes and run affected crash/reopen, quota, protection, scorer and terminal regressions.
2. Correct A09 and append source-specific evidence. Commit the audit/fixes with the two earlier commits preserved, push normally, and run mandatory exact-source CI. Fix a red CI before publishing another package.
3. Finish the remaining shared quota integration through an explicit stage-transfer and owner-bootstrap handoff design. Restore cannot settle a reservation bound to the old stage path after rename; an evaluator outer WRITING reservation cannot safely remain open forever or become a retained root that double-counts child imports on reopen. See [owner quota handoff design](OWNER_QUOTA_HANDOFF_DESIGN.md).
4. Complete privacy metadata/control/checkpoint/migration quota and native fence lifetime under their current authority/control bounds. [Privacy and fence quota design](PRIVACY_AND_FENCE_QUOTA_DESIGN.md) identifies bootstrap, self-accounting, mutable-tree and durable identity requirements. Unknown engine byte use must remain UNKNOWN, not zero.
5. Close A–D verification, lifetime and supervisor acceptance before advancing optional E–G experiments. Execute actual account/local-model/signing/pilot acceptance only with its required inputs and existing offline preference respected.
6. Publish only after the new immutable revision passes its required foundation and package checks; perform exact public readback again. Keep engineering and stable readiness distinct.
