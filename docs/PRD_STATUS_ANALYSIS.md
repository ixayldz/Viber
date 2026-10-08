# PRD 1.1 durum ve eksik analizi

Tarih: **8 Ekim 2026**. Kod: **0.6.0-dev**, `f4f1d10f169650c82b19c6d99383e89887b6339d`. Yetkili sözleşme: [prd.md](../prd.md), özellikle §8, §11–23, §26–29. Bu belge bu kod revision'ının değerlendirmesidir; otomatik güncellenen bir dashboard değildir.

## 1. Sonuç

**İlk kararlı A–D ürün kapsamındaki mühendislik ilerlemesi yaklaşık %40,2. Uygulama üretime hazır değildir.** Çalışan ve test edilmiş offline temel var; günlük kullanıcıların serbest isteklerini gerçek modellerle çözen, bağımsız doğrulanmış değişiklikleri güvenle teslim eden ürün henüz yok.

| Gösterge | Sonuç | Açıklama |
|---|---|---|
| A–D gereksinim ilerlemesi | **1.650 / 4.100 = %40,24** | Aşağıdaki açık rubric ile kısmi implementasyon puanı |
| Bütün A–G kataloğu | **1.650 / 5.400 = %30,56** | E–G deneysel gereksinimleri de dahil |
| Tam DONE A–D gereksinimi | **0 / 41** | Bütün uygulanabilir kapsam ve kabul kanıtı kapanmış değil |
| Kayıtta bütün kapsamı kapanan kritik family | **0 / 38** | PARTIAL/NOT_IMPLEMENTED; A–D ve koşullu F/G alt kapsamları ayrıştırılmalı |
| Release | **A CLOSED; B preview CLOSED; stable A–D CLOSED** | Engineering binary var; üretim kabulü yok |

%40,24 ölçülmüş görev başarısı, bitmiş işçilik oranı, kalan süre/maliyet tahmini veya güvenlik garantisi değildir. Gereksinimlerin büyüklükleri eşit olmadığı için hassasiyeti sınırlıdır; pratik ifade **yaklaşık %40** olmalıdır. Production readiness ortalama puanla verilmez: kritik bir gate açık kaldığında release kapalıdır. “304 test geçti, dolayısıyla %100” sonucu çıkarılamaz.

## 2. Yöntem, kapsam ve hesap

İlk kararlı sürüm A–D'dir. FR-01–27 ile **FR-33'ün B dilimi** ve **FR-38'in D dilimi** dahil: 29 FR. NFR-01–12'nin ilk sürüme uygulanabilir korumaları dahil: 12 NFR. Toplam **41** eşit ağırlıklı katalog kalemi.

E–G ayrı deneylerdir; ilk stable için hepsini bitirmek gerekmez. Buna karşılık B'nin typed plan/dependency contract'ı “FR-33 F'de” diye veya D extension boundary'si “FR-38 G'de” diye kapsamdan çıkarılamaz. Kapalı worker/plugin capability'leri çalıştırılmaz; açıldığında ilgili kabul matrisi ayrıca kapanır.

| Puan | Ölçüt |
|---|---|
| 0 | İstenen çalışan davranış yok; yakın bir temel aynı özellik sayılmaz |
| 25 | Temel veya küçük alt dilim var; ana gereksinim eksik |
| 50 | Anlamlı ve test edilen sınırlı implementasyon var; geniş kapsam/integrasyon/kabul eksik |
| 75 | Gereksinimin büyük bölümü çalışıyor; önemli scope veya conformance boşlukları var |
| 100 | Bütün uygulanabilir kapsam, entegrasyon ve gerekli kabul kanıtı kapanmış |

Kod, gerçek CLI sınırları, test isimleri, [VALIDATION](VALIDATION.md) ve [RELEASE_GATES](RELEASE_GATES.md) birlikte değerlendirildi. Geçmiş PASS kaydı yeni çalıştırma diye sunulmadı. Unit/offline vector gerçek OS izolasyonu, production provider kabulü veya bağımsız ürün eval'i yerine geçmedi.

Hesap verisi: [PRD_STATUS_SCORE.json](PRD_STATUS_SCORE.json). FR **1.175/2.900 = %40,52**; NFR **475/1.200 = %39,58**; toplam **1.650/4.100**. **34** kalem kısmi implementasyon, **7** A–D kalem 0 puan; hiçbir kalem 100 değil. Tüm vizyonda 42 FR + 12 NFR = 54 kalem var; ek 13 deneysel FR 0 puanlı.

Bu bir mühendislik tahminidir. Başka ağırlıklandırma başka yüzde üretir; burada daha küçük özelliklere fazla, büyük kalan işlere az işçilik biçildiği iddia edilmiyor. Asıl release kararı gereken kanıtlar üzerinden verilir.

## 3. Gerçek kullanıcı yolculuğu

1. Go ile binary derlenir; doctor mevcut capability bildirimini verir.
2. Kullanıcı `run --offline --fixture` ile ham input/source/store/task açar.
3. Exact byte/hash bağlı immutable baseline/candidate oluşur.
4. Fixture canonical yanıtlar üretir; read/list/search çalışır, proposal güncel read-set/policy ile değerlendirilir.
5. Review modunda candidate mutation bağlı request/response onayı bekler.
6. `--allow-unverified` açıkça verilmişse sonuç `FINISHED / UNVERIFIED / SATISFIED`, exit **2** olur; required verification obligation açık kalır.
7. Kullanıcı status/diff/history inceleyebilir; exact changeset export, store backup/fresh restore/replay yapabilir.

**Bu yolculuk genel bir coding agent değildir.** Prompt'u değiştirmek prewritten fixture yanıtlarını yeniden planlamaz. Gerçek provider, shell/test araçları, trusted verification ve live apply bütünleşik ürün döngüsünde yoktur. SATISFIED burada sınırlı candidate-only teslim durumudur; istenen davranışın bağımsız doğrulandığını ifade etmez. [README](../README.md) bugünkü gerçek akışın kopyalanabilir kullanımını anlatır.

## 4. Tamamlanmış alt dilimler

| Çalışan alt iş | Maddi sınır |
|---|---|
| Strict canonical JSON, integer/digest/version guards | Bütün PRD authoritative nesne şemaları yok |
| SQLite WAL/FULL, tek owner lock, hash journal/projection, task/store sequence, dedup | Global ledger/control reserve, lifecycle GC/delete ve full platform crash matrisi yok |
| Saf reducer; ayrı execution/outcome/quality/fulfillment | Terminal task yeni attempt ve bütün domain state/work interval akışları yok |
| Scoped CAS, immutable manifest, blob-before-pointer ve fresh materialization | Host metadata guard'ı process isolation değildir |
| Native dirty Git/index/ignore capture; FILE/ABSENT/LISTING precondition | BEST_EFFORT; unsupported path/index biçimleri reddediliyor, live merge yok |
| Durable model/tool intent, receipt, UNKNOWN usage/effect rezervasyonu | Task bütçesi var; atomik global para/resource ledger yok |
| Tek işlem/task/spec/policy/candidate bağlı request/response | Complete supervisor/reconnect ve çok attempt UX yok |
| SID/UID/PID kontrollü local IPC; aktif pause/cancel/dedup | Foreground owner; kalıcı daemon/detach/attach değil |
| Raw steering barrier ve explicit additive scope revision | Gerçek provider semantic replanning/TUI queue yok |
| Exact native paging ve mandatory conservative context preflight | Byte üst sınırı; gerçek token profile/compactor değil |
| Mutation öncesi operator check origin/closure, weakening guard ve freeze binding | Operator repo baseline bağımsız oracle değil; test argv'si çalıştırılmıyor |
| Verification predicate ve kalite/delivery ayrımı | Trusted result producer/observer yok; VERIFIED kapalı |
| Pinned Docker readonly/nonroot/network-none/resource/timeout broker | Genel backend escape/egress/fencing conformance değil |
| Responses/Messages/Ollama HTTP/protokol kodu | Nonstream; CLI bağlantısı ve gerçek inference kabulü yok |
| Exact before/after, unified patch, report ve manifest export | Live apply/restore ve genel secret-safe export değil |
| SQLite+CAS backup, fresh restore ve historical closure/replay | Delete watermark 0; source identity otomatik taşınmıyor |
| Explicit v1→v2 migration, restore-validated backup, durable intent/commit/read-only recovery | Bütün version/update/downgrade/power-loss matrisi değil |

Bunlar alt iş kapsamındaki çalışan teslimlerdir; üst FR/NFR'nin tamamı veya stable PASS olarak işaretlenemez. Kod bağlantıları ve açık işler aşağıdaki tablolarda.

## 5. A–D işlevsel gereksinimleri

| ID / gereksinim / puan | Mevcut implementasyon | PRD'ye göre kalan | Kod / plan |
|---|---|---|---|
| FR-01 — Ham input/revision ve source-bound kriterler — **50/100** | Raw input CAS/source span, additive bound revision, boş niyet/kriter reddi. | Semantik scoping/constraint extraction, bağımsız goal omissions review. | [Kanıt alanı](../internal/agent/steering.go); P01/P02/P10 |
| FR-02 — Tek writer kernel ve command/event API — **75/100** | OS owner lock, saf reducer, sequence ve command dedup. | Bütün canonical nesne/event katalogları, çok task supervisor, target mutex ve conformance. | [Kanıt alanı](../internal/store/store.go); P01/P04/P14 |
| FR-03 — Dirty/staged/unstaged/untracked baseline — **75/100** | Native Git index/ignore ve kullanıcı byte'ları korunuyor. | Unsupported index/repo/path biçimleri, stronger capture ve geniş platform matrisi. | [Kanıt alanı](../internal/workspace/git.go); P06/P08 |
| FR-04 — İzole immutable candidate — **75/100** | Scoped CAS, immutable manifest, fresh materialization. | Executable candidate/backend lifecycle, writer freeze/process quiescence ve tam crash matrisi. | [Kanıt alanı](../internal/artifact/archive.go); P03/P06 |
| FR-05 — Read/write-set ve stale patch — **75/100** | FILE/ABSENT/LISTING preconditions, exact reads, stale proposal reddi. | Genel tool write-set, generated writes, filesystem race/authority boundary kabulü. | [Kanıt alanı](../internal/workspace/proposal.go); P06/P08 |
| FR-06 — Durable intent/attempt/receipt ve recovery — **50/100** | Intent/receipt, completed boundary recovery, UNKNOWN bloklama. | Gerçek model/process/external effect reconciliation, attempt identity ve retry matrisi. | [Kanıt alanı](../internal/agent/session.go); P04/P05/P10 |
| FR-07 — Process/FS/network/credential/resource sandbox — **50/100** | Gerçek Docker readonly/nonroot/network-none/resource/timeout developer profili. | Rootless/backend/version floor, bütün escape/egress/child/credential/race/fencing matrisi. | [Kanıt alanı](../internal/runner/docker.go); P03 |
| FR-08 — Capability gate, approval, epoch fencing — **50/100** | Policy intersection, bound approval, epoch/generation/input barrier. | Gerçek execution/publish sınırında backend fence, credential/config authority. | [Kanıt alanı](../internal/policy/policy.go); P03/P05/P16 |
| FR-09 — Global resource ledger — **25/100** | Task belge seviyesinde used/reserved token, step/tool/time alanları. | Store transaction global para/resource ledger, concurrent reserve, child allocation, price/usage lineage, control reserve. | [Kanıt alanı](../internal/agent/session.go); P05 |
| FR-10 — Lexical/path/range ve bounded paging — **75/100** | Exact read/list/search pages, coverage ve scoped cursor. | Genel artifact output paging/hydration ve bütün araç yüzeyine entegrasyon. | [Kanıt alanı](../internal/agent/native_pages.go); P06/P11 |
| FR-11 — Check/receipt/quality ve ayrı fulfillment — **50/100** | Kalite/delivery predicate, pre-mutation origin, weakening guard, frozen bindings. | V0–V5 trusted runner/discovery/observer, authorized check revision, goal review ve gerçek receipts. | [Kanıt alanı](../internal/verify/verify.go); P02/P07 |
| FR-12 — Lifecycle CLI ve JSONL — **50/100** | Run/status/diff/pause/resume/cancel/history/requests/respond ve exit codes. | Gerçek provider loop, production JSONL stream, terminal task yeni attempt ve complete resume. | [Kanıt alanı](../internal/cli/task.go); P10/P14 |
| FR-13 — Typed checkpoint ve deterministic replay — **75/100** | Persisted typed task state ve full historical checkpoint/replay integrity. | Work interval/DevelopmentEpoch, bütün domain state ve deletion/unavailable semantiği. | [Kanıt alanı](../internal/store/checkpoint.go); P04/P11 |
| FR-14 — Context preflight/compiler/manifest/prefix — **50/100** | Mandatory full intent/spec/policy/protocol, durable context/request manifest. | Provider token semantics, complete filtering/ranking/packing, stable prefix/cache/inclusion lineage. | [Kanıt alanı](../internal/agent/context.go); P11 |
| FR-15 — Snapshot/environment freshness — **25/100** | Whole source/index/ignore changes conservative invalidation. | Typed EnvironmentFingerprint, lock/toolchain/check/env dependencies, watcher/direct fallback, selective invalidation. | [Kanıt alanı](../internal/agent/session.go); P06/P12 |
| FR-16 — Bounded compaction — **0/100** | Compactor yok. | Typed constraints koruması, bounded narrative compaction, rejection/cost/reservation ve paired continuation eval. | [Kanıt alanı](../internal/context/compiler.go); P12 |
| FR-17 — Context/history pin/page ve explanation — **25/100** | Event/history paging, included/omitted context refs. | Pin/unpin, kullanıcı explain/why, context hydration/history inclusion UX. | [Kanıt alanı](../internal/cli/history.go); P11/P13 |
| FR-18 — Safe model switch ve privacy/model lock — **25/100** | Protocol continuation isolation ve policy gate temeli. | Runtime model lock/switch, complete tool boundary, token recompile ve current admissibility. | [Kanıt alanı](../internal/model/types.go); P09/P13 |
| FR-19 — Durable steering/pause/revision ve queue — **50/100** | Raw barrier, active pause, explicit bound additive revision. | Gerçek provider semantic replanning, process fence/drain matrisi, TUI composer/prompt queue. | [Kanıt alanı](../internal/agent/steering.go); P10/P15 |
| FR-20 — Autonomy ve bağımsız isolation — **50/100** | review/guided/auto native candidate admission, one-shot review approval. | Genel effect/preset sözleşmesi ve ayrı backend/isolation profile seçimi/kabulü. | [Kanıt alanı](../internal/agent/requests.go); P03/P10/P15 |
| FR-21 — Apply/restore ve delivery receipt — **25/100** | Candidate-only diff/export ve limited delivery binding. | Live apply/restore primitive, exclusive target access, three-way conflict, file receipts/crash recovery/reverification. | [Kanıt alanı](../internal/delivery/changeset.go); P08/P15 |
| FR-22 — TUI, @file, slash, diff/status — **0/100** | TUI yok. | Interactive composer/queue/why, keyboard/resize, escaped logs ve human workflow. | [Kanıt alanı](../internal/cli/cli.go); P15 |
| FR-23 — Doctor/onboarding/support matrix — **25/100** | Doctor/help, kaynak derleme ve yetenek bildirimleri. | Onboarding/config/credential UX, doğrulanmış destek matrisi, installer/update/native smoke. | [Kanıt alanı](../internal/cli/cli.go); P16/P19 |
| FR-24 — İki remote + bir local conformance — **50/100** | Responses/Messages/Ollama nonstream adapter ve offline/loopback fixtures. | SSE/partial/cancel/auth/rate/usage/retention full conformance ve gerçek endpoint kabulü. | [Kanıt alanı](../internal/model/http.go); P09/P19 |
| FR-25 — Secure IPC, supervisor, detach/attach — **50/100** | Peer-auth local owner IPC, active control, generation binding. | Background supervisor, detach/attach, cursor reconnect/gap resync, backpressure/orphan cleanup. | [Kanıt alanı](../internal/owner/owner.go); P14 |
| FR-26 — Retention/delete/export — **25/100** | Opt-in local changeset export ve backup. | Minimum retention/delete, tombstone/reachability/GC, shared refs, derived/managed backup purge, restore watermark. | [Kanıt alanı](../internal/agent/backup.go); P04/P16 |
| FR-27 — TS/JS/Python outline/symbols — **0/100** | Lexical arama var; outline değil. | Source-bound outline/symbol, extractor version/coverage ve unsupported fallback. | [Kanıt alanı](../internal/agent/tools.go); P17 |
| FR-33 — Typed plan DAG/dependency/mutex — B dilimi — **0/100** | Tek writer var; dependency plan değil. | Typed PlanNode, DAG validation, contract/dependency/mutex scheduler; F workers ayrı deney. | [Kanıt alanı](../internal/contracts/types.go); P06 |
| FR-38 — Pinned extension/effect sınırı — D dilimi — **0/100** | İç ModelAdapter interface var; extension runtime değil. | Version/effect/hash pinned extension hook, isolation/scoped RPC ve gerekli dar MCP boundary. | [Kanıt alanı](../internal/model/types.go); P16 |

Toplam **1.175 puan**. “Bir writer var” typed DAG; “adapter interface var” pinned extension; “backup var” retention/delete kabulü sayılmadı.

## 6. A–D kalite gereksinimleri

| ID / gereksinim / puan | Mevcut implementasyon | PRD'ye göre kalan | Kod / plan |
|---|---|---|---|
| NFR-01 — Fault injection'da veri koruma — **50/100** | Corruption guard, candidate publication ve gerçek subprocess migration crash tests. | Full filesystem/power-loss/first-write/apply/backend/external effect crash matrisi. | [Kanıt alanı](../internal/store/migration_test.go); P03/P04/P06/P08/P19 |
| NFR-02 — Eski authority admission reddi — **50/100** | Stale spec/policy/candidate/owner ve input barrier kontrolü. | Backend execution/integration fence, lease/clock domain, alias/process identity. | [Kanıt alanı](../internal/owner/owner_test.go); P03/P05/P14 |
| NFR-03 — False VERIFIED engeli — **50/100** | Model/stdout PASS kalite yükseltemez; predicate/origin tests. | Full interval read-only source/check, trusted channel/observer ve goal coverage. VERIFIED kapalı olması güvenli ama özellik tamamlanmış değil. | [Kanıt alanı](../internal/verify/verify_test.go); P02/P07/P08/P18 |
| NFR-04 — Recoverable provider/plugin/environment failure — **50/100** | Unknown usage korunur, dedup/replay ve pending migration read-only recovery. | Gerçek provider/backend/env reconciliation, safe retry taxonomy, supervisor ve açılan extension matrisi. | [Kanıt alanı](../internal/agent/session_test.go); P05/P09/P10/P14 |
| NFR-05 — Her profile egress gate — **50/100** | Fixture remote inference yok; Docker network none; HTTP origin/proxy/redirect deny. | DNS/IPv6/rebinding/socket/metadata, sensitivity/credential ve bütün enabled profile kabulü. | [Kanıt alanı](../internal/model/http.go); P03/P09/P16 |
| NFR-06 — Bounded disk/log/context/UI — **50/100** | Read/log/response/context/backup/process resource sınırları. | Global disk/artifact quota, physically protected control reserve, GC, slow UI/client buffers. | [Kanıt alanı](../internal/context/compiler.go); P04/P05/P11/P14/P15 |
| NFR-07 — Pinned schema/migration/restore backup — **75/100** | Explicit 1→2 upgrade, restore-validated backup ve commit recovery. | Full install/update/version/power-loss/backend matrix ve deletion-aware restore. | [Kanıt alanı](../internal/store/migration.go); P01/P04/P19 |
| NFR-08 — Warm CLI/context p95 — **0/100** | Referans makine/cohort/cache tanımlı p95 ölçümü yok. | Benchmark manifest, p95 ve latency non-regression; hedefler ölçülmüş sonuç değil. | [Kanıt alanı](../docs/VALIDATION.md); P18/P19 |
| NFR-09 — Source/index/memory erişim kontrolü — **25/100** | Scoped artifact refs, private store/IPC, restriction intersection. | Sensitive source/index/derived memory, cross-project filters, hostile principal/secret artifact egress. | [Kanıt alanı](../internal/artifact/archive.go); P03/P11/P16/P17 |
| NFR-10 — Versioned JSONL/cursor/exits — **50/100** | Structured outputs, task/store cursor ve exit contract. | Production JSONL streaming/reconnect/retention gap, compatibility ve backpressure. | [Kanıt alanı](../internal/cli/history.go); P01/P10/P14/P19 |
| NFR-11 — Default OFF ve secret-safe export — **25/100** | Doctor OFF bildiriyor; provider error body printable çıktıya taşınmıyor. | Bağımsız privacy config, actual egress acceptance, secret-safe preview/export, deletion lineage. | [Kanıt alanı](../internal/cli/cli.go); P04/P16 |
| NFR-12 — Controlled ablation/all attempts cost — **0/100** | Independent ablation yok. | Holdout/uncertainty, paired context experiments, tüm attempts cost ve karşılaştırılabilir baseline. | [Kanıt alanı](../docs/VALIDATION.md); P12/P13/P17/P18 |

Toplam **475 puan**. NFR-08'deki warm CLI p95 <1s / context p95 <250ms PRD hedefleridir; ölçülmüş sonuç yoktur. Default-off field ve redacted HTTP hata metni bütün export/privacy yüzeylerinin kabul edildiğini kanıtlamaz.

## 7. E–G: ilk stable dışında kalan vizyon

Aşağıdaki 13 FR **0/100**. A–D completion paydasına eklenmez; tüm vizyon hesabına dahildir.

| ID / faz | Eksik özellik | Credential ilişkisi |
|---|---|---|
| FR-28 E — **0/100** | AST/LSP resolved graph ve freshness | Çoğu parser/graph işi anahtarsız. |
| FR-29 E — **0/100** | Git history/co-change/issue memory ve cutoff | Yerel Git anahtarsız; uzak issue connector ayrı token isteyebilir. |
| FR-30 E — **0/100** | Semantic embedding/retrieval/reranker | Local model anahtarsız; remote servis credential ister. |
| FR-31 E — **0/100** | Runtime trace/coverage ve test selection | Deterministik backend/eval anahtarsız. |
| FR-32 E — **0/100** | Yeni repo blueprint generation/eval | Fixture altyapısı anahtarsız; gerçek kalite model inference ister. |
| FR-34 F — **0/100** | Bounded workers/isolated writers/integration | Scheduler/backend anahtarsız; remote inference credential ister. |
| FR-35 F — **0/100** | TTL/generation/clock-domain AgentLease | Anahtarsız OS/scheduler işi. |
| FR-36 F — **0/100** | Sticky/phase routing ve measured profile | Routing kodu anahtarsız; remote karşılaştırma credential/bütçe ister. |
| FR-37 F — **0/100** | ExternalAgentAdapter patch worker | Adapter anahtarsız; harici ürün kendi hesabını isteyebilir. |
| FR-39 G — **0/100** | Scoped/versioned/evaluated procedural skills | Lifecycle anahtarsız; bazı eval işlemleri model ister. |
| FR-40 G — **0/100** | Learned policy/shadow/canary/rollback | Yerel eğitim mümkün; ayrıca veri/compute/eval gerekir. |
| FR-41 G — **0/100** | Ayrı opt-in training/preference/context exports | Export/policy anahtarsız; uzak training seçilirse credential. |
| FR-42 G — **0/100** | Team policy/remote execution | Model key'den ayrı auth/tenant/host/deploy işleri. |

Bu capability'lerin kapalı tutulması hata değildir. Temel güven zinciri kapanmadan varsayılan açılmaları PRD sırasına aykırıdır.

## 8. Faz değerlendirmesi

| Faz | Bugünkü durum | Geçişi durduran ana eksik |
|---|---|---|
| A — contract/spike | Anlamlı kısmi implementasyon | Bütün schema/ADR karar eşlemesi, apply/restore spike, tam sandbox/provider matrisi |
| B — durable single agent | Çalışan offline dikey dilim | Trusted verification, global/control budget, gerçek model/process integration, minimum delete ve delivery primitive |
| C — continuity | Preflight/manifest/replay/paging dilimi | Token profile, compiler/freshness/compaction, switch/explain ve controlled ablation |
| D — günlük kullanım | Doctor, review, foreground IPC/export temeli | TUI/supervisor/reconnect, onboarding/privacy/symbols, real provider, native paket/pilot |
| E/F/G | İstenen ürün capability'leri yok | Ayrı ölçümlü deneyler; bütün vizyon ilk stable için zorunlu değil |

A veya B bitmiş sayılamaz. C/D'den bazı temellerin erken yapılması eksik A/B korumalarını kapatmaz.


## 9. PRD §28: 38 kritik hata ailesi

Aşağıdaki durumlar [release kaydındaki](RELEASE_GATES.md) gözlenmiş alt dilimlere dayanır. **PARTIAL tüm family PASS demek değildir.** F/G capability'lerine özgü acceptance aktif olduklarında ayrıca zorunludur; A–D guard'ları capability kapalı diye gevşetilemez.

| Family | Durum | Var olan kanıt / kalan |
|---|---|---|
| F01 İlk write crash | PARTIAL | CAS-before-pointer/fresh manifest; bütün first-write/backend kill sınırları eksik |
| F02 Effect sonrası response kaybı | PARTIAL | Durable intent/reserve/unknown ve no blind retry; gerçek dış effect reconciliation eksik |
| F03 PASS sonrası kaynak değişimi | PARTIAL | Digest/frozen binding predicate; gerçek protected check→edit→reverify akışı eksik |
| F04 Lockfile/env değişimi | PARTIAL | Whole source değişimi yakalanır; dependency/environment typed matrisi eksik |
| F05 Read sonrası user edit | PARTIAL | Stale preimage/read-set reddi; live apply race çözümü eksik |
| F06 Queued steering | PARTIAL | Durable raw barrier/epoch/approval invalidation; gerçek provider/process fencing matrisi eksik |
| F07 Expired lease worker | PARTIAL | Owner generation kontrolleri; AgentLease/backend publish fence yok |
| F08 Concurrent budget reserve | PARTIAL | Task rezervasyonları var; atomik global ledger/concurrent remaining denetimi yok |
| F09 Sibling escape | PARTIAL | Docker readonly mount profili; tam OS/backend escape matrisi eksik |
| F10 Junction/symlink/case | PARTIAL | Path/metadata guards ve platform fixture'ları; hostile replacement/handle race kabulü eksik |
| F11 Network/external file test | PARTIAL | Docker network none/readonly/nonroot; tam tree/destination/credential matrisi eksik |
| F12 Fake PASS/exit0 | PARTIAL | Model/stdout quality yükseltemez; gerçek trusted discovery/result channel eksik |
| F13 Acceptance azaltma | PARTIAL | Protected closure/required coverage guard; authorized semantic check revision eksik |
| F14 Corrupt index/watch loss | PARTIAL | Native index integrity/direct capture; incremental watcher fallback yok |
| F15 Partial tool-call stream | PARTIAL | Incomplete/malformed nonstream response reddi; SSE parser/cut matrix yok |
| F16 Context overflow | PARTIAL | Mandatory preflight dispatch/reserve'den önce durur; gerçek tokenizer/profile kabulü eksik |
| F17 Bad compaction | NOT_IMPLEMENTED | Compactor/verifier yok |
| F18 Privacy fallback | PARTIAL | Restriction intersection ve no proxy/redirect; full lineage/model switch kabulü eksik |
| F19 Plugin metadata erişimi | NOT_IMPLEMENTED | İzole plugin/extension runtime yok |
| F20 Disk/blob/migration failure | PARTIAL | Corruption ve explicit restore-tested migration crash recovery; control reserve/physical power-loss eksik |
| F21 İki daemon/owner | PARTIAL | OS lock ve peer-auth generation; complete supervisor/hostile-principal kabulü eksik |
| F22 Workspace restore/user edit | PARTIAL | Fresh store restore güvenli; canlı workspace restore/three-way conflict yok |
| F23 Slow/disconnected JSONL | PARTIAL | Historical event cursor/paging; retained-gap resync/live reconnect/backpressure eksik |
| F24 Telemetry OFF | PARTIAL | Fixture remote yok/OFF bildirimi; bağımsız privacy config ve bütün enabled surface egress kabulü eksik |
| F25 C/D kapalı B guards | PARTIAL | Minimum context/revision/policy guard var; full sensitive/env/verification matrisi eksik |
| F26 Pre-verification weakening | PARTIAL | Operator origin/file/tree/absence/helper/config guard; full independent closure/revision/observer eksik |
| F27 Source'u değiştirip geri alma | PARTIAL | Docker readonly profil; verification boyunca protected source/check ve trusted observer bütünleşmesi eksik |
| F28 VERIFIED + delivery conflict | PARTIAL | Pure quality/fulfillment predicate; live apply/merged candidate revalidation yok |
| F29 Reboot/suspend/clock | NOT_IMPLEMENTED | Complete backend lease/clock-domain matrisi yok; request expiry tek başına yeterli değil |
| F30 Store/target alias owner | PARTIAL | Store lock/generation; target/repo mutex ve backend old owner fencing eksik |
| F31 Parent exited/live child/cache | PARTIAL | Docker timeout cleanup/quiescence fixture; supervisor/orphan/shared environment matrisi eksik |
| F32 Pause/resume/stale respond/dedup | PARTIAL | Native controls, bound one-shot requests, command conflict; full process/attempt matrisi eksik |
| F33 Low budget/disk/control reserve | PARTIAL | Task budget/unknown preservation ve migration space preflight; bounded control reserve yok |
| F34 Delete/shared blob/old backup | PARTIAL | Backup/closure guard var; delete/tombstone/current watermark/purge hiç yok |
| F35 Multi-task sequence/checkpoint | PARTIAL | Task/store cursor, full historical replay/rehash corruption guard; full retention/supervisor matrisi eksik |
| F36 Missing intent/criteria + no-op | PARTIAL | Raw intent closure/empty criteria/no false VERIFIED; trusted no-op/analysis artifact acceptance eksik |
| F37 Independent evaluator | NOT_IMPLEMENTED | Hidden benchmark, çözüm/kontrol denominator ve pilot kanıtı yok |
| F38 Git helper/OSC/secret egress | PARTIAL | Capture host Git çalıştırmaz, escaped output ve HTTP error redaction; full tool/export sensitivity/UI matrisi eksik |

Ailelerin **34'ü PARTIAL, 4'ü NOT_IMPLEMENTED; 0'ı bütünüyle kapanmış**. Bu sayı alt case PASS'lerini yok saymaz; incomplete family'yi PASS saymayı engeller. Ek X01–X10 kabul senaryoları planda eşlenmiştir; known_absent, external check edit, server tool deny, repo config escalation, migration restore, generated writes, flaky attempts, unsupported backend, model switch ve post-check edit ayrıca kapanmalıdır.

## 10. Model API anahtarı gereken kalan işler

**API anahtarı kod yazabilmek için değil, gerçek uzak servis kabulünü çalıştırmak için gerekir.** Kullanıcının “şimdilik offline fixture” tercihi korunmuştur. Bugünkü eksiklerin çoğu anahtar olmadığı için bekleyen işler değildir.

| Kalan iş | Gereken | Bugünkü durum / kabul |
|---|---|---|
| Gerçek OpenAI Responses round-trip | Mevcut adapter yolu için yetkili API credential, izinli model/profile ve inference bütçesi | Client/tool/result/continuation/usage protokol kodu var; actual inference, auth/rate/refusal/cancel/partial/error kabulü yapılmadı |
| Gerçek Anthropic Messages round-trip | Mevcut adapter için API key, uygun workspace ve inference bütçesi | Offline vectors var; gerçek client-tool/continuation/usage/auth/rate kabulü yapılmadı |
| Gerçek remote stream/usage/failure conformance | Aynı credentials + çalışan streaming implementasyonu | Offline adversarial testler önce yazılabilir; gerçek servis proof ayrıca gerekir |
| Provider token/context/cache/retention capability doğrulaması | İzinli remote model, versioned runtime profile | Metadata/profile kodu anahtarsız; gerçek limits/count/usage/cache behavior ayrıca doğrulanmalı |
| Remote coding/long-task/switch eval | Credentials, tüm attempts maliyeti ve bağımsız evaluator | Key tek başına başarı kanıtı değil; diğer güven zinciri hazır olmalı |
| E/F/G remote embedding/reranker/routing/training | Seçilen servisin credentials/hesabı | İlk stable kapsamına bütün deneyler eklenmez; local alternatifler mümkün |

OpenAI API bearer credential olarak API key veya workload identity access token kabul eder; mevcut repo `Secret` handle'ı üzerinden bearer gönderir, workload identity lifecycle'ı uygulamaz. [Resmi OpenAI authentication](https://developers.openai.com/api/reference/overview/#authentication).

Anthropic alternatif authentication yolları da sunar; mevcut adapter legacy `x-api-key` gönderir. Workload identity/App Attest yoktur; key/workspace seçimine göre gereken `anthropic-workspace-id` desteği ayrıca tasarlanmalıdır. [Resmi Anthropic authentication](https://platform.claude.com/docs/en/manage-claude/authentication).

**CLI bugün ortamdan OPENAI_API_KEY veya ANTHROPIC_API_KEY okumaz; .env dosyası oluşturmak gerçek inference açmaz.** Provider/model config, secure credential onboarding/keychain ve runtime adapter bağlantısı eksiktir. Anahtarları chat'e veya repo'ya eklemek çözüm değildir. Gerçek kabul deferred kalır; fixture PASS bunun yerine geçmez.

## 11. Model API anahtarı gerektirmeyen kalan işler

| Alan | Anahtarsız yapılabilecek somut iş | Başka bağımlılık |
|---|---|---|
| Provider runtime | Fixture ve gerçek adapter için ortak loop; model/profile config; typed auth failure; SSE/partial/cancel parser; continuation/tool protocol; keychain interface ve sahte secret handle testleri | Gerçek remote kabul en sonda credential ister |
| Gerçek yerel Ollama | Runtime wiring, local profile/tool parser/context/usage/cancel ve local endpoint acceptance | Kurulu Ollama, önceden indirilmiş gerçekten local model, model/template bilgisi, yeterli RAM/VRAM ve gerçek çalışma |
| P02/P07 Verification | Check discovery, immutable protected closure, authorized revision, trusted runner/result channel, independent observer, coverage/baseline/flaky receipts | Gerçek OS sandbox + bağımsız fixture/evaluator |
| P03 Sandbox | Process identity/tree, readonly source/check, isolated scratch, no egress/credential leakage, epoch fence, orphan drain ve escape/race matrisi | Uygun Linux/backend ve native platform erişimi |
| P04 Store lifecycle | Disk/artifact quota, protected control reserve, minimum retention/delete, payload envelope ayrımı, tombstone/GC/shared ref/backup purge/restore watermark | Fault injection ve OS durability test ortamı |
| P05 Global budget | Atomik reserve/settle/release, concurrent child allocation, versioned price/usage lineage, conservative unknown reconciliation | Fixture/fake provider ile geliştirilebilir |
| P06 Workspace/plan | Typed environment/read/write contract, generated output guard, PlanNode DAG/dependency/mutex, freeze serialization | Deterministik repo/process fixtures |
| P08 Delivery | Exact preimage/exclusive target capability, file intent/receipt, conservative three-way apply/restore, crash/conflict ve merged candidate reverify | Desteklenen filesystem/backend |
| P10 Agent | Semantic scoping/criteria flow, bounded planning/repair/watchdog, terminal yeni attempt, no-op/analysis artifacts, safe recovery | Sahte modelle kodlanabilir; gerçek kalite sonradan inference/eval ister |
| P11–P13 Context | Provider token count profile, stable prefix, scopes/sensitivity, inclusion reasons, pin/page/why, compaction/verifier, dependency freshness, safe switch | Gerçek remote tokenizer/count proof ayrıca credential isteyebilir; local modelde key yok |
| P14 Supervisor | Daemon lifecycle, detach/attach, command/event handshake, contiguous reconnect/gap resync, bounded clients, orphan cleanup | Native OS process/IPC testing |
| P15 TUI | Composer, @file/slash, prompt queue, request/diff/status/why, cancel/pause/restore UX, resize/keyboard/escaped logs | Gerçek kullanıcı denemeleri |
| P16 Config/privacy/extension | Authority intersection, independent privacy axes, onboarding, secret-safe preview/support/export, derived delete, pinned effect/extension isolation | OS keychain; gerekiyorsa connector credentials model key'den ayrıdır |
| P17 Language tools | TS/JS/Python source-bound outline/symbol, version/freshness/coverage, lexical fallback | Parser/fixtures ve dependency policy |
| P18 Eval/performance | Hidden evaluator, çözüm/kontrol cohort, holdout/uncertainty, all-attempt accounting, reference CLI/context p95 | Real model quality kohortu inference ister; harness/fault/performance altyapısı anahtarsız |
| P19/P20 Release | Installer/update/rollback, reproducible build, SBOM/dependency/license/secret/vulnerability audit, checksum/signature, native smoke, runbook/evidence bundle | Signing identity, platformlar ve gerçek pilot kullanıcıları; model API key değildir |

Ollama'nın yerel API'si authentication gerektirmez. Ancak cloud model yerel API'den de uzak servise yönlenebilir; local-only kabulü model/config/conformance ile doğrulanmalıdır. Mevcut adapter literal loopback ister ve bilinen `:cloud`/`-cloud` model biçimlerini reddeder; bu, her olası proxy yapılandırmasını dışlayan tam proof değildir. [Resmi Ollama authentication](https://docs.ollama.com/api/authentication).

Model indirmesi ayrı ağ/kurulum adımıdır; offline çalışma için önceden hazırlanmalıdır. Cloud Ollama'nın doğrudan API erişimi credential ister ve mevcut local profil tarafından desteklenmez.

**Sonuç:** Anahtar verilmeden offline implementasyon ve birçok gerçek OS/local model kabulü ilerletilebilir. “API key yok, gerisi yapılamaz” değerlendirmesi yanlış olur. Anahtar geldiğinde de eksik runtime/verification/privacy zinciri kendiliğinden tamamlanmaz.

## 12. Test kanıtının gücü ve sınırları

Mevcut [VALIDATION](VALIDATION.md) kaydı, kod revision'ına ait şu geçmiş sonuçları içerir:

| Kanıt | Değerlendirme |
|---|---|
| Windows **304 benzersiz test/alt test PASS, 2 SKIP, 0 FAIL** | Full tur + opt-in real Docker + final store turu birleştirilmiş. İki symlink privilege SKIP PASS sayılmadı |
| Linux module verify/vet/full race + final store race PASS | linux/amd64 engineering runtime kanıtı |
| Windows offline demo/export/backup/fresh restore/history PASS | Native kullanıcı yolu var; sonucun kalite sınırı UNVERIFIED |
| Native 0.5 backup → format1 restore → migrate2 → idempotent retry → backup2/fresh restore/replay PASS | Original backup korunmuş, journal sequence/tail/state aynı |
| Real Docker broker tests PASS | Linux engine'de exact mount source/image/argv, readonly source/root, nonroot, network-none, timeout child cleanup. Bütün OS isolation değil |
| macOS/arm64 cross-build PASS | Test binary'leri compile edilmiş; /bin/true ile runtime atlanmış. **COMPILE_ONLY**, native smoke değil |
| JSON/index fuzz ve govulncheck geçmiş kayıtları | İlgili decoder/dependency sürümüne ait tarihsel evidence; sınırsız security proof değil |
| Provider vectors/loopback fixtures PASS | Gerçek OpenAI/Anthropic/Ollama inference değil |

[CI workflow](../.github/workflows/ci.yml) Windows/Linux/macOS, module/vet/test/build/doctor/vulnerability ve Linux race/fuzz/Docker kontrollerini tanımlar. Workflow varlığı GitHub'da son run'ın PASS olduğunu kanıtlamaz; bu analizde remote CI sonucu gözlenmedi.

Bu dokümantasyon çalışması kaynak implementasyonunu değiştirmedi. README örnekleri için ayrıca native smoke yapılır; mevcut 304 sonucu “bugün yeniden bütün testler koşuldu” diye sunulmaz. Independent benchmark success, false-verdict rate, recovery reliability, kullanıcı rework, all-attempt maliyet ve p95 henüz ölçülmüş değildir. Böyle bir oran olmadığı için uydurulmadı.

## 13. %100 A–D için kritik yol

Mevcut [tamamlama planını](IMPLEMENTATION_PLAN.md) daraltmadan, bağımlılık sırasıyla:

1. **P01/P02/P03/P04/P05/P06:** Eksik contracts, check origin/closure review, gerçek backend fencing, store lifecycle/delete/control reserve, global ledger, environment/plan/candidate freeze sözleşmeleri.
2. **P07:** Trusted discovery/execution/result channel ve bağımsız observer; V0–V5 receipts, requirement coverage ve no false VERIFIED. Bu kapanmadan genel test çıktısı başarıya çevrilemez.
3. **P08/P09/P10:** Güvenli delivery primitive, streaming/credential/provider adapters ve aynı ortak gerçek/fixture agent loop. Real remote kabul kullanıcı offline tercihi sürerken deferred olarak görünür kalır.
4. **P11/P12/P13:** Provider token/context semantics, continuity/freshness/compaction, model lock/switch ve explain/pin.
5. **P14/P15/P16/P17:** Supervisor/reconnect, günlük TUI, onboarding/privacy/extension boundary ve TS/JS/Python navigation.
6. **P18/P19/P20:** Independent eval/ablation/performance, native Windows/Linux/macOS install/update/recovery, doğrulanabilir imzalı paket, pilot/evidence bundle ve gate review.

Gerekli “bitti” kanıtları:

- A–D kapsamındaki bütün FR/NFR uygulanmış ve ilgili acceptance gözlenmiş.
- Aktif scope'a uygulanabilir kritik family/case'lerde %100 PASS; SKIP/UNKNOWN/DEFERRED tamamlanmış sayılmaz.
- İki remote protokol + bir gerçekten local endpoint actual conformance.
- Gerçek backend source/check readonly, process quiescence, fencing/egress/credential/resource proof.
- Restore/migration/deletion/current watermark; sensitive managed-copy purge durumu doğru.
- Live apply destekleniyorsa user edit/exclusive access/conflict/crash kabulü; unsupported backend açık candidate-only davranış.
- Bağımsız evaluator; success/control denominator, bütün attempts cost ve preregistered uncertainty/non-regression.
- Platform capability manifest, install/update/rollback/runbook, checksum/signature/SBOM ve native smoke.
- İlk stable için E–G'nin bütün deneysel özelliklerini bitirmek şart değil; temel korumalar ve dar B/D dilimleri şart.

PRD D pilotu başlangıçta **öneri olarak 5–10 geliştirici** der; bu sayı bağlayıcı bir istatistiksel başarı eşiği değildir. Mevcut P19 planı 5–10 kullanıcı pilotunu teslim işi olarak belirler. PRD'nin önerisini “resmi zorunlu test sayısı”na çevirmeden gerçek kullanıcı kabulü/rework kanıtı toplanmalıdır.

Bugünkü en kritik açıklar yalnız modeller değildir: **trusted verification, tam backend fence/isolation, global/control bütçe, minimum delete/retention ve gerçek agent/delivery entegrasyonu**. Kullanıcı deneyimi ve dağıtım bunların üzerine tamamlanmalıdır.


## 14. Bu README değişikliğinde gözlenen doğrulama

8 Ekim 2026 Windows native kontrolü: README'den çıkarılan 8 PowerShell blok grubu çalıştırıldı. Kaynak derleme/version/doctor, run/status/diff, export, review pending request, güncel bağlarla respond/resume, inspect/replay/events, backup/fresh restore/status/replay başarılı. Normal run ve onay sonrası resume beklenen exit **2** verdi; review mutation öncesi WAITING_USER gözlendi.

Source hello.txt SHA-256 değişmedi; kalite UNVERIFIED kaldı; restored candidate digest'i original ile aynı. Dokümantasyonun yerel dosya bağlantıları ve 41 kalem/1.650 puan hesabı doğrulandı. Placeholder TASK_ID/OLD_STORE örnekleri canlı bir kullanıcı task/store'una uygulanmadı; Linux/macOS komutları bu dokümantasyon turunda native yeniden çalıştırılmadı.

Yerel ignored kanıt: .cache/readme-usage-validation.txt; reproducible metin-blok kontrolü .cache/readme-validation.ps1. Bunlar clone'a dahil olmayan yerel çıktılardır. README kullanıcı komutlarını içerir; bütün uygulama testlerinin yeni turu veya production gate açılması iddia edilmez.
