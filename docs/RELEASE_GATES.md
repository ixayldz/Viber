# Release kanıt kaydı

PRD §28 family kimlikleri sabit. Fixture yalnız çalışıp beklenen davranış gözlenince PASS; unit/spike tüm family'yi kapatmaz. Bu kayıt 0.2.0-dev offline engineering profili içindir.

| Family | Guard | Uygulanan kanıt / eksik taraf | Tam release |
|---|---|---|---|
| 1 | Partial write crash | Immutable blob-before-pointer; fresh stage/manifest publication; tam boundary kill suite eksik | PARTIAL |
| 2 | Lost response/unknown effect | Durable intent+reserve, usage unknown restart/restore'da korunur; blind retry yok | PARTIAL |
| 3–5 | Candidate/env/stale edit | Candidate/index/ignore/read-set binding; source edit resume'ı durdurur; live merge yok | PARTIAL |
| 6 | Steering | Saf input barrier/epoch reducer; aktif owner IPC/steering revision eksik | PARTIAL |
| 7–11 | Lease/budget/process/FS isolation | Offline Docker readonly/non-root/no-network/timeout cleanup; resource/fencing/escape matrisi eksik | PARTIAL |
| 12–13 | Forged PASS/check weakening | Model/stdout PASS kalite yükseltemez; protected observer/check closure eksik | PARTIAL |
| 14–15 | Index/partial model stream | Native Git index v2/v3/v4/SHA-256/worktree; incomplete response reddi; SSE eksik | PARTIAL |
| 16 | Context overflow | Bounded preflight/transport/native tool limits; full compiled manifest eksik | PARTIAL |
| 17 | Compaction policy | Compactor yok | NOT_IMPLEMENTED |
| 18 | Privacy fallback | Restriction intersection, no remote fixture loop, redirect/proxy deny; full lineage eksik | PARTIAL |
| 19 | Plugin isolation | Plugin runtime yok | NOT_IMPLEMENTED |
| 20–21 | Corruption/two owners | Journal/projection/dedup receipt hash/replay + OS lock; owner IPC eksik | PARTIAL |
| 22–24 | Restore/reconnect/telemetry | Store fresh restore ve opt-in changeset export; workspace restore/reconnect/delete yok | PARTIAL |
| 25–28 | Guards/origin/integrity/delivery | Pure predicate + frozen artifact + scoped limited delivery; protected verifier eksik | PARTIAL |
| 29 | Clock/lease | Request expiry kontrolü var; backend lease/fencing conformance yok | NOT_IMPLEMENTED |
| 30–32 | Owner/children/pause/dedup | Lock/generation, Docker cleanup, native pause/cancel; bound one-shot approval/dedup; supervisor eksik | PARTIAL |
| 33–34 | Reserve/delete/backup | Offline model reserve/settle, unknown tutulması, CAS+SQLite backup; control reserve/GC/tombstone/migration eksik | PARTIAL |
| 35–36 | Sequence/replay/intent | Current/historical replay, task/global cursor ayrımı, raw input closure, event paging | PARTIAL |
| 37 | Independent evaluator | Bağımsız benchmark/pilot yok | NOT_IMPLEMENTED |
| 38 | Host Git/OSC/secret output | Capture Git executable çalıştırmaz; OSC escaped; provider error secret redaction | PARTIAL |

**A gate CLOSED. B preview CLOSED. Stable A–D CLOSED.** Engineering özelliklerinin çalışması kapıları açmaz. Gerçek iki remote+bir local endpoint kabulü kullanıcı tercihiyle ertelendi; fixture sonucu bu kabulün yerine geçmez. Tam sandbox, protected verification, store lifecycle/retention/privacy, IPC, platform packaging ve bağımsız evaluator kanıtları zorunludur.
