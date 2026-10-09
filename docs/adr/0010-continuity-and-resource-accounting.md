# ADR 0010 — Source-bound continuity and conservative resource accounting

Status: engineering implementation, 0.9.0-dev, 9 October 2026.

## Continuity authority

Compaction archives only old complete canonical conversation blocks. Immutable original document, exact canonical archive and deterministic preview proof are retained. The kernel compares the authoritative-state digest before and after transformation and validates active archive markers against their source proof. Goal/spec, raw intent, policy, budget, candidate, plan, checks and approvals cannot be derived from a summary.

Human inspection pages the retained canonical archive. Model hydration returns a separately identified canonical projection without opaque provider/model continuation metadata. Exact source digest and projection digest are distinct. This does not assert that a deterministic preview has the semantic quality of a reviewed model summary.

Source pins contain bounded exact captured byte ranges, candidate/source digests and stable IDs. Model switch requires a complete quiescent boundary and preserves the provider/account/endpoint/privacy lock. Old profiles remain available to validate old charges. New profile compilation and task/global token/resource preflight precede commit. No inference is made by the switch command.

User continuity commands have full payload digest, task sequence and durable command ID dedup. Rejected commands do not first publish a recovery event. At a valid quiescent user revision the reducer can renew the owner generation in the same event; unfinished input/token/resource risk forbids this path.

## Global resource ledger

A new task enrolls in immutable store-wide currency, resource limits and operator price catalog. A single SQLite transaction applies all resource and token changes. Replay performs the same global admission. Mixing unaccounted historical tasks with a new ledger is rejected; this release does not invent historical money/CPU/disk usage or implicitly migrate an old store.

Money uses integral micro currency units, versioned input/output rates per million tokens and upward rounding. Paid model without catalog entry is denied before task creation; a switch also checks the new entry. Prices are user/operator declarations, not a provider billing invoice. Local/fixture/ChatGPT plan API spend is zero in this ledger; subscriptions, electricity and external provider compute are not included.

CPU and disk are **conservative allocation charges**, not measured CPU/GPU/physical consumption. Native operations reserve bounded kernel time and, when a check runtime is configured, its CPU/PID/scratch envelope. PID capacity is released only after the existing broker quiescence path has returned. Physical retained archive work also has a size quota. Metadata/managed-copy/full filesystem quotas and exact CPU backend observations require further backend conformance; they are not represented as completed by a vector type.

A missing result becomes UNKNOWN and retains every reservation across cancel, restart, replay and fresh restore. Actual bounded token overage remains charged. Control operations do not require spare token/money work budget.

## Physical control space

A private rooted reserve holds 32 MiB of random bytes. Writes are synced and OS physical allocation is checked; a sparse/short/replaced/hardlinked reserve fails closed. Work is admitted above a 64 MiB free-space low watermark with a replenished reserve. Bounded control/receipt writes can release part of the physical reserve; work cannot resume before replenishment. Exhaustion preserves the last durable pointer and rejects further writes.

This is an allocated-space guard on the exercised Windows/Linux filesystem profiles. It does not establish power-loss guarantees on arbitrary network/COW/rootless backends. A capacity measurement failure is a denial, not zero/free space.

## Explicit model UNKNOWN accounting

Only a quiescent user command bound to the current sequence and exact UNKNOWN model request can choose `ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT`. The user event atomically charges **all** reserved tokens/money/CPU/disk/time as `OPERATOR_ASSUMED_UPPER_BOUND`. It is not provider-reported usage and cannot accept a response, mutate candidate/goal or upgrade quality. A cancelled task remains cancelled. An unknown native process cannot use this command.

Receipts bind the original UNKNOWN document, canonical request/profile, command digest, full upper-bound assumption and paired token/resource charge. Backup/fresh restore validates these source objects. Kernel/model events cannot manufacture user accounting authority, reduce exposure to zero or reuse a command ID with changed content.

## Scope still controlled by the execution plan

This ADR records the implemented C01/C02 contracts. It does not close all of FR-09/16/17/18 or production gates. Paired real-model continuation quality, broader provider/privacy policy changes, backend CPU/disk/child conformance and remaining C03-C11 packages are evaluated separately in `docs/KEYLESS_EXECUTION_PLAN.md`. Old 0.8 PRD analysis remains a historical baseline, not a new score.
