# Release kanıt kaydı

PRD §28 family kimlikleri sabit. Fixture yalnız çalışıp beklenen davranış gözlenince PASS; unit/spike tüm family'yi kapatmaz. Bu kayıt 0.5.0-dev offline engineering profili içindir.

| Family | Guard | Uygulanan kanıt / eksik taraf | Tam release |
|---|---|---|---|
| 1 | Partial write crash | Immutable blob-before-pointer; fresh stage/manifest publication; tam boundary kill suite eksik | PARTIAL |
| 2 | Lost response/unknown effect | Durable intent+reserve, usage unknown restart/restore'da korunur; blind retry yok | PARTIAL |
| 3–5 | Candidate/env/stale edit | Candidate/index/ignore/read-set binding; source edit resume'ı durdurur; live merge yok | PARTIAL |
| 6 | Steering | Raw blob-before-barrier, aktif owner pause, bound spec/epoch revision, eski approval invalidation; semantic provider/fencing matrisi eksik | PARTIAL |
| 7–11 | Lease/budget/process/FS isolation | Offline Docker readonly/non-root/no-network/timeout cleanup, exact mount source/image/argv/namespace/scratch binding; resource/fencing/escape matrisi eksik | PARTIAL |
| 12–13 | Forged PASS/check weakening | Model/stdout PASS kalite yükseltemez; mutation öncesi operator origin/explicit closure guard ve frozen binding var; full closure review/authorized check revision/trusted observer eksik | PARTIAL |
| 14–15 | Index/partial model stream | Native Git index v2/v3/v4/SHA-256/worktree; incomplete response reddi; SSE eksik | PARTIAL |
| 16 | Context overflow | Mandatory full protocol/spec/policy preflight, durable manifest/request closure, exact-byte source/list/search paging; provider tokenizer/compaction eksik | PARTIAL |
| 17 | Compaction policy | Compactor yok | NOT_IMPLEMENTED |
| 18 | Privacy fallback | Restriction intersection, no remote fixture loop, redirect/proxy deny; full lineage eksik | PARTIAL |
| 19 | Plugin isolation | Plugin runtime yok | NOT_IMPLEMENTED |
| 20–21 | Corruption/two owners | Journal/projection/dedup receipt hash/replay + OS lock + SID/UID/PID peer-auth RPC; hostile-principal platform acceptance eksik | PARTIAL |
| 22–24 | Restore/reconnect/telemetry | Store fresh restore ve opt-in changeset export; stale owner descriptor reconciliation; workspace restore/stream reconnect/delete yok | PARTIAL |
| 25–28 | Guards/origin/integrity/delivery | Pure predicate + frozen artifact + scoped limited delivery; durable typed origin, tree/absence check weakening guard, revision/recovery/backup closure; full interval protected verifier eksik | PARTIAL |
| 29 | Clock/lease | Request expiry kontrolü var; backend lease/fencing conformance yok | NOT_IMPLEMENTED |
| 30–32 | Owner/children/pause/dedup | Lock/generation, Docker cleanup, native pause/cancel; bound one-shot approval ve active command dedup; Ctrl+C pause; tam supervisor/children fencing eksik | PARTIAL |
| 33–34 | Reserve/delete/backup | Offline model reserve/settle, unknown tutulması, CAS+SQLite backup; control reserve/GC/tombstone/migration eksik | PARTIAL |
| 35–36 | Sequence/replay/intent | Current/historical replay, task/global cursor ayrımı, raw input closure, event paging | PARTIAL |
| 37 | Independent evaluator | Bağımsız benchmark/pilot yok | NOT_IMPLEMENTED |
| 38 | Host Git/OSC/secret output | Capture Git executable çalıştırmaz; OSC escaped; provider error secret redaction | PARTIAL |

**A gate CLOSED. B preview CLOSED. Stable A–D CLOSED.** Engineering özelliklerinin çalışması kapıları açmaz. Gerçek iki remote+bir local endpoint kabulü kullanıcı tercihiyle ertelendi; fixture sonucu bu kabulün yerine geçmez. Tam sandbox, protected verification, store lifecycle/retention/privacy, tam IPC/process supervisor conformance, platform packaging ve bağımsız evaluator kanıtları zorunludur.

0.5 iş kanıtı: P02'nin origin/explicit closure/proposal guard/frozen binding dilimi uygulandı; P03'ün broker source/image/argv/namespace/scratch binding açığı düzeltildi. P02/P03 tam DONE değildir. Test adları `TestF13...`, `TestF26...` ilgili family bağlantısını gösterir; readonly protected runner/discovery/observer/check revision ve tüm adversarial OS matrisi kapanmadan family PASS olmaz. Güncel [plan v2](IMPLEMENTATION_PLAN.md) tüm FR/NFR ve F01–F38/X01–X10 eşlemesini taşır.