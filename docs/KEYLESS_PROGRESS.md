# Anahtarsız geliştirme kaydı — 0.8.0-dev

Tarih: 8 Ekim 2026. Başlangıç: da48e40 / 0.6.0-dev. PRD ve [tamamlama planı](IMPLEMENTATION_PLAN.md) yetkili kapsamı korur. [Önceki %40,24 analizi](PRD_STATUS_ANALYSIS.md) 0.6 kod revision'ına ait tarihsel değerlendirmedir; bu teslim sonrası yeni ölçüm diye sunulmaz.

**Anahtarsız backlog'un tamamı bitmedi.** 0.7 temeli P06/FR-33 B plan, P09/P10 local runtime ve P05 token reservation’dır. 0.8 ekleri aşağıdadır. Release kapıları kapalı; production-ready veya gerçek model conformance iddiası yoktur.

## 0.7 tarihsel temel (0.8 ekleri aşağıdadır)

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

## 0.8 tamamlanan anahtarsız dilimler

| İş | Uygulanan davranış | Açık kabul / kapsam |
|---|---|---|
| Registered check tools | Operator pinned profile + ID-only check_run; protected readonly Docker broker; candidate/spec/policy/origin/profile/argv bound CAS receipts; exact check_output; CLI/IPC checks/status | Trusted observer/discovery, full closure review, authorized check revision ve full fencing yok; EXIT_ZERO verification UNKNOWN |
| Completion/analysis | Mevcut bütçede 0..8 bounded repairs; current checks/pending plan final blockers; readonly ANALYSIS, NO_CHANGES ve bound final artifact/export | Semantic goal/coverage/analysis proof ve bağımsız reviewer yok |
| Fresh attempt | Exact terminal parent seq, raw input/criteria inheritance, fresh live capture, zero old approval/plan/quality reuse; stable ID dedup; Created/Scoping crash recovery; store charge korunur | UNKNOWN reconciliation, full alternative strategy/pilot eval açık |
| Context inspection | context-why/component inclusion, context-page exact retained request, checks/check-output/report owner IPC ve direct readonly CLI | Full compaction/pin/hydration/redaction/tokenizer profile yok |
| JS/TS/Python outline | Lexical declarations, comments/strings mask, exact digest/span/source IDs ve bounded candidate/policy-bound paging | AST/module/reference/LSP/semantic graph değil |
| ChatGPT coding login | Resmî SIWC browser/loopback state+nonce+PKCE; issued client persistence; RS256/JWKS identity; distinct profiles/select; serialized rotating refresh/granted scopes; revoke + local logout | Gerçek eligible account/browser/refresh/revoke kabulü NOT_RUN; diğer coding abonelikleri yok |
| Credential isolation | Windows user DPAPI/private ACL; Unix 0700/0600 owner-only; source/store disjoint; bounded atomic records; tracked olsa da credential/temp-name capture/mutation/forged-manifest deny; redacted status/catalog | Unix encrypted-at-rest değildir; keyring/HSM/enterprise managed lifecycle tamamlanmadı |
| Subscription runtime | Exact selected profile + visible live catalog + explicit remote consent; public Responses stateless SSE; namespace native tools; sealed terminal receipt; full 128000 output ceiling reservation | gpt-6.1-sol tek registered profile; gerçek inference NOT_RUN; no live UI deltas/connection resume/tokenizer calibration |
| API-key runtime | OpenAI/Anthropic CLI wiring; fixed official origins/env handles; explicit remote consent; common durable budget/tool loop; no fallback; secret not persisted | Gerçek endpoint acceptance anahtar bekler; geniş profile/error recovery/price lineage açık |
| API/local stream | Explicit --stream: OpenAI Responses SSE, Anthropic Messages SSE (fragment JSON/signed thinking/cumulative cache usage), Ollama NDJSON; full terminal-only tools; raw receipt recovery; model/sequence/quota/duplicate/fallback deny; cancellation and UNKNOWN restore/no retry | UI live deltas/connection resume ve actual provider/model acceptance açık |
| Kernel no-dispatch | Preflight refusal proves transport not called; bound zero usage receipt; ledger KERNEL_NO_DISPATCH and fresh restore validation | Dispatch sonrası UNKNOWN retained; billing hard cap/physical control reserve değil |
| Usable examples | Analysis/check fixtures + pinned matching runtime/plan; check-config fingerprint/validation; README commands, 0.8 help/doctor | Installer/pilot/native macOS acceptance yok |

Bu işler PRD work package’lerini bütünüyle DONE yapmaz. Anahtarsız backlog’un tamamı hâlâ bitmedi; yalnız API-key kabulü kalmış gibi gösterilmeyecek. İlk stable A–D için yukarıdaki açık engineering işler ve alttaki liste zorunludur. 0.6 yüzde puanı yeni 0.8 ölçümü değildir.

### Anahtar gerektiren gerçek kabul

- OpenAI API ve Anthropic API’nin gerçek model/tool/usage/timeout/conformance testleri API credential ister; CLI bağlantı kodu artık hazır.
- SIWC gerçek hesap kabulü API key istemez, uygun ChatGPT aboneliğiyle kullanıcının browser login’ini ister. Kodlanan login/refresh/revoke/runtime offline testleri gerçek hesap PASS’i değildir.
- Ollama actual model acceptance API key istemez; kurulu yerel runtime/model/hardware gerekir.
- Native macOS, signing identity, pilot ve independent evaluator API credential’dan bağımsız dış kabul gereksinimleridir.

### Öncelikli anahtarsız kalan implementasyon

Trusted observer/discovery ve protected check revision; full process fencing/orphan/escape; physical control reserve + monetary/CPU/disk/child ledger; retention/deletion/GC/tombstone/backup watermarks; typed environment/output invalidation; exclusive live apply/restore/three-way crash receipts; semantic goal coverage; model/profile switch ve geniş provider profile/recovery/live UI streaming; context compaction/verifier/pins/hydration/privacy lineage; supervisor/detach/attach/reconnect/backpressure; TUI/onboarding/config/extensions; independent eval/performance; verified install/update/SBOM/signature/pilot/release bundle.

[ADR 0009](adr/0009-checks-completion-and-plan-auth.md) yeni trust/credential/accounting sınırlarını; [README](../README.md) kullanıcı akışını; [VALIDATION](VALIDATION.md) gözlenen testi kaydeder. A/B/stable gates CLOSED.

0.8 test kanıtı: Windows full + final usage regression 449 benzersiz PASS/2 SKIP/0 FAIL; Linux full race/vet/build, Darwin COMPILE_ONLY; README native source-preserving workflow ve gerçek Docker registered check. Details/limitation [VALIDATION](VALIDATION.md).
