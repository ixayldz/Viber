# ADR 0008 — Journal-derived store-wide token work ledger

Status: Accepted for 0.7 engineering profile, 8 October 2026.

Model intent and token reservation must commit together. TokenAccount is an optional TaskState projection; TokenMutation is a required kernel-owned event payload extension on TaskCreated/SessionRecorded. Pure reducer validates operation/spec/policy/generation/request/profile bindings, single unresolved operation and reservation status. Store admission aggregates all task accounts inside the same SQLite transaction as journal/payload/projection/dedup. Full replay revalidates the same global bounds; no independently mutable counter table is authoritative.

The first new task establishes immutable store input/output work limits; later tasks inherit them. Task limits also apply. Failed admission leaves journal, step/cursor/reservation unchanged and stops before dispatch with WAITING_RESOURCE. Command ID dedup returns the historical receipt; settle is not charged twice. Bounded observed usage above an upper bound is still charged, preventing a rejected receipt from erasing accounting. Subsequent admissions stop. UNKNOWN retains its upper bound across cancel/restart/restore; unsupported release/retry cannot erase risk.

Every retained reservation binds canonical request/profile and a settled usage raw receipt. Load validates model/context/output/conservative upper bound, receipt availability and exact usage. Fixture-reported and provider-reported sources are distinct; neither is a trusted task quality verdict. Backup validates historical resource events/documents and fresh restore preserves all accounting.

Global work token limits do not fund/cap control commands. Cancel/receipt/recovery remain independent of exhausted token work budget. This is not proof of physical control disk reserve, monetary pricing/billing, child allocation, CPU/disk quotas, cross-store accounting or provider usage accuracy. No UNKNOWN reconciliation/release capability is exposed.

Legacy tasks lack authoritative global token history. Their read/resume/backup contract remains unchanged; new ledger tasks require a fresh store. Mixing tracked/untracked tasks or changing limits is rejected, instead of assuming missing historical usage is zero. SQLite schema 2 is unchanged; new typed journal/projection fields make older strict binaries fail closed before writable recovery. No implicit DB migration, altered historical task verdict or downlevel resume support is introduced.
