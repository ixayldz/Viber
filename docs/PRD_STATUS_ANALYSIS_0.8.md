# Viber PRD ve kalan işler analizi — 0.8.0-dev

Tarih: **8 Ekim 2026**. İncelenen commit: **049c4e4bab5910c8807ebcde95c6866539b937aa**. Yetkili kapsam: [PRD 1.1](../prd.md). Plan: [P01–P20](IMPLEMENTATION_PLAN.md). Bu dosya yeni 0.8 değerlendirmesidir; kullanıcı tarafından değiştirilmiş tarihsel 0.6 [analizi](PRD_STATUS_ANALYSIS.md) ve [puanları](PRD_STATUS_SCORE.json) değiştirilmedi.

## 1. Sonuç

**Uygulama production-ready değil. Anahtarsız işler bitmiş değil; API anahtarları tek engel değil.** Mevcut ürün güçlü guard'ları olan bir engineering CLI/agent temelidir. Özelliklerin bir kısmı çalışır; PRD'nin güven zinciri, günlük kullanım ve dağıtım kapsamı kısmen uygulanmış veya uygulanmamıştır.

İlk kararlı A–D kapsamındaki 41 FR/NFR dilimi için nitel uygulama skoru **1875/4100 = %45,73**; yuvarlak ifade **yaklaşık %46**. **Bu, production hazırlığı, kalan emek, güvenlik assurance'ı veya görev çözme başarısı yüzdesi değildir.** Bütün zorunlu kabul kanıtlarıyla DONE olan madde **0/41**; 36 madde PARTIAL, 5 madde NOT_IMPLEMENTED. Bu sayı özelliklerin hiç çalışmadığı anlamına gelmez; uygulanabilir kapsam ve kabul kanıtının birlikte tamamlanmadığını gösterir.

[Release kaydı](RELEASE_GATES.md): **Faz A CLOSED; B preview CLOSED; stable A–D CLOSED.** Bu incelemede çalıştırılan mevcut Windows `doctor --json` da `release_ready:false`, `live_apply:UNSUPPORTED`, `capture_consistency:BEST_EFFORT` bildiriyor. Provider/auth/local capability alanları canlı kabul beklediğini açık gösteriyor.

**Anahtarları bugün eklemek** remote inference ve canlı conformance testlerini mümkün kılar. Apply/restore, güvenilir observer, silme/GC, kaynak kontrolü, compaction/model switch, supervisor/TUI, privacy/config/extensions, bağımsız eval ve release paketlemesini tamamlamaz.

## 2. Analiz yöntemi ve kanıt sınırı

Bu bir PRD/kod/kayıt incelemesidir. PRD §8 kataloğu ve ilgili §4–7, §11–23, §25–29 kabul sözleşmeleri; CLI command dispatch, agent completion/check/context, auth credential storage, provider runtime, plan, store, delivery ve mevcut test/validation kayıtları karşılaştırıldı. Kodda mevcut explicit unsupported/untrusted sınırlar bulgu kabul edildi. Yeni full test/race/fuzz/security scan veya gerçek provider çağrısı yapılmadı; mevcut Windows doctor smoke çalıştırıldı ve yerel Windows JSON test kayıtları yeniden sayıldı. Daha önceki güvenlik taraması yeni tarama gibi sunulmuyor.

Puan ölçeği tarihsel 0.6 raporuyla aynı: 0 = davranış yok; 25 = temel/küçük dilim; 50 = çalışan kısıtlı uygulama, büyük eksik; 75 = geniş uygulama, maddi kapsam/kabul eksikleri; 100 = uygulanabilir gereksinim ve gerekli kabul kanıtı tam. Maddeler eşit ağırlıklıdır; 75 puanlı Git capture ile 0 puanlı compaction aynı ağırlıktadır. Bu öznel mühendislik sınıflaması person-day veya takvim tahmini vermez. Kapanan test/eklenen satır sayısına puan verilmedi.

İlk stable kapsam: FR-01…27 + FR-33 B dilimi + FR-38 D dilimi + NFR-01…12'nin A–D dilimleri = 41 madde. E–G sonraki deneylerdir; aşağıda ayrı listelenir. Tüm PRD için tek birleşik yüzde vermek B/F ve D/G dilimlerini ve deneyleri karıştıracağı için yapılmadı. Tarihsel 0.6 skor %40,24'tü; 0.7 plan/local runtime/token ledger ve 0.8 check/auth/stream/inspection dilimleri bu ölçekte **+5,49 yüzde puan** getiriyor. Aynı score bucket'ında kalan maddelerdeki geliştirmeler de aşağıda yazılıdır.

Hesaplanabilir satırlar ve toplamlar: [PRD_STATUS_SCORE_0.8.json](PRD_STATUS_SCORE_0.8.json).

### Mevcut doğrulama kanıtı

| Kanıt | Gözlenen sonuç | Kapanmayan kabul |
|---|---|---|
| Windows 0.8 full JSON turu | 443 test/alt test PASS; 2 SKIP; 0 FAIL | İki symlink host privilege fixture'ı SKIP; tüm PRD güvenlik matrisi değil |
| Final affected model/agent/CLI turu | 200 PASS; 1 opt-in Docker SKIP; 0 FAIL | Yeni full suite turu değil; Docker agent check önceki full turda PASS |
| İki kaydın birleşimi | 449 ayrı package/test kimliği için en az bir PASS; yalnız SKIP olan 2 kimlik; 0 FAIL | Son affected turda bazı testler tekrarlandı; 449 son final tek-tur sayısı değil |
| Gerçek Docker broker + registered agent check | Full turda pinned Linux engine üzerinde PASS; check current/EXIT_ZERO/verification UNKNOWN | Rootless/escape/fencing/OS matrix ve trusted observer tamam değil |
| Linux | Kayıtlı full race/vet/build ve final affected race/vet/build PASS | Trusted build container sonucu untrusted runner'ın tüm isolation assurance'ı değil |
| Darwin arm64 | Package/test/CLI cross-build COMPILE_ONLY | `/bin/true` ile native test çalıştırılmadı; macOS runtime/platform kabulü yok |
| README | Native Windows'ta 13 PowerShell blok grubu; source hash korunmuş, restored candidate aynı | Günlük kullanım TUI ve production installer kabulü değil |
| OpenAI/Anthropic gerçek API | NOT_RUN | API anahtarı/model erişimi ve canlı kabul gerekiyor |
| ChatGPT gerçek hesap login/inference | NOT_RUN | Uygun hesap ve kullanıcı consent'i gerekiyor |
| Kurulu gerçek Ollama modeli | NOT_RUN | Local model, endpoint, compute/hardware gerekiyor |
| Bağımsız model eval/pilot | NOT_RUN | Eval altyapısı, model erişimi ve insan katılımı gerekiyor |

449 sayımı sonucun görüldüğü **birleşim** tanımıdır: actual Docker agent check full turda PASS, opt-in olmayan final affected turda SKIP. SKIP bir çalıştırmada PASS'a dönüştürülmedi; full turun gözlenen kanıtı ve final affected kapsamı ayrı tutuldu. İki yalnız-SKIP kimliği workspace/fileguard symlink privilege fixture'larıdır. Ayrıntılı kalıcı kayıt: [VALIDATION.md](VALIDATION.md). Yerel raw log'lar ignored `.cache/windows-keyless-0.8-tests.jsonl` ve `.cache/windows-keyless-0.8-usage-final.jsonl`; bunlar commit'e dağıtılan release evidence bundle yerine geçmez.

## 3. Şu anda kullanıcıya sunulabilen çalışan dilimler

- **İzole aday üretme:** kullanıcı source/Git index'ine otomatik yazmadan immutable candidate, exact-byte diff/changeset/export; dirty/staged/untracked korunması ve stale patch reddi.
- **Dayanıklı görev çekirdeği:** ham input/revision, durable intents/receipts, journal/CAS/checkpoints, command dedup, pure replay, backup/fresh store restore ve explicit migration.
- **CLI kontrolü:** run/status/diff/pause/resume/cancel, requests/respond, inspect/replay/events, steer/revise, analysis/report ve terminal parent'tan fresh attempt.
- **Tek-worker plan:** typed DAG, dependency ve input/output/scope/criterion/check contract; deterministic node progression. Model-authored plan authority değildir.
- **Bounded model/tool protocol:** native read/list/search/outline/paging, whole-protocol context preflight, context-why/page, token reserve/settle; belirsiz usage restart/restore'da korunur.
- **Registered check:** operator tarafından sabitlenen check ID, pinned readonly offline Docker ve immutable bound stdout/stderr/result receipt. Model arbitrary argv/image seçemez.
- **Provider wiring:** OpenAI/Anthropic API ve declared-local Ollama ortak durable loop'a bağlı; JSON ve opt-in SSE/NDJSON terminal protocol fixtures var.
- **ChatGPT plan login kodu:** resmi SIWC/PKCE/state/nonce/JWT validation, saved profiles, select/catalog, refresh rotation/logout/revoke; private credential store. Gerçek hesap kabulü bekliyor.

Bunlar uçtan uca **limited candidate delivery** sağlar. [Normal agent final path](../internal/agent/session.go) `LimitedResultFinalized` ile kaliteyi **UNVERIFIED** tutar; trusted verification/goal coverage yoksa bound kullanıcı kararı ister. `--allow-unverified` explicit limited delivery'yi mümkün kılar, strict başarı sağlamaz; README örnekleri exit 2 gözlemiştir. Pure [verification predicate](../internal/verify/verify.go) guard'ları tanımlar; predicate unit test'i bu guard'ları gerçek backend'de sağlamış bir runtime değildir.

[Check implementation](../internal/agent/checks.go) `discovery_authority:UNTRUSTED_UNRESOLVED` ve `verification:UNKNOWN`; [final artifact](../internal/agent/completion.go) `MODEL_AUTHORED_UNREVIEWED` olarak kaydedilir. Exit0, stdout PASS veya modelin “bitti” metni independent coverage/quality kanıtı sayılmaz.

## 4. API anahtarı gereken kalan kabul işleri

Buradaki işleri **kod eksikleri** ile **canlı kabul eksikleri** olarak ayırmak gerekir. Adapter/CLI kodunun bulunması gerçek servis davranışının test edildiği anlamına gelmez.

| İş | Gerekli erişim | Mevcut kod | Kalan gerçek kabul | PRD |
|---|---|---|---|---|
| OpenAI doğrudan API | `OPENAI_API_KEY`, uygun project/model/quota/billing, ağ | Fixed official origin; env credential handle; Responses JSON/SSE; tool/result/continuation/usage parse | İlk inference, gerçek tool round-trip, current candidate/check loop, stream completion/interruption/cancel, refusal/auth/rate/quota/timeout, gerçek model limits ve observed usage | FR-24, NFR-04/05; P09/P18/P19 |
| Anthropic doğrudan API | `ANTHROPIC_API_KEY`, model/quota/billing, ağ | Messages JSON/SSE; ordered blocks, tool fragments, signed thinking, cache/usage arithmetic | Gerçek model/template/options profile, signed continuation/tool result, rate/quota/error/cancel, cache ve billing/token-count semantics, restore sonrası fresh context | FR-24, NFR-04/05; P09/P18/P19 |
| İki remote protokol için product eval | Her değerlendirilecek remote modelin erişimi/anahtarı; sabit budget/model sürümü | Protocol fixtures var; ürün evaluator yok | Eval altyapısı tamamlandıktan sonra bağımsız görev çözme/cohort/ablation; tüm attempts/cost/cache/latency ve unknown billing | NFR-12, §27; P18/P20 |
| Remote embedding/reranker/Gemini/external services seçilirse | Seçilen hizmetin anahtarı/hesabı | Bu opsiyonlar uygulanmadı | Önce ilgili E/F/G feature ve scoped egress/adapter, sonra hizmet kabulü | E/F/G; ilk A–D için bütün bu hizmetler zorunlu değil |

API credential **değerleri** context/journal/status'a yazılmaz; runtime environment handle kullanır. [CLI runtime](../internal/cli/task.go) kodu wiring kanıtıdır, canlı provider conformance değil. Fiyat, model ve capability adlarını tek sabit genel “desteklenir” vaadiyle genişletmek yerine her desteklenen profile/version için gerçek kabul kaydı gerekir. Bu incelemede fiyat veya provider usage çağrısı yapılmadı.

**Önemli sınır:** failure taxonomy, safe backoff/retry, model profile registry, token/price ledger ve real-test harness'ı yazmak için API anahtarı gerekmez. Yalnız gerçek servis/billing/model davranışının ölçülecek kısmı anahtar veya ilgili account access gerektirir. Anahtar olmaması bu altyapı backlog'unu ertelenmiş saymaz.

## 5. API anahtarı gerekmeyen, dış hesap/ortam/insan kabulü isteyen işler

| İş | API anahtarı | Gerekli dış koşul | Kalan kabul |
|---|---|---|---|
| ChatGPT coding-plan login | Gerekmez | Uygun ChatGPT hesabı, browser, kullanıcının plan usage consent'i ve ağ | Gerçek registration/login/callback/ID-token/refresh/logout/revoke/profile-select/models + tool inference, rate/usage/revoked-session kabulü |
| Ollama gerçek local model | Gerekmez | Kurulu model/endpoint, doğru template/tool parser, CPU/GPU/RAM; offline hazırlanmış bağımlılıklar | Gerçek model tool adherence, context/output/usage/cancel/stream, cloud proxy olmadığının profile kabulü |
| Native macOS | Gerekmez | macOS makinesi veya native CI runner | IPC/path/mount/permission/browser/credential-store/terminal/install smoke; cross-build yeterli değil |
| Güçlü backend/platform matrix | Gerekmez | Linux/WSL2/container/rootless ve desteklenecek filesystem/version ortamları | Hostile principal/escape/fencing/resource/power-loss conformance ve capability manifest |
| Artifact signing | Model API anahtarı gerekmez | İmzalama kimliği/sertifikası veya seçilmiş keyless signing OIDC/release infra | Signature doğrulama ve installer/update trust/rollback kabulü |
| Pilot/independent review | Gerekmez | Gerçek kullanıcı/evaluator katılımı ve model erişimi seçeneklerinden biri | PRD'nin önerdiği 5–10 geliştirici pilotu, rework/recovery/onboarding; pilot istatistiksel üstünlük kanıtı değil |

“API anahtarsız” burada “kimlik doğrulamasız, internetsiz veya kendiliğinden kabul edildi” anlamına gelmez. ChatGPT OAuth tokenları credential'dır; kullanıcı API key yaratmasa da protected credential lifecycle gerekir.

### Coding-plan girişi: mevcut kapsam ve sınırları

PRD dışındaki son kullanıcı isteğinin **OpenAI/ChatGPT planıyla giriş kodu** eklendi. `auth login/status/profiles/select/models/logout` resmi SIWC akışını kullanıyor; callback loopback, PKCE S256, state/nonce ve RS256 ID-token/JWKS doğrulaması var. Windows user DPAPI/private ACL ile saklar. [Unix implementation](../internal/auth/protect_unix.go) yalnız owner directory/file permission uygular ve **NOT_ENCRYPTED_AT_REST** bildirir; Linux/macOS OS credential-store/keyring entegrasyonu kalan anahtarsız iştir.

Uygun Plus/Pro kullanıcıları açık kaynak uygulamalarda ChatGPT plan kullanım izniyle API key vermeden inference yapabilir. Identity ve plan-usage scope'ları ayrı; sadece login başarılı olması inference yetkisi değildir. Ticari partner integration şu anda sınırlı trial/selected-partner erişimidir; her hosted/commercial dağıtım için açık-source yolunun otomatik uygulanacağı varsayılmaz. Kaynak: [resmi SIWC quickstart](https://developers.openai.com/siwc/quickstart).

Kod visible model catalog'u alıyor ancak runtime yalnız **`gpt-6.1-sol`** registered SIWC profile'ına izin veriyor. Catalog'da görünen her model Viber'de otomatik desteklenmiş değil. Çeşitli “coding planları” için genel subscription-provider abstraction ve Anthropic/başka sağlayıcı consumer-plan login yok; genişletme isteniyorsa önce sağlayıcının resmi yetkilendirme/usage contract'ı, sonra yeni adapter/profile ve live acceptance gerekir. Bütün abonelikleri API key alternatifi sayamayız.

Mevcut ChatGPT request profile `store:false`, `stream:true`, stateless history ve namespaced client tools kullanır; unsupported request alanları çıkarılır. Bu kısıtlar [resmi preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations) ile ayrı bir contract'tır. TUI “Continue with ChatGPT”, usage/quota/recovery ekranları henüz yok; CLI profile yönetimi çok kullanıcılı SaaS tenant/auth sistemi değildir. SaaS'ın tamamı ilk PRD CLI kapsamına eklenmedi.

## 6. Kalan anahtarsız uygulama backlog'u ve kapanış koşulları

Aşağıdaki 15 grup kalan ilk-stable işlerini toplar; FR/NFR kataloglarının sayısı veya efor tahmini değildir. Her grubun kodu ve deterministik/OS fixture'ları API anahtarsız geliştirilebilir. Gerçek model kalitesi ve bazı native/platform/human acceptance koşulları §4–5'te ayrı belirtilmiştir.

### K01 — Trusted verification ve hedef kapsamı (KRİTİK)

**PRD/plan:** FR-01/04/11, NFR-03; P02/P07/P10.

**Mevcut:** Check origin/explicit closure guard, registered Docker execution ve pure predicate var. Check receipt discovery UNTRUSTED_UNRESOLVED; verification UNKNOWN.

**Eksik:** Test/helper/config/discovery closure incelemesi; authorized check revision; V0-V5 seçimi, baseline failures, discovered/executed/selected/skipped/rerun kayıtları; korunan sonuç kanalı; independent observer/scoped human receipt; semantik goal coverage. Bütün writer'lar freeze edilmeli.

**Kapanış kanıtı:** Fake PASS, exit0, zero discovery, weakened test, farklı candidate/env/spec receipt ve canlı child VERIFIED üretememeli; gerçek bağımsız check geçince current exact candidate için strict başarı üretilebilmeli.

**Bağımlılık/erişim:** P03/P06 ile birlikte; foundation ve observer fixture'ları API anahtarsız.

### K02 — Sandbox, süreç ağacı ve fencing (KRİTİK)

**PRD/plan:** FR-07/08, NFR-01/02/05; P03.

**Mevcut:** Pinned readonly/nonroot/network-none Docker, timeout/output/resource sınırı ve cleanup mevcut.

**Eksik:** Backend/version/rootless capability matrix; process start identity, eski epoch/generation publish engeli, orphan discovery/drain, credential/socket/host mount escape; suspend/reboot/clock-domain/lease kabulü.

**Kapanış kanıtı:** Eski owner/child hiçbir yeni etki yayınlayamamalı; mount/namespace/symlink/junction/case kaçışları gerçek OS backend'inde reddedilmeli; capability sessiz düşmemeli.

**Bağımlılık/erişim:** Docker/WSL/Linux test ortamı gerekir; model anahtarı gerekmez.

### K03 — Retention, delete, GC ve backup silme zinciri (KRİTİK)

**PRD/plan:** FR-13/26, NFR-06/07/09/11; P04/P16.

**Mevcut:** Backup/fresh restore/migration var; backup deletion policy UNSUPPORTED_NO_DELETIONS, watermark 0.

**Eksik:** Minimum journal envelope ile silinebilir payload ayrımı; shared refs/pins/GC; active operations quiescence; task-scoped tombstone/deletion watermark; index/summary/candidate/cache/support/yönetilen backup türevlerinin purge'u.

**Kapanış kanıtı:** Silinmiş scope eski backup restore ile geri gelmemeli; başka scope'un pin'i korunmalı; PARTIAL/PENDING deletion açık gösterilmeli; silinen receipt completion'a katılmamalı.

**Bağımlılık/erişim:** P03 ile quiescence; P05 control reserve. Tamamı fixture ve local disk ile geliştirilir.

### K04 — Global kaynak/maliyet ve kontrol rezervi (KRİTİK)

**PRD/plan:** FR-09, NFR-06; P05.

**Mevcut:** Store-wide token ledger atomik; unknown korunuyor; kanıtlı transport öncesi no-dispatch sıfır charge var.

**Eksik:** Sürümlü price/para birimi lineage, CPU/disk/elapsed ve child allocations; low-watermark/quota; fiziksel disk/control reserve; operator-authorized UNKNOWN reconcile/write-off.

**Kapanış kanıtı:** Concurrent parent/child work bütçe aşmamalı; iş bütçesi/disk bittiğinde cancel/reconcile/checkpoint için ölçülmüş ayrılmış kaynak çalışmalı; uncertain charge sıfır sayılmamalı.

**Bağımlılık/erişim:** Ledger ve fault injection anahtarsız; gerçek remote usage/billing eşleşmesi canlı provider kabulü.

### K05 — Live apply ve scoped workspace restore (KRİTİK)

**PRD/plan:** FR-21, NFR-01/03; P08.

**Mevcut:** Diff/changeset/export candidate-only çalışıyor; apply/restore UNSUPPORTED.

**Eksik:** Exclusive target/repo mutex; baseline-current-candidate preimage/three-way conflict; file-by-file apply intent/receipt; partial-write crash reconciliation; yalnız harness değişikliklerinin restore'u; merge yeni candidate olarak reverify.

**Kapanış kanıtı:** Kill-after-first-write ve kullanıcı arada edit yaptı senaryosunda veri kaybı/overwrite olmamalı. Apply sonrası receipt delivered bytes'a bağlı olmalı; eski quality yeni merge'e taşınmamalı.

**Bağımlılık/erişim:** P03/P04/P06/P07; API anahtarı gerekmez. store-restore bu işin yerine geçmez.

### K06 — Environment, freshness ve plan mutex (YÜKSEK)

**PRD/plan:** FR-05/15/33 B, NFR-02; P06/P12.

**Mevcut:** Read-set/stale rejection ve tek-worker typed DAG var; mutex isimleri plan metadata'sında.

**Eksik:** Environment/toolchain/dependency/config/lockfile/generated-output fingerprints; selective invalidation; watcher-loss direct read; prepare/apply/freeze; gerçek resource/target mutex enforcement.

**Kapanış kanıtı:** Lockfile/helper/config/env değişince ilgili kanıt current kalmamalı; missed watcher kör freshness üretmemeli; pending writers freeze'i engellemeli.

**Bağımlılık/erişim:** Temel tek-agent dilimi anahtarsız; F çok-worker sonradan ayrı.

### K07 — Context compiler, privacy ve hydration (YÜKSEK)

**PRD/plan:** FR-13/14/17, NFR-06/09; P11/P16.

**Mevcut:** Whole-protocol preflight, canonical digest, context-why/page ve byte estimator var.

**Eksik:** Provider tokenizer/profile kalibrasyon altyapısı; sensitivity hard filters; optional rank/pack, stable prefix/cache ölçümü, pin/unpin/hydration; typed DevelopmentEpoch/work intervals.

**Kapanış kanıtı:** Zorunlu kriter/policy/protocol bölünmemeli; current/historical/unavailable ayrılmalı; yetkisiz source ve derived metadata egress'e girmemeli.

**Bağımlılık/erişim:** Compiler, tokenizer fixture ve pin UX anahtarsız; gerçek provider token/cache ölçümü remote anahtar veya uygun plan hesabı.

### K08 — Compaction ve güvenli model değiştirme (YÜKSEK)

**PRD/plan:** FR-16/18, NFR-12; P12/P13.

**Mevcut:** Compactor yok; continuation aynı provider/model'e scoped, çapraz taşıma reddediliyor.

**Eksik:** Bounded compactor/summary verifier; authoritative constraint/receipt/unknown preservation; tamamlanmış tool boundary'de model switch; model/privacy lock; context/output/budget yeniden compile; affected evidence admissibility.

**Kapanış kanıtı:** Missing constraint/ref/pending call summary reddedilmeli. Kill-user-edit-resume-different-model demosu kullanıcı byte'larını korumalı. Paired continuation eval gerekli.

**Bağımlılık/erişim:** Fixture/local fake adapters ile yazılır; gerçek davranış/kalite deneyi gerçek model erişimi ister.

### K09 — Supervisor, detach/attach ve JSONL reconnect (YÜKSEK)

**PRD/plan:** FR-02/12/25, NFR-04/10; P14.

**Mevcut:** Secure owner commands/IPC ve durable event paging var; serve supervised task lifecycle'ın tamamı değil.

**Eksik:** Background task supervisor, detach/attach; stream/event cursor handshake, gap+snapshot resync; bounded slow/disconnected clients; multi-task single writer/target mutex; orphan lifecycle.

**Kapanış kanıtı:** UI/CLI kopunca task kaybolmamalı; reconnect retained sequence'i atlamamalı; retention gap açık olmalı; yavaş client kernel'i bloklamamalı.

**Bağımlılık/erişim:** P03/P10/P13 ve P04 retention; bütünü anahtarsız.

### K10 — Günlük kullanım TUI (YÜKSEK)

**PRD/plan:** FR-19/20/22, NFR-06; P15.

**Mevcut:** Komut satırı lifecycle ve steering var; TUI yok.

**Eksik:** Composer, @file fuzzy refs, slash commands, prompt queue, status/diff/request/why/budget, model/restore ekranları; keyboard/resize/large log; bounded live deltas ve güvenli shell quick mode.

**Kapanış kanıtı:** Task/change/verification/pending decision görünür olmalı; UNKNOWN gizlenmemeli; Ctrl+C ve queue scope'u doğru çalışmalı; UI crash kernel task'ını silmemeli.

**Bağımlılık/erişim:** P08/P13/P14; provider fixtures ile tamamlanabilir.

### K11 — Config, onboarding ve genel privacy (YÜKSEK)

**PRD/plan:** FR-23/26, NFR-05/09/11; P16.

**Mevcut:** Remote consent/provider/path gate; doctor ve auth CLI; telemetry/training OFF; belirli credential-name filtreleri var.

**Eksik:** User/system/project/task/CLI authority intersection; inference/embedding/reranker/MCP/telemetry/training/retention/sensitive-path/cross-project bağımsız axes; secret-safe support/export preview; derived privacy lineage; Linux/macOS OS keyring.

**Kapanış kanıtı:** Repo config üst yetkiyi genişletmemeli; general secret/sensitivity filtresi sadece birkaç dosya adına dayanmamalı; tokenlar Unix owner-only plaintext ile kalmamalı; export policy source'tan bağımsız aşılmamalı.

**Bağımlılık/erişim:** Geliştirme anahtarsız; native platform credential store kabulü gerçek OS gerekir.

### K12 — Dil outline ve sınırlı extension sınırı (ORTA/YÜKSEK)

**PRD/plan:** FR-27/38 D, NFR-09; P16/P17.

**Mevcut:** TS/JS/Python lexical source-bound outline var; extension runtime yok.

**Eksik:** Outline coverage/freshness/unsupported syntax matrix; D asgari version-pinned scoped tool/extension interface, isolated process/RPC/quota/effect descriptor. Derin AST/LSP graph genişlemesi E'de ayrı.

**Kapanış kanıtı:** Parser/index sonucu eski veya yetkisiz kaynak için current authority üretmemeli. Tool annotation hint olmalı; yeni effect descriptor eski approval'ı kullanmamalı; plugin store'a direkt yazmamalı.

**Bağımlılık/erişim:** Native/local fake extension ile anahtarsız; SaaS connector seçilirse o connector hesabı ayrıca.

### K13 — Provider hata ve profile mühendisliği (YÜKSEK)

**PRD/plan:** FR-06/24, NFR-04/05; P09.

**Mevcut:** JSON/SSE/NDJSON terminal parsers, stream quota ve cancel/UNKNOWN fixture'ları var; ChatGPT tek registered model profile'ı.

**Eksik:** Typed refusal/auth/rate/quota/timeout/retry classes ve safe backoff; protocol/model capability versioning; first-use smoke ve supported-profile matrisi; live UI transport/resume mühendisliği.

**Kapanış kanıtı:** Yeni/unsupported event fail-closed olmalı; yarım tool arguments dispatch edilmemeli; post-dispatch unknown blind retry yaratmamalı; sessiz provider/model fallback olmamalı.

**Bağımlılık/erişim:** Hata vector, fake server, taxonomy ve UI kodu anahtarsız. Uçtan uca model/provider kabulü ayrı.

### K14 — Bağımsız eval ve reference performance (YÜKSEK)

**PRD/plan:** NFR-08/12, FR-11; P18.

**Mevcut:** Meaningful unit/crash/loopback tests var; bağımsız ürün evaluator ve p95 benchmark yok.

**Eksik:** Eval manifest/run harness, protected hidden evaluator, solution/control denominator, all attempts cost/compute/rework, holdout/cutoff/preregistration, paired ablations/uncertainty/adoption/rollback; reference cold/warm p95.

**Kapanış kanıtı:** Kendi VERIFIED/final beyanı başarı sayılmamalı; unsupported/timeout assigned solution paydadan atılmamalı; en iyi run seçilmemeli; unknown charge sıfır olmamalı. CLI/context latency ölçülmeli.

**Bağımlılık/erişim:** Altyapı ve deterministic/local fixture performance anahtarsız. Gerçek model karşılaştırması erişim/compute, insan rework/pilot insan katılımı ister.

### K15 — Paketleme ve release evidence (YÜKSEK)

**PRD/plan:** FR-23, NFR-01/07/10; P19/P20.

**Mevcut:** Pinned CI workflow ve Windows/Linux build; Darwin cross-build var; dev version, release_ready false.

**Eksik:** Reproducible packages/install/update/uninstall/downgrade/rollback, SBOM/dependency licenses/checksum/signature, exact revision/schema/policy/backend/provider/platform evidence bundle; offline installed smoke ve native matrix.

**Kapanış kanıtı:** Bütün zorunlu kabul PASS ve bağımsız review; SKIP/UNKNOWN/DEFERRED kapı açmamalı. Paket ile tested revision aynı olmalı. Root'ta LICENSE dosyası bulunmuyor; dağıtım lisansı kararı/metadata eksik.

**Bağımlılık/erişim:** Build/package/evidence altyapısı anahtarsız; imzalama kimliği, native macOS ve kullanıcı pilotu dış kabul.

## 7. PRD gereksinim bazında tam ilk-stable matrisi

Bütün satırlarda kalan anahtarsız geliştirme veya kabul altyapısı vardır. 100 puanlı satır yoktur. FR-24'ün gerçek remote/local kabulü ve NFR-12'nin gerçek model ölçümü ayrıca erişim gerektirir; diğer satırların API anahtarını beklemesi gerektiği varsayılmaz.

| ID | PRD gereksinimi | Uygulama puanı | Mevcut çalışan dilim | Kalan kapsam/kabul | Paket |
|---|---|---|---|---|---|
| FR-01 | Ham input/revision ve source-bound kriterler | 50 | Ham talimat ve revision CAS'a kaydediliyor; gereksinimler source span'a bağlı, boş niyet/kriter reddediliyor. | Semantik requirement/constraint çıkarımı, kullanıcı hedefinin eksiksiz kapsandığının bağımsız incelemesi ve kritik kriterlerin kaynak/insan kanıtı. | P01/P02/P10 |
| FR-02 | Tek writer kernel ve command/event API | 75 | Tek metadata owner, OS lock, transaction-atomic command/event, saf reducer, task/global sequence ve command dedup var. | Canonical nesne/event kataloğunun tamamı, supervisor altında çok görev koordinasyonu ve repo/target mutex kabulü. | P01/P04/P14 |
| FR-03 | Dirty/staged/unstaged/untracked baseline | 75 | Dirty/staged/unstaged/untracked exact bytes; native Git index v2/v3/v4, SHA-256 ve worktree capture var. | BEST_EFFORT capture'ın sınırları; LFS/submodule/sparse/generated/mode/CRLF/case ve desteklenen platformların tam matrisi. | P06/P08 |
| FR-04 | İzole immutable candidate | 75 | Scope'a bağlı CAS, immutable candidate manifest ve canlı kaynak dışına materialization var. | Tüm writer/pending mutation freeze'i, process-tree quiescence, backend lifecycle ve bütün publication crash sınırlarının kabulü. | P03/P06 |
| FR-05 | Read/write-set ve stale patch | 75 | FILE/ABSENT/LISTING read precondition, bounded exact reads, stale proposal reddi ve korunan check kapsamı guard'ı var. | Genel tool/generated-output write-set, transaction prepare/apply/freeze ve gerçek filesystem yarış/fencing matrisi. | P06/P08 |
| FR-06 | Durable intent/attempt/receipt ve recovery | 50 | Durable intent/reserve/receipt, UNKNOWN bloklama, inspection replay; terminal parent'tan yeni task attempt ve interrupted creation uzlaşması var. | Task attempt'tan ayrı bütün operation attempt/retry sınıfları; gerçek backend/external effect reconciliation ve yetkili UNKNOWN çözümü. | P04/P05/P10 |
| FR-07 | Process/FS/network/credential/resource sandbox | 50 | Gerçek Docker engineering profili readonly/nonroot/network-none, pinned image, sınırlı kaynak/output/timeout ve cleanup uygular. | Escape/child/namespace/credential/mount/rootless/resource matrisi, backend version floor, orphan drain ve process fencing kabulü. | P03 |
| FR-08 | Capability gate, approval, epoch fencing | 50 | Capability/restriction intersection, bound scoped one-shot approval, policy epoch/generation ve steering barrier var. | Backend dispatch/publish fencing, clock-domain/reboot leases ve bütün config/credential/extension authority yolları. | P03/P05/P16 |
| FR-09 | Global resource ledger | 50 | Store-wide atomik token reserve/settle/UNKNOWN ledger; dedup, overage, proof-bound KERNEL_NO_DISPATCH zero settlement var. | Para/price-version, CPU/disk/time/child allocation; fiziksel control reserve ve yetkili UNKNOWN reconciliation/write-off. | P05 |
| FR-10 | Lexical/path/range ve bounded paging | 75 | Exact-byte file/range/list/search, snapshot/read-set bağlı cursor ve bounded native tool/artifact sayfaları var. | Bütün artifact türleri için ortak hydrate/page yetki/availability sözleşmesi ve büyük repo/retention-gap kabulü. | P06/P11 |
| FR-11 | Check/receipt/quality ve ayrı fulfillment | 50 | Pure quality/fulfillment predicate, mutation öncesi operator check origin, readonly registered Docker check ve immutable receipt var. | Trusted discovery/selection/result channel, bağımsız observer, V0-V5/goal coverage, baseline/flaky bütün attempts; runtime strict VERIFIED yolu. | P02/P07 |
| FR-12 | Lifecycle CLI ve JSONL | 75 | run/status/diff/pause/resume/cancel/inspect/replay/events/requests/respond; fresh attempt ve analysis/report çalışıyor. | PRD'nin tam sürümlü canlı JSONL event sözleşmesi, cursor reconnect/gap-resync ve uygun headless compatibility/exit matrisi. | P10/P14 |
| FR-13 | Typed checkpoint ve deterministic replay | 75 | Typed checkpoint, journal integrity, deterministic current/historical replay, backup/fresh restore ve explicit migration var. | Tam DevelopmentEpoch/work interval kayıtları; silinmiş payload availability ve bütün gerekli typed state nesnelerinin lifecycle'ı. | P04/P11 |
| FR-14 | Context preflight/compiler/manifest/prefix | 50 | Tam canonical protocol/spec/policy preflight, konservatif byte üst sınırı, request/manifest digest ve inclusion explanation var. | Provider tokenizer kalibrasyonu, sensitivity hard filter, ranking/optional packing, stable prefix/cache ölçümü ve optimize compiler. | P11 |
| FR-15 | Snapshot/environment freshness | 25 | Source/candidate/index/ignore değişimlerine konservatif freshness ve stale resume/proposal/check reddi var. | Typed environment/toolchain/config/lockfile/dependency fingerprint; selective invalidation, generated outputs ve watcher-loss direct-read fallback. | P06/P12 |
| FR-16 | Bounded compaction | 0 | Compactor veya summary verifier uygulanmış değil. | Bounded compaction, authoritative typed state koruması, source-ref/constraint/pending-tool verifier ve paired continuation kabulü. | P12 |
| FR-17 | Context/history pin/page ve explanation | 50 | context-why, retained canonical context-page, exact history/events/read paging ve digest/inclusion reason var. | User pin/unpin, ortak hydration/availability, context omission/optional ranking UX ve retention sonrası historical/unavailable ayrımı. | P11/P13 |
| FR-18 | Safe model switch ve privacy/model lock | 25 | Opaque continuation provider/model profile'a bağlı; çapraz provider ve uygunsuz fallback reddediliyor. | Aktif task'ta safe model switch, complete protocol boundary, privacy/model lock, context/budget yeniden compile ve receipt admissibility. | P09/P13 |
| FR-19 | Durable steering/pause/revision ve queue | 50 | Durable steer/revise, raw input-before-barrier, aktif owner pause/cancel ve eski approval invalidation var. | Semantik replan, prompt queue/TUI steering, backend race/fencing ve ikinci Ctrl+C supervisor force-stop kabulü. | P10/P15 |
| FR-20 | Autonomy ve bağımsız isolation | 50 | review/guided/auto aday mutation admission'a bağlı; analysis task readonly; sınırlı Docker isolation ayrı tanımlı. | Genel effect surface ve tüm autonomy/isolation profil matrisi; yetkili shell quick mode ve kullanıcı delivery kontrolü. | P03/P10/P15 |
| FR-21 | Apply/restore ve delivery receipt | 25 | Candidate-only changeset/diff/export ve limited delivery artifact mevcut; canlı source yazılmıyor. | Live apply/scoped restore, exclusive target mutex, preimage/three-way conflict, dosya bazlı durable receipt/crash recovery ve merge sonrası reverify. | P08/P15 |
| FR-22 | TUI, @file, slash, diff/status | 0 | CLI var; PRD'nin interaktif TUI'si yok. | Composer, @file, slash commands, prompt queue, diff/status/why, model/restore UX, keyboard/resize ve bounded live log kabulü. | P15 |
| FR-23 | Doctor/onboarding/support matrix | 50 | doctor/help/README, auth login/status/profiles/select/models/logout ve backend/provider pending/unsupported gösterimi var. | Tam onboarding ve config resolution; installed binary setup/update/uninstall, actual platform support matrix ve doctor'ın tüm ortam kontrolleri. | P16/P19 |
| FR-24 | İki remote + bir local conformance | 75 | OpenAI Responses, Anthropic Messages, Ollama adapter ve CLI; JSON/SSE/NDJSON offline/loopback conformance; resmi ChatGPT SIWC auth/runtime kodu var. | İki ayrı remote protokol + gerçek kurulu local model kabulü; seçili provider/model profile, auth/rate/quota/usage/cancel/recovery gerçek testleri. | P09/P19 |
| FR-25 | Secure IPC, supervisor, detach/attach | 50 | SID/UID/PID peer-auth named pipe/Unix socket, OS owner lock ve secure local owner commands var. | Background supervisor, detach/attach, persistent stream cursor/reconnect/gap-resync, slow-client backpressure ve orphan cleanup. | P14 |
| FR-26 | Retention/delete/export | 25 | Candidate export, CAS+SQLite snapshot backup, temiz store restore ve migration kanıtı var. | Retention/delete/tombstone, metadata-payload ayrımı, shared-reference-aware GC, managed-copy purge ve deletion watermark ile restore no-revival. | P04/P16 |
| FR-27 | TS/JS/Python outline/symbols | 50 | TS/JS/Python lexical declarations; source digest, exact byte span/line ve snapshot'a bağlı symbol ID; sınırlı output var. | Derin dil çözümlemesi/unsupported syntax coverage ve freshness kabulü; güvenli external parser/LSP ve sensitivity entegrasyonu. AST/resolved graph FR-28'dir. | P17 |
| FR-33 | Typed plan DAG/dependency/mutex — B dilimi | 50 | B dilimi: typed bounded tek-worker DAG, dependency/input-output/scope/criterion/check contract ve deterministik node progression var. | B dilimindeki resource/target mutex enforcement ve pending writer freeze entegrasyonu; çok-worker/integration queue F kapsamı ayrı. | P06 |
| FR-38 | Pinned extension/effect sınırı — D dilimi | 0 | Sınırlı D extension runtime/MCP contract uygulanmış değil. | D: sürümü pinned, scoped effect/authority ve isolated tool extension boundary; G'deki geniş MCP/team kapsamından ayrı kabul. | P16 |
| NFR-01 | Fault injection'da veri koruma | 50 | Candidate/CAS/journal/migration corruption ve subprocess crash fixture'ları kullanıcı source'unu koruyor. | Full boundary kill/power-loss, live apply partial write, publish/fencing ve tüm filesystem/platform veri koruma matrisi. | P03/P04/P06/P08/P19 |
| NFR-02 | Eski authority admission reddi | 50 | Stale policy/spec/generation, old approval, source/candidate bağları ve input barrier reddi var. | Gerçek backend dispatch/publish fence; expired lease/clock-domain/suspend/reboot ve orphan process kabulü. | P03/P05/P14 |
| NFR-03 | False VERIFIED engeli | 50 | Model PASS/exit0/partial response kaliteyi yükseltmiyor; pure predicate guard ve UNKNOWN final blokları var. | Admissible trusted receipt üreten uçtan uca runtime, independent observer/goal review ve tüm F12/F13/F25-F28/F36 saldırı kapsamı. | P02/P07/P08/P18 |
| NFR-04 | Recoverable provider/plugin/environment failure | 50 | Malformed/partial stream fail-closed; cancellation raw prefix'i ve UNKNOWN reserve'u korur; fresh restore/restart mümkün. | Typed provider auth/rate/quota/retry/backoff sınıfları, gerçek charge/effect reconciliation ve supervisor/plugin recovery. | P05/P09/P10/P14 |
| NFR-05 | Her profile egress gate | 50 | Remote consent/provider gates, fixed origin, no proxy/redirect/fallback; Docker network-none; loopback local declaration var. | Bütün execution/extension/index/export egress yolları, tam OS profile conformance ve local endpoint'in gerçek cloud-free kabulü. | P03/P09/P16 |
| NFR-06 | Bounded disk/log/context/UI | 50 | Context/output/wire/event/page/plan/step sınırları ve token reservation var. | Store disk quota/GC/low-watermark/physical control reserve, CPU/child ledger, bounded UI ve slow-client buffers. | P04/P05/P11/P14/P15 |
| NFR-07 | Pinned schema/migration/restore backup | 75 | Format pin, v1->v2 explicit migration/backup/recovery, fresh restore ve historical checkpoint integrity var. | Install/update/downgrade/power-loss matrisi, deletion-aware backup/restore ve reproducible release artifact kabulü. | P01/P04/P19 |
| NFR-08 | Warm CLI/context p95 | 0 | PRD reference-machine p95 performans ölçüm paketi yok. | Warm CLI <1s/context <250ms hedeflerini tanımlı repo/token/cache/CPU/disk manifest'iyle ölç; sonuç ve hedefi ayrı raporla. | P18/P19 |
| NFR-09 | Source/index/memory erişim kontrolü | 25 | Task-scoped CAS/path access, private owner IPC/auth store; belirli credential dosya adları capture/proposal'a giremiyor. | Genel sensitivity/secret politika filtresi, index/context/derived memory/export/backup privacy lineage; Unix OS keyring. | P03/P11/P16/P17 |
| NFR-10 | Versioned JSONL/cursor/exits | 50 | Schema-versioned structured JSON, task/store sequence ve durable event paging/exit ayrımları var. | Tam JSONL envelope/compatibility, event ID dedup, persistent reconnect, retention gap+state snapshot ve backpressure. | P01/P10/P14/P19 |
| NFR-11 | Default OFF ve secret-safe export | 25 | Telemetry/training default OFF; API değerleri yerine env handle, private SIWC credentials ve provider error redaction var. | Bağımsız privacy axes/config, support/export preview+general redaction, derived deletion; Unix token encryption/OS keyring. | P04/P16 |
| NFR-12 | Controlled ablation/all attempts cost | 0 | Bağımsız ürün benchmark/paired ablation/holdout ve tüm denemeler maliyet paketi uygulanmış değil. | Preregistered solution/control cohort, bağımsız evaluator, all-attempt cost/compute/rework, uncertainty/adoption/rollback ölçümü. | P12/P13/P17/P18 |

### Satırların kod kanıtı

Kod yolu varlığı DONE değildir; mevcut davranış ve açık sınırın incelendiği yeri gösterir. Test kanıtları [VALIDATION.md](VALIDATION.md) ve [RELEASE_GATES.md](RELEASE_GATES.md)'de kapsamıyla kayıtlıdır.

| ID | Kod / kabul kaydı |
|---|---|
| FR-01 | [internal/agent/steering.go](../internal/agent/steering.go)<br>[internal/contracts/types.go](../internal/contracts/types.go) |
| FR-02 | [internal/store/store.go](../internal/store/store.go)<br>[internal/kernel/reducer.go](../internal/kernel/reducer.go) |
| FR-03 | [internal/workspace/git.go](../internal/workspace/git.go)<br>[internal/workspace/snapshot.go](../internal/workspace/snapshot.go) |
| FR-04 | [internal/artifact/archive.go](../internal/artifact/archive.go)<br>[internal/agent/checks.go](../internal/agent/checks.go) |
| FR-05 | [internal/workspace/proposal.go](../internal/workspace/proposal.go)<br>[internal/agent/protection.go](../internal/agent/protection.go) |
| FR-06 | [internal/agent/attempt.go](../internal/agent/attempt.go)<br>[internal/agent/session.go](../internal/agent/session.go)<br>[internal/agent/tokens.go](../internal/agent/tokens.go) |
| FR-07 | [internal/runner/docker.go](../internal/runner/docker.go) |
| FR-08 | [internal/policy/policy.go](../internal/policy/policy.go)<br>[internal/agent/requests.go](../internal/agent/requests.go) |
| FR-09 | [internal/contracts/tokens.go](../internal/contracts/tokens.go)<br>[internal/store/tokens.go](../internal/store/tokens.go)<br>[internal/agent/tokens.go](../internal/agent/tokens.go) |
| FR-10 | [internal/agent/native_pages.go](../internal/agent/native_pages.go)<br>[internal/agent/observe.go](../internal/agent/observe.go) |
| FR-11 | [internal/verify/verify.go](../internal/verify/verify.go)<br>[internal/verify/origin.go](../internal/verify/origin.go)<br>[internal/agent/checks.go](../internal/agent/checks.go) |
| FR-12 | [internal/cli/cli.go](../internal/cli/cli.go)<br>[internal/cli/history.go](../internal/cli/history.go) |
| FR-13 | [internal/store/checkpoint.go](../internal/store/checkpoint.go)<br>[internal/store/migration.go](../internal/store/migration.go) |
| FR-14 | [internal/agent/context.go](../internal/agent/context.go)<br>[internal/context/compiler.go](../internal/context/compiler.go) |
| FR-15 | [internal/agent/runtime.go](../internal/agent/runtime.go)<br>[internal/workspace/snapshot.go](../internal/workspace/snapshot.go) |
| FR-16 | [internal/agent/context.go](../internal/agent/context.go) |
| FR-17 | [internal/agent/observe.go](../internal/agent/observe.go)<br>[internal/cli/observe.go](../internal/cli/observe.go) |
| FR-18 | [internal/model/protocol.go](../internal/model/protocol.go)<br>[internal/agent/runtime.go](../internal/agent/runtime.go) |
| FR-19 | [internal/agent/steering.go](../internal/agent/steering.go)<br>[internal/owner/steering.go](../internal/owner/steering.go) |
| FR-20 | [internal/agent/tools.go](../internal/agent/tools.go)<br>[internal/runner/docker.go](../internal/runner/docker.go) |
| FR-21 | [internal/delivery/changeset.go](../internal/delivery/changeset.go)<br>[internal/cli/cli.go](../internal/cli/cli.go) |
| FR-22 | [internal/cli/cli.go](../internal/cli/cli.go) |
| FR-23 | [internal/cli/auth.go](../internal/cli/auth.go)<br>[internal/cli/cli.go](../internal/cli/cli.go)<br>[README.md](../README.md) |
| FR-24 | [internal/model/stream.go](../internal/model/stream.go)<br>[internal/model/chatgpt.go](../internal/model/chatgpt.go)<br>[internal/cli/task.go](../internal/cli/task.go)<br>[internal/auth/client.go](../internal/auth/client.go) |
| FR-25 | [internal/ipc/ipc.go](../internal/ipc/ipc.go)<br>[internal/owner/owner.go](../internal/owner/owner.go) |
| FR-26 | [internal/store/backup.go](../internal/store/backup.go)<br>[internal/artifact/export.go](../internal/artifact/export.go)<br>[internal/cli/cli.go](../internal/cli/cli.go) |
| FR-27 | [internal/outline/outline.go](../internal/outline/outline.go)<br>[internal/agent/native_pages.go](../internal/agent/native_pages.go) |
| FR-33 | [internal/plan/plan.go](../internal/plan/plan.go)<br>[internal/agent/plan.go](../internal/agent/plan.go) |
| FR-38 | [internal/cli/cli.go](../internal/cli/cli.go) |
| NFR-01 | [internal/store/migration_test.go](../internal/store/migration_test.go)<br>[internal/agent/backup_test.go](../internal/agent/backup_test.go)<br>[internal/workspace/workspace_test.go](../internal/workspace/workspace_test.go) |
| NFR-02 | [internal/policy/policy_test.go](../internal/policy/policy_test.go)<br>[internal/runner/docker_test.go](../internal/runner/docker_test.go) |
| NFR-03 | [internal/verify/verify.go](../internal/verify/verify.go)<br>[internal/agent/checks.go](../internal/agent/checks.go)<br>[internal/agent/session.go](../internal/agent/session.go) |
| NFR-04 | [internal/model/stream.go](../internal/model/stream.go)<br>[internal/agent/stream_test.go](../internal/agent/stream_test.go)<br>[internal/agent/tokens.go](../internal/agent/tokens.go) |
| NFR-05 | [internal/model/http.go](../internal/model/http.go)<br>[internal/policy/policy.go](../internal/policy/policy.go)<br>[internal/runner/docker.go](../internal/runner/docker.go) |
| NFR-06 | [internal/contracts/tokens.go](../internal/contracts/tokens.go)<br>[internal/agent/context.go](../internal/agent/context.go)<br>[internal/model/stream.go](../internal/model/stream.go) |
| NFR-07 | [internal/store/migration.go](../internal/store/migration.go)<br>[internal/store/backup.go](../internal/store/backup.go) |
| NFR-08 | [docs/VALIDATION.md](../docs/VALIDATION.md) |
| NFR-09 | [internal/artifact/archive.go](../internal/artifact/archive.go)<br>[internal/workspace/ignore.go](../internal/workspace/ignore.go)<br>[internal/auth/protect_unix.go](../internal/auth/protect_unix.go) |
| NFR-10 | [internal/cli/history.go](../internal/cli/history.go)<br>[internal/ipc/ipc.go](../internal/ipc/ipc.go) |
| NFR-11 | [internal/cli/cli.go](../internal/cli/cli.go)<br>[internal/auth/store.go](../internal/auth/store.go)<br>[internal/auth/protect_unix.go](../internal/auth/protect_unix.go) |
| NFR-12 | [docs/IMPLEMENTATION_PLAN.md](../docs/IMPLEMENTATION_PLAN.md)<br>[prd.md](../prd.md) |

## 8. E–G ve tüm PRD'de kalan sonraki kapsam

İlk stable A–D'yi tamamlamak ile tüm vizyonu tamamlamak farklıdır. PRD §26 deneylerini release ön koşulu gibi karıştırmıyoruz; kapalı deneyler core guard'larını da bypass edemez. Bu 13 madde henüz uygulanmış değil:

| ID | E-G kapsamı | Durum | Erişim ayrımı |
|---|---|---|---|
| FR-28 | AST/LSP resolved graph ve freshness | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-29 | Git history/co-change/issue memory ve cutoff | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-30 | Semantic embedding/retrieval/reranker | UYGULANMADI | Local embedding/reranker ile anahtarsız olabilir; remote seçenek hesap/anahtar ister. |
| FR-31 | Runtime trace/coverage ve test selection | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-32 | Yeni repo blueprint generation/eval | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-34 | Bounded workers/isolated writers/integration | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-35 | TTL/generation/clock-domain AgentLease | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-36 | Sticky/phase routing ve measured profile | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-37 | ExternalAgentAdapter patch worker | UYGULANMADI | Adapter/izolasyon anahtarsız; seçilen external agent hesabı ayrı. |
| FR-39 | Scoped/versioned/evaluated procedural skills | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-40 | Learned policy/shadow/canary/rollback | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-41 | Ayrı opt-in training/preference/context exports | UYGULANMADI | Geliştirme anahtarsız; kalite/ürün eval'i model erişimi veya kullanıcı/ortam isteyebilir. |
| FR-42 | Team policy/remote execution | UYGULANMADI | Team/auth/remote execution geliştirmesi anahtarsız; deployment/infra ayrı. |

Ek dilimler: **FR-33 F** çok-worker/global lease/mutex/integration/routing kabulü açık; **FR-38 G** geniş MCP/plugin/team extension kapsamı açık. FR-33 B single-worker contract ve FR-38 D asgari extension boundary ilk-stable tablosundadır. Learned ranker/router, procedural skills, training exports ve team remote execution için kendi scoped privacy/effect/consent/rollback/eval kapıları gerekir. API anahtarı olması bunları otomatik uygulamaz.

## 9. Ürün açısından en önemli boşluklar ve doğru öncelik

1. **Gerçek güvenilir başarı üretmek:** K01/K02/K06. Current registered check computation doğru kaydediliyor; hedefin karşılandığını bağımsızca doğrulayan result protocol/observer ve tam freeze/fencing zinciri eksik. Önce bu zincir; daha çok model veya TUI bu eksikliği kapatmaz.
2. **Veri/restore yaşam döngüsünü tamamlamak:** K03/K04. Backup var ama deletion-aware değil; quota/physical control reserve olmadan production durability/retention sözü verilemez.
3. **Kullanıcının kaynaklarına kontrollü teslim:** K05. Candidate export kullanım sağlar; native safe apply/restore ve conflict/crash receipts olmadan PRD delivery journey eksik.
4. **Uzun görev continuity ve runtime yaşamı:** K07/K08/K09. Compaction, safe model switch, environment freshness, supervisor ve reconnect gerçek uzun görev akışını tamamlar.
5. **Günlük kullanım ve policy yüzeyi:** K10/K11/K12/K13. TUI/onboarding/config/privacy/extension/profile hata UX; native credentials ve support bundle.
6. **Kabul ve dağıtım:** K14/K15 ve §4–5. Bağımsız evaluator/performance, actual two-remote+one-local conformance, native packages/signing/review/pilot ve exact-revision evidence bundle.

P02/P03/P04/P05 aynı güven zincirinin farklı sınırlarını tamamlar; sonraki apply/supervisor/UI bunlara dayanır. Öncelik takvim taahhüdü veya “birkaç küçük iş kaldı” iddiası değildir. Anahtarlar geldiğinde live provider kabulü bu sıra ile paralel yürütülebilir; anahtarsız temel bitmeden prod-ready ilanı yapılamaz.

### PRD ayırt edici demo ve release hard-stop'ları

PRD §5 demosu **henüz tam geçmedi**: çok dosyalı görevi mutation sınırında öldür → kullanıcı edit yapsın → başka izinli modelle resume → eski proposal reddedilsin → raw intent korunsun → current final candidate bağımsız receipt ve doğru delivery ile teslim edilsin. Bugünkü stale/cancel/replay/attempt parçaları yararlıdır; model switch, trusted verification ve safe live delivery bütünlüğü tamamlanmadan bu uçtan uca kabul kapatılamaz.

§29 hard-stop'ları: kullanıcı veri kaybı; eski policy/epoch/fencing ile yeni etki; capability/policy bypass; farklı candidate receipt'iyle VERIFIED; unknown effect'in blind retry'ı; required criterion sessiz silme; privacy-restricted fallback; inconsistent checkpoint'ten devam. Çeşitli guard fixture'ları mevcut; ilgili §28 family'lerinin tam matrisi hâlâ PARTIAL/NOT_IMPLEMENTED'dir.

## 10. Dokümantasyon ve operasyonel teslim eksikleri

- README mevcut engineering profile'ı ve anahtar/plan/local kullanımını anlatıyor. Root tarihsel score/analysis dosyaları **0.6**; bunları 0.8 durumu gibi okumamak gerekir. Güncel analiz bu sürümlü dosya ve JSON'dur.
- IMPLEMENTATION_PLAN v2 başlangıç bölümünde 0.4 baseline ve eski eksik listesi korunuyor; güncel dilimler release/validation/ADR kayıtlarında var. Sürüme bağlı current status link'i gereksiz “her şey yok” veya “her şey tamam” yorumunu önler.
- `apply/restore/attach/delete` explicit unsupported. `store-restore` metadata/CAS store restore'udur; kullanıcı source'u için `restore TASK` değildir.
- Root'ta LICENSE bulunmadı; dağıtım lisansı/metadata ve dependency-license/SBOM çıktısı release işinde ele alınmalı. İmzalı/reproducible installer ve update/downgrade/rollback evidence henüz yok.
- Gözlenen engineering test log'larının önemli kısmı ignored local cache'de; bağımsızca denetlenebilir exact-revision release evidence bundle, signed artifacts ve native support manifest'i ayrıca hazırlanmalı.
- Safe support bundle preview/redaction ve managed backup deletion politikası yok; generic export/backup tüm privacy lifecycle'ını tamamlamaz.

## 11. %100 kapanış sözleşmesi

“%100” için yalnız puan satırlarını artırmak yeterli değildir. Her ilgili FR/NFR çalışan davranış **ve gerekli kabul kanıtı** ile kapanmalı; PRD §28'in ilgili mandatory attack/crash families PASS olmalı; SKIP/UNKNOWN/DEFERRED açık kalmamalı; desteklenen backend/platform/model profile'ları gerçekten test edilmeli; protected independent verification doğru subject'e bağlanmalı; safe delivery, deletion/restore, resource reserve ve privacy sözleşmeleri geçmeli; eval/performance/installer/rollback ve review kanıtı exact release revision'ına bağlanmalıdır.

Bugünkü doğru durum: **yaklaşık %46 nitel ilk-stable uygulama kapsamı; production release kabul edilmemiş; hem önemli anahtarsız geliştirme işleri hem API anahtarı/hesap/ortam/insan kabul işleri var.** Model erişimi bugün eklense de bu değerlendirme kendiliğinden %100 olmaz.
