# Anahtarsız geliştirme kaydı — 0.7.0-dev

Tarih: 8 Ekim 2026. Başlangıç: da48e40 / 0.6.0-dev. PRD ve [tamamlama planı](IMPLEMENTATION_PLAN.md) yetkili kapsamı korur. [Önceki %40,24 analizi](PRD_STATUS_ANALYSIS.md) 0.6 kod revision'ına ait tarihsel değerlendirmedir; bu teslim sonrası yeni ölçüm diye sunulmaz.

**Anahtarsız backlog'un tamamı bitmedi.** Bu sürüm P06/FR-33 B plan, P09/P10 local provider runtime ve P05 store-wide token reservation dilimlerini kodlar. Release kapıları kapalı; production-ready veya gerçek model conformance iddiası yoktur.

| İş | Kodlanan/kanıtlanan | Açık kalan |
|---|---|---|
| Typed plan DAG | Version/spec/policy/candidate binding; bounded node/contract/scope/criterion/check/mutex metadata; DEPENDS_ON DAG; deterministic tek active node; unknown/cycle/unsafe scope reddi | Semantic contract review, global resource allocation, multiworker/çok-store mutex, independent plan outcome eval |
| Plan/native tool integration | plan_propose/next/finish, read/write/read-set restriction, pending node final block; IMPLEMENTED quality'ye taşınmaz | Trusted check receipts ve goal review |
| Plan durability | CAS work product output/schema/node/candidate/contract/trust bağları; load/backup closure; historical candidate hydration; revision invalidate; status/IPC/history/fresh restore | Retention/delete/unavailable lifecycle |
| Gerçek local adapter yolu | CLI ollama/local-model config; actual HTTP request; fresh inference authority; ortak durable loop; no proxy/redirect/secret; cloud/remote/mixed config deny | Kurulu gerçek model/template conformance ve OS egress proof |
| Local context/usage | Model/num_ctx/num_predict/output/timeout binding, conservative preflight, usage settlement, unknown reserve/no blind retry, raw response CAS, model timeout üzerinde task active deadline | Tokenizer/model profile calibration, streaming/thinking ve global price/resource ledger |
| Store-wide token ledger | Immutable operator limits; transaction-atomic intent/reserve; concurrent multi-task bound; exact command dedup/settle; raw request/profile/usage receipt validation; actual overage charge; UNKNOWN cancel/restart/restore retention; budget CLI/IPC; work limit doluyken controls açık | Monetary/price lineage, child/resource allocation, physical control reserve, trusted reconciliation/release ve legacy ledger migration |
| Local recovery/revision | Unknown restart'ta korunur; profile/context backup/fresh restore; steering barrier/pause ve local spec revision fixture istemez | Full process/supervisor/cursor/platform acceptance |

Mevcut ortamdaki Get-Command ollama kontrolü installed executable göstermedi. Yerel Ollama model kabulü API anahtarı değil, kurulu runtime/model/hardware bağımlılığı bekler. Fixture/loopback testleri onun yerine PASS yazılmaz.

## Öncelikli anahtarsız kalan işler

- P02/P03/P07: full protected closure/authorized check revision, trusted observer/result protocol, gerçek source/check readonly interval, process fencing/orphan/escape matrisi.
- P04/P05: quota/control reserve, silinebilir payload envelope, retention/delete/GC/shared refs/tombstone/backup watermark ve global para/CPU/disk/child ledger tamamlaması.
- P06/P08: typed environment dependency invalidation, generated output; exclusive target access/apply/restore/three-way/crash receipts ve reverify.
- P09/P10: streaming/cancel/typed error/profiles; semantic scoping/coverage/watchdog/no-op/analysis/new attempt.
- P11–P13: token profile/compiler/prefix/filter/inclusion/pin/why, compaction/verifier, freshness/model switch.
- P14–P17: background supervisor/detach/attach/reconnect/backpressure, TUI/onboarding/config/privacy/pinned extension ve language outline/symbol.
- P18–P20: independent eval/ablation/performance, packaging/update/signature/SBOM/native platform/pilot/release evidence.
- E/F/G'nin ayrı anahtarsız deneyleri: AST/history/local semantic retrieval, measured test selection, lease/scheduler/workers, skills/policy/export/team altyapısı. İlk stable için E–G'nin tamamı zorunlu değildir.

Signing identity, native macOS ortamı ve gerçek kullanıcı pilotu model API key'den farklı dış bağımlılıklardır. Credential gelmesi bu açıkları kapatmaz.

## Sürüm uyumluluğu

SQLite schema 2 korunur; yeni DB tablosu/implicit migration yoktur. Yeni runtime/plan/store_token_limits opsiyonel task document alanları, token_account projection ve token_mutation event alanları ve model tool contracts'tır; 0.7 eski fixture task'larını okuyabilir. 0.7 plan/runtime/token alanlarını içeren journal/task belgesini eski strict decoder sessizce yorumlayamaz; older binary ile downlevel resume denenmemelidir. Tarihsel source-bound intent/check origin/verdict değiştirilmez. Legacy görevlerin bilinmeyen kullanımı store-wide hesabın dışında sessizce bırakılmaz: eski store okunabilir/resume edilebilir, yeni ledger görevleri fresh store ister. Limit değişimi ve untracked task karışımı fail-closed olur.

## Doğrulama

Windows full JSON test turu ve final affected tests; Linux pinned offline runtime race/vet; Darwin arm64 yalnız çapraz derleme. Ayrıntılı gözlenmiş sonuçlar [VALIDATION.md](VALIDATION.md) içinde tutulur. Yerel ignored log'lar .cache/windows-keyless-final-tests.jsonl, .cache/linux-keyless-final-validation.txt; önceki plan/runtime dilimi .cache/windows-plan-runtime-tests.jsonl ve .cache/linux-plan-runtime-validation-final.txt. Actual local inference/remote providers, native macOS, physical power-loss ve pilot kabulü ayrı ve açık kalır.
