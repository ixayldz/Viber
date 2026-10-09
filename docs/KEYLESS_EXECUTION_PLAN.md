# Anahtarsız kapsam yürütme planı — 9 Ekim 2026

Yetkili kapsam: [PRD 1.1](../prd.md). Başlangıç revision: 049c4e4bab5910c8807ebcde95c6866539b937aa / 0.8.0-dev. Kullanıcının talebi tüm anahtarsız geliştirme eksiklerinin tamamlanmasıdır; bu dosya önceki P01-P20 planını daraltmaz. Gerçek hesap/platform/hardware/insan kabulü API anahtarı bekleyen işler ile karıştırılmaz.

## Çalışma kuralı

Her paket için contract + gerçek agent/CLI wiring + anlamlı saldırgan/recovery fixtures + docs + commit gerekir. Durumlar OPEN, IMPLEMENTING, VALIDATED, EXTERNAL_ACCEPTANCE_PENDING. VALIDATED yalnız test edilmiş ilgili dilimi kapatır; bütün PRD family veya production gate'i otomatik kapatmaz. Kullanıcı dosyalarına broad reset veya overwrite yok; mevcut kullanıcı silmeleri ve tarihsel analiz değişikliği korunur.

## Paketler ve bağımlılıklar

| Paket | Kapsam | Kabul | Durum |
|---|---|---|---|
| C01 | FR-16/17/18: bounded archive compaction, source-bound history page, context pin, safe model switch | Pending/UNKNOWN dispatch switch/compaction engeli; bütün authoritative state korunur; provider opaque continuation taşınmaz; backup/fresh restore lineage; context overflow dispatch öncesi | IMPLEMENTING |
| C02 | FR-09/NFR-06: multi-resource admission/control reserve | Transaction-atomic money/CPU/disk/time/child; physical reserve exhaustion/cancel fixtures; unknown reconciliation | IMPLEMENTING |
| C03 | FR-26/NFR-07/09/11: retention/delete/GC | Silinebilir payload vs envelope; tombstone/shared refs/managed copies; backup watermark no-revival; quiescence | IMPLEMENTING |
| C04 | FR-11/NFR-03: trusted verifier/goal coverage | Protected observer, result/discovery protocol, closure review/revision, V0-V5/baseline/flaky; fake PASS ve stale receipt reddi | IMPLEMENTING |
| C05 | FR-07/08/15/33: environment/process/fencing/mutex | Gerçek Docker/WSL namespace/credential/child/rootless/race matrix; old owner publish engeli; env invalidation | IMPLEMENTING |
| C06 | FR-21: apply/restore | Teknik exclusive-access capability, per-file receipts, partial-write reconciliation, conflict + kullanıcı edit koruması, merge reverify; unsupported backend fail-closed | OPEN |
| C07 | FR-12/25/NFR-10: supervisor/JSONL | Background lifecycle, secure detach/attach, cursor/gap/resync, backpressure, orphan recovery | IMPLEMENTING |
| C08 | FR-19/22: TUI | Composer/@file/slash/queue/control/diff/context/model; keyboard/resize/live output; UI crash task kaybı değil | IMPLEMENTING |
| C09 | FR-23/38/NFR-09/11: config/privacy/extensions | Authority intersection, bağımsız privacy axes, OS keyring, safe support/export, pinned isolated extension | IMPLEMENTING |
| C10 | FR-27/NFR-08/12: language/eval/performance | Source-bound coverage; independent evaluator, all-attempt denominator/cost, holdout/paired continuation, reference p95 | IMPLEMENTING |
| C11 | P19/P20: paketleme/release | Reproducible install/update/rollback, SBOM/licenses/checksum/signature, exact-revision evidence, native/platform/pilot kabulü | IMPLEMENTING |

C02/C03/C04/C05 altyapı işleri C06/C07/C09 ile bağımlıdır; arayüz ve benchmark bunların güven zincirini atlatmaz. C01 kapasite/model geçiş sınırları tamamlanmadan uzun görev iddiası açılmaz.

## Anahtar istemeyen dış kabul

ChatGPT gerçek kullanıcı login/consent/inference; kurulu local model/hardware; native macOS ve rootless/backend filesystem matrix; signing identity/release infra; independent human review/pilot. Geliştirme ve fixture altyapısı yapılır; gerçek kabul NOT_RUN/PENDING ise PASS yazılmaz.

## API anahtarı isteyen kabul

OpenAI ve Anthropic gerçek model/protokol/rate/quota/usage/cancel conformance; ilgili remote model benchmark'ları. Hata/profile/eval altyapısını yazmak anahtar istemez.

## E-G

FR-28..32, FR-34..37, FR-39..42 ve FR-33 F/FR-38 G deneyleri ilk stable A-D'den ayrı PRD kapsamıdır. Kullanıcı tüm vizyonu istediğinde bağımlı core guard'ları tamamlandıktan sonra kendi eval/feature gates'iyle yürütülür; kapalı/uygulanmamış deney tam üretim capability'si sayılmaz.


## 0.9 uygulanan dilim ve ölçülen kabul

C01: source-bound deterministic archive/proof, human exact paging vs opaque-free model projection, bounded source pins, locked-provider safe switch, task/current/global preflight, historical profile/charge lineage, owner RPC/CLI ve atomic generation renewal uygulandı. Semantic model-summary verifier, paired real-model continuation kalitesi ve broader authorized provider/privacy revision aynı şey değildir; ilgili kabul ayrı kalır.

C02: transaction-atomic conservative multi-resource/token admission, immutable currency/versioned operator price catalog, paid-model preflight, bounded native child allocation, rooted physical 32 MiB control reserve + 64 MiB watermark, archive work quota, bounded control writes ve explicit full-upper-bound UNKNOWN model accounting uygulandı. CPU/disk charge gerçek OS tüketimi gibi sunulmaz; tam metadata/managed-copy quota, backend CPU/child observations ve filesystem failure matrix ayrıca uygulanır.

Windows final full turu: 500 PASS / 4 SKIP / 0 FAIL. SKIP'lerden ikisi opt-in Docker, ikisi Windows symlink privilege fixture'ı. Docker broker ve actual agent check ayrı opt-in turda PASS oldu. Format/module verify/vet/native build/doctor PASS. Linux full race/vet/build ve Darwin compile-only sonucu tamamlanınca VALIDATION'a kaydedilir. Bütün C03-C11 kapsamı bu dilim ile kapatılmış sayılmaz.


## 0.9 bakım, supervisor, TUI/config ve OS vault dilimi

C03: typed historical roots ile orphan GC preview/intent/per-object receipt/retry ve backup/fresh restore dedup uygulandı. Journal, source snapshot, compaction, pin, request, ledger ve standalone snapshot kökleri korunur. Bu task privacy deletion, retention, tombstone veya managed backup purge değildir.

C07: ayrı background owner process, detached invocation admission/completion, secure peer RPC, bounded contiguous attach cursor/JSONL, slow client ayrımı ve shutdown lock reconciliation uygulandı. Actual child process fixture geçti. Tüm history retained olduğundan gap yoktur; retention gap/resync, hostile old owner/children fencing ve orphan process family acceptance tamamlanmış sayılmaz.

C08: background owner API'lerine bağlı native terminal/line UI, escaped renderer, Unicode editor/history/paste guard, resize/current-state polling, captured @file fuzzy references, source page, durable queue add/remove ve atomic activation, status/diff/plan/context/model/control/request/response/scope revision eklendi. Yeni goal task scheduling, provider delta transport, force-stop/restore backend ve native PTY/platform kabulü aynı dilimle kapanmaz.

C09: numeric allowlist support/export canary testleri; Unix OS vault AES-GCM + explicit plaintext migration + no plaintext fallback; secret-free global/project/CLI preference resolution vs retained restriction intersection; provider empty-array deny ve pre-CAS sensitive source exclusion uygulandı. Windows DPAPI korunur. Auth Linux race testleri fake vault ile geçti; gerçek Secret Service/Keychain kabulü yapılmadı. Admin/current-policy revisions, complete privacy lifecycle/export policy ve pinned isolated extension/MCP hâlâ geliştirme işidir.

Güncel sabit kaynak kopyası Windows full suite: **539 PASS / 4 SKIP / 0 FAIL** (.cache/windows-0.9-ui-full.jsonl). Önceki GC kopyası Linux full race: **525 PASS / 2 SKIP / 0 FAIL**; tüm Darwin paketleri COMPILE_ONLY ve native Linux build/doctor PASS. Güncel UI/config/vault kopyasının Linux full race turu **545 PASS / 2 SKIP / 0 FAIL**; Linux build/doctor PASS, tüm Darwin paket/binary COMPILE_ONLY. Native macOS/vault runtime kabulü yoktur.

Root LICENSE kararı kod sahibinden beklenmektedir. Paketleme/signature/independent pilot ve gerçek hesap/native platform kabulü anahtarsız dış karar/kabul işleridir. Bunlar API anahtarı bekleyen provider testleri diye yeniden sınıflandırılmaz.

C11: exact committed revision source archive, pinned offline dependency verification, dependency/Go license material, CycloneDX inventory, twice-built byte-identical binaries ve manifest-last hashes komutu uygulandı. Temporary Git fixture smoke PASS. Root LICENSE kararı, signing/install/update/rollback/pilot gate bu komutla kapanmaz.

## C04 korunan V4 gözlemci dilimi

Kernel-owned exact STDIO observer aday kodu observer içinde çalıştırmaz. Golden case/oracle source mount'una veya model context'ine girmez. Profile/image/argv/stdin/candidate bound fresh non-root readonly/network-none Docker subject, explicit case discovery, 2..4 repeats, distinct container identities, baseline comparison, fail-preserving attempts ve unknown cleanup guard uygulanmıştır. Protected output model tool'una verilmez; local operator case/repeat/baseline paging ile okuyabilir.

Operator raw-input/required-goal/dependency review pre-mutation origin ve suite digest'lerine bağlıdır. Nonfinal quality PARTIAL/FAILED olabilir; frozen artifact/report transaction bütün guards ile VERIFIED candidate-only finalization ve exit 0 verebilir. Bu generic framework self-report, whole V0–V5 veya release-ready iddiası değildir. Authorized check revision/dependency discovery/framework verifier families ayrı geliştirme olarak sürmektedir; API anahtarı beklemezler.

Sabit .cache/validation-0.9-observer kaynak kopyası Windows full suite: 579 PASS / 6 SKIP / 0 FAIL. Dört opt-in Docker ve iki symlink privilege SKIP vardır. Gerçek pinned Docker acceptance ayrıca PASS: no-op verified candidate; baseline FAIL→changed candidate PASS; fake stdout PASS→FAILED; source unchanged; all attempts retained. Native CLI observer-demo (recipe→goal review→verification→exact case paging) PASS. Aynı sabit kopyanın Linux full race turu 585 PASS / 4 SKIP / 0 FAIL; module verify/vet/Linux build/doctor PASS. Bütün Darwin arm64 paketleri ve binary COMPILE_ONLY; native macOS acceptance değildir. ADR 0013 ve VALIDATION ayrıntıları korur.

## C05/C07 kalıcı process lease ve owner crash recovery dilimi

Her check subject için process lease/capsule Docker create öncesinde CAS/journal intent'e girer. Physical runtime instance, filesystem directory kimliğine bağlıdır; backup kopyasına taşınmaz. Monotonic clock domain/deadline, generation/policy/spec, source/image/profile/invocation/stdin ve baseline/candidate ordinal bağları her dispatch ve retained receipt'te kontrol edilir.

Eski owner öldüğünde yalnız container listesinin boş olması yeterli görülmez. Explicit bound native reconciliation, daha yeni generation ve özgün physical store gerektirir; exact full container ID/profile/mount/label group'u inceleyip subject'leri kaldırır ve tüm izinli adları çalıştırılmayan engine fence container'larıyla tutar. Gecikmiş create veya eski ID ile start engellenir. Full native allocation charge edilir; kayıp çıktı ve doğrulama kabul edilmez. Eksik/bozuk cleanup kanıtı UNKNOWN risk'i korur; dedup tekrar effect üretmez. CLI/runtime-info/reconcile-native-risk, owner RPC ve TUI /runtime bağlıdır.

Actual Windows host/Linux Docker engine testinde gerçek agent child aniden öldürüldü; orphan temizlendi, eski ID start ve gecikmiş name create reddedildi, kaynak aynı kaldı ve quality UNVERIFIED kaldı. Pure clone/restore/clock/altered-group/full-charge fixtures PASS. Bu rootless/hostile process lifetime/all-filesystem/backend acceptance veya bütün C05/C07 DONE değildir. Fence retention/quota ve tam backend matrix ayrı anahtarsız geliştirme olarak kalır; ADR 0014 sınırları kaydeder.
## C10 bağımsız aday testi ve paired muhasebe dilimi

Integer-only protocol/condition/assignment/measurement contracts; çözümler ve kontrol kohortları; sabit primary/repeated paydalar; bütün retry/missing/UNKNOWN maliyetler; false VERIFIED/strict claim; repo-disjoint splits/cutoff; deterministic paired repository-cluster bootstrap ve karşılaştırma düzeltmesi uygulandı. eval-report private manifest-last paket üretir; eval-report-inspect canonical ölçümlerden bütün float presentation değerlerini yeniden hesaplar. Imported metadata adoption/release yetkisi taşımaz.

eval-candidate original terminal/quiescent task CAS captureını ayrı private ANALYSIS ownerına tam metadata ile import eder. Frozen source beyanı dispatch öncesi evaluator journalındaki ham girdinin digestine bağlıdır; verilen source metadata özgün journal ile bütün olarak karşılaştırılır. Hidden operator STDIO oracle ve feedback original modele verilmez. Sabit fixture bütün checkleri bir kez seçer; repair/best-run yok. Normal durable native leases/resources/fences kullanılır. Complete quiescent PASS/FAIL korunur; partial/flaky/unowned UNKNOWN olur. eval-inspect ayrı owner retained receiptsinden sonucu yeniden üretir. Actual goal-consistent hidden FAIL, false verified/success kaydı ve source/task unchanged testleri geçti.

Tam preregistered dataset orchestration/condition-build attestation, paired continuation deneyleri, localization labels ve reference p95, generic V0–V5/language coverage ile human/power/pilot kabulü bu dilimle kapanmaz. Bunların geliştirme altyapısı API anahtarı beklemez. C10 IMPLEMENTING; bütün anahtarsız kapsam veya releaseReady tamamlanmış sayılmaz. Kullanıcı kılavuzu docs/EVALUATION.md; karar ADR 0015.
