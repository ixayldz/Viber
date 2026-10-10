# PRD: API anahtarı beklemeyen işlerin teslim planı

10 Ekim 2026; başlangıç runtime `0139535`, denetim karşılaştırması `f4af274`. Yetkili kapsam [PRD](../prd.md), bütün FR-01–42 ve NFR-01–12'dir. [Önceki paket planı](KEYLESS_COMPLETION_PLAN.md) ve [denetim karşılaştırması](AUDIT_RECONCILIATION_2026_10_10.md) kanıt tabanıdır. Bu belge bir tamamlanma beyanı değil; kod, kullanıcı akışı ve kabul sırasıdır.

## Teslim kuralı

Her satır için dört çıktı gerekir: çalışan kernel/admission/replay sözleşmesi; CLI/owner/TUI bağlantısı; saldırgan/crash/restore kabulü; kullanıcı kılavuzu ve exact revision kanıtı. Mevcut çalışan alt işler korunur, yeniden yazılmaz. Fixture/native/gerçek provider kabulü birbirine dönüştürülmez. CLOSED/unsupported bir özellik uygulanmış kabul edilmez. Test sayısı PRD yüzdesi üretmez.

### 1. Metadata yaşam döngüsü — FR-06/09/13/26, NFR-01/06/07

1. **Authority allocation:** physical root ve authority kimliği 32 MiB reserve/genesis yazımından önce owner journal'ına CAS ile bağlanır. Bind atomik olarak pending allocation'ı current authority'ye geçirir. Restart aynı fiziksel root'ta tamamlar; başka path/root/genesis/foreign file benimsenmez. Bind sonrası crash ikinci authority yaratmaz. Sadece dizin prefix'i cleanup yetkisi değildir.
2. **Baseline:** 0/256/1024/1536/2048 record, 64 MiB ve 8192 katalog; replay/admission latency/IO, on reopen, ordinary/control exhaustion. Byte sınırı unit testi ve gerçek fiziksel saturation ayrı kanıt olur.
3. **Checkpoint/compaction:** external authority'nin sequence/tail, watermark, deletion dedup, managed copy ve attempt/owner lineage'ı korunur. Publish/checkpoint/current anchor/old file retire crash adımları ayrı kabul edilir. Eski backup restore güncel external authority'yi geçemez. Dosya kaldırma yeni durable state'ten sonra yapılır.
4. **Retention:** açık süre/policy revision ve expiry receipt; otomatik expiry ile user irreversible deletion farklı actor/admission taşır. UNKNOWN süreç/charge pin'leri korunur; explicit raw redaction recovery kaybını açık kaydeder. Content-wide purge minimal kernel/ledger bütünlüğünü bozmaz.
5. **Owner/legacy lifecycle:** inactive lease retirement current scope/backup/evaluator/restore/fence pin'lerini doğrular. Schema-1 evaluator inventory salt okunur başlar; açık operator migration source/root/origin/audit bağlıdır, eski verdict'e yeni assurance vermez.

Kabul: active/replaced/foreign root reddi; bind-before/after actual subprocess death; watermark ve global ledger değişmeden exact purge; old-backup resurrection reddi; 10 reopen/replay; corrupted checkpoint fail-closed. İşlem önce support/status'ta, ardından owner API üzerinden remediation olarak görünür.

### 2. Ortak fiziksel kaynak bütçesi — FR-07/09/35, NFR-06

Backup, export, preview, restore stage, evaluator owner/report, artifact/materialization ve metadata writer envanteri çıkarılır. Allocation öncesi outstanding + temp + final payload byte/entry reservation gerekir. Partial/crashed writer yeniden ölçülmeden rezerv serbest bırakılmaz. Global ledger admission atomiktir; parent/worker riski aynı bütçeye dahildir. Process fences lifetime boyunca charge edilir. Bounded kontrol rezervi normal payload tarafından tüketilemez.

Kabul: aynı anda farklı writer'lar, physical exhaustion, interrupted SQLite snapshot, delayed create, timeout/cancel, fresh owner reopen, allocator failure ve retry. Kullanıcı 10 Ekim'de geçici privileged engine ölçümünü onayladı. Rootful gerçek restart/fencing geçti; bu hosttaki nested rootless engine gerekli kaynak controller'ları olmadığı için güvenle reddedildi. Ayrı disposable Linux CI'de supported Docker CE 28.0.4/rootless/cgroup2/systemd gerçek restart/fencing ve hostile/PID/scratch/output/128 MiB OOM kabulü geçti (`aae720e`, exact native kanıt aşağıda). Aggregate physical resource quota açık kalır; unsupported profil supported sayılmaz.

### 3. Güvenli teslim — FR-03/04/05/08/11/21

Mevcut B/C/U preview ve fresh merged checks korunur. Exclusive backend yalnız teknik primitive ve actual hostile writer kabulüyle açılır; source hash veya advisory lock yeterli değildir. Per-file intent/current precondition/owned diff/final receipt; crash'ta effect reconciliation; kullanıcı editini silmeyen restore. Native backend desteklemiyorsa PRD candidate/patch fallback'i açık raporlanır. Protected final merged candidate yeniden doğrulanır, eski PASS/approval taşınmaz.

Kabul: dirty/staged/untracked baseline; mapped/open writer; namespace/path replacement; case/CRLF/mode; apply/restore arasında user edit; per-file abrupt death ve dedup; final integration checks. Live backend kabulü olmadan canlı write capability beyan edilmez.

### 4. Verification ve derin dil araçları — FR-10/11/15/27/28/31, NFR-03/09

Check discovery ve revision protected dependency closure'a bağlanır; framework self-report trusted observer olmaz. TS/JS ve Python test/config/source discovery; lexical fallback için explicit coverage. V0–V5 her sınıf ayrı observer/verdict/unsupported sınırı taşır. AST/LSP graph exact source/protocol/version/cutoff ve unresolved edge coverage ile kurulur. Runtime trace/coverage yalnız conservative selection; required checks'i azaltmaz.

Kabul: forged PASS, check weakening, malicious discovery/config, stale dependency, incomplete discovery, flaky rerun, framework mismatch, source change ve hidden oracle leak. Test plan revision kullanıcı input/goal/check origin bağlarını korur.

### 5. Supervisor ve kullanıcı akışı — FR-12/19/20/22/23/25, NFR-04/08/10

Durable state cursor ile ephemeral provider delta cursor ayrılır. Slow client bounded buffer'ı kernel'i bloklamaz. Retention gap earliest cursor/current state ref ve explicit resync verir. Queue yeni goal admission/scheduling; cancel/pause/drain child reconciliation; @file/slash/status/diff/plan/context/model/requests ve recovery/restore UX. Capacity pressure numeric reason ve yalnız desteklenen next action taşır.

Kabul: native PTY/input/resize/paste/control sequence; detach/attach/disconnect; stale owner/old child; event gap/duplicate; model terminal partial/cancel; queue stop/restart. Warm CLI/context p95 sabit referans host ve repo sınıfında ölçülür, model wait ayrı tutulur.

### 6. Policy, extensions ve team sınırı — FR-08/18/38/39/41/42, NFR-02/05/09/11

Admin/current restriction revisions authority intersection olarak taşınır. Remote inference, allowed providers, embedding/reranker, MCP/browser, telemetry, training, sensitive paths, cross-project memory ve retention ayrı eksendir. Pinned extension/MCP ayrı sandbox process ve scoped RPC/time/bytes/egress/resource admission taşır; direct kernel DB write yoktur. Procedural skill version/scope/eval manifest'i trusted authorization üretmez. Team policy ve remote executor aynı current authority/receipt/resource sözleşmesini kullanır; unrestricted transport yoktur.

Kabul: version/effect değişimi eski approval'ı reddeder; repo automatic installation reddi; malicious RPC/oversized/timeout/redirect/child; offline egress deny; telemetry/training default OFF; cross-project and current-policy isolation. Gerçek egress capability ölçülmeden açılmaz.

### 7. Retrieval/memory ve repository generation — FR-14/16/17/29/30/32, NFR-06/09

Mevcut FTS5/BM25/trigram/cache/partition/exact span bağları korunur. History/co-change/issue memory cutoff, source/project sensitivity ve license lineage taşır. Local embedding/reranker adapter version/dataset/index fingerprint ve egress admission ile gelir; semantic score izin değildir. Yeni repo blueprint explicit experimental profile, architecture/dependency decisions, generated closure ve independent blueprint checks taşır. Context compaction authoritative obligation/criterion/policy state'i azaltamaz.

Kabul: future history leakage, stale index/model version, missing partitions/unresolved symbols, sensitive path, context overflow ve replay-equivalent retained obligations. Model ablation gerçek model kabulüne ayrıca bağlanır.

### 8. Workers/routing/öğrenme — FR-33/34/35/36/37/40

Typed plan DAG/mutex/resource contracts read-only worker ve isolated writer admission'ını yönetir. AgentLease TTL/generation/scope/task/policy bağlarını taşır; parent bütçesi atomic olarak paylaşılır. Writer integration new final candidate üretir. External adapter yalnız bounded patch/evidence proposal sağlar. Sticky/phase routing measured capability manifest'iyle çalışır. Offline learned policy shadow/canary/rollback taşır; verification selector zorunlu gate'i düşürmez.

Kabul: concurrent stale promotion, duplicate join, cancelled parent/late child, deadlock/mutex expiry, partial integration, routing privacy lock, forged worker receipt, learned-policy invariant violation ve rollback. Kontrollü fixture runtime kabulü model-quality/adoption yerine geçmez.

### 9. Eval, onboarding ve dağıtım — FR-23/24 ve bütün NFR'ler

Frozen preregistered dataset/condition/build attestation runner; assigned bütün attempts/UNKNOWN maliyetleri; repo/time-disjoint holdout; paired continuation/localization; reproducible reference p95. Mevcut separate-owner hidden evaluation bunu temel alır. Install/update/rollback platform packages exact revision hashes/SBOM/license/signing manifest'iyle gelir. Native doctor'dan ilk göreve kadar kullanıcı akışı ve bağımsız pilot ölçülür.

Kabul: altered registration/build/dataset, missing run, unfair budget/model, future leakage, unsafe update/downgrade/rollback, partial install, tam platform uninstall ve actual pilot rework. İmza için gerçek signing identity; kullanım için gerçek insan pilotu gerekir. Bunlar API anahtarı işi olarak etiketlenmez.

## Kapsam haritası ve kapanış

FR-01/02: mevcut raw source-bound intent/tek writer kernel, yukarıdaki bütün değişikliklerde regression gate. FR-03–09: 1–3/6/8. FR-10–18: 4/7/6. FR-19–27: 3/5/9/1/4. FR-28–32: 4/7. FR-33–37: 8. FR-38–42: 6/8. NFR-01–12 her ilgili paketin recovery/policy/verdict/egress/quota/backup/performance/privacy/protocol/export/eval kabulüne dahildir; bir kapsam maddesi atlanmış sayılmaz.

Kodlama sırası **1 → 2 → 3/4 → 5/6 → 7/8 → 9**; bağımlı olmayan read-only ölçüm hazırlığı öne alınabilir. Her runtime commit üç OS full/vet/vulnerability/migration, Linux race/fuzz, gerçek rootful Docker ve iki native vault ile exact-SHA foundation kabulü ister. Kırmızı CI önce düzeltilir. Kullanıcının diğer yerel değişiklikleri commit kapsamına alınmaz.

Son kalan liste üç kanıt türünü açıkça ayırır: (a) yazılım/fixture/native engineering işi, (b) API anahtarı istemeyen dış kabul — gerçek login/yerel model/donanım/signing/pilot, (c) gerçek OpenAI/Anthropic API protocol/usage/rate/cancel/charge/model-quality kabulü. **(a) veya (b) açıkken “yalnız API anahtarları kaldı” denmez.** Kullanıcının hedefi, bu açık maddelerin gerçek kabulüyle kapanmasıdır.

10 Ekim yeni runtime dilimi: `privacy-capacity`/owner/TUI `/capacity` reason/next-action UX ve gerçek bounded reserve replenishment; metadata/catalog doluluğu replenishment sonrası yanlış hazır olamaz. Actual 128 MiB disposable tmpfs üzerinde ENOSPC, ordinary deny, reserve'den control publication, exhaustion denial, fresh reopen ve physical rearm kabulü eklendi. Bunlar F-06 remediation ve F-04 pressure alt işleri; checkpoint/retirement/global quota veya native power-loss yerine geçmez.

İzleyen runtime dilimi: transient restore operation lease'lerine current authority-bound canonical descriptor, existing-inode native lock, normal close retirement ve crash sonrası bounded reclaim eklendi. CLI/owner/TUI `retire-operation-leases` sunar; active process, persistent owner scope, historical untyped lease ve fence'ler korunur. 1.5'in yalnız transient operation katalog sızıntısı kapanır; persistent owner retirement, checkpoint/retention, global payload quota ve 3–9 teslim paketleri açık kalır.

Rootless positive acceptance: `aae720eb96193e8654a0475fd8b5e8f6d4862b6f` [native CI job](https://github.com/ixayldz/Viber/actions/runs/38067484356/job/114257975200) SUCCESS. Fresh non-root UID1002, per-UID systemd delegation, Docker CE 28.0.4/cgroup2/systemd, all enforcement flags ve real same-engine restart/fencing gözlendi. Dört native parent + beş hostile alt senaryo **9 PASS / 0 SKIP / 0 FAIL**; ayrı probe/before/after her biri PASS. Initial/generated XDG environment ve mode0700 runtime doğrulandı; cleanup_verified=true/measurement_exit_code=0. Bu supported profile'nin positive kabulü kapanır; Windows driver-none nested UNSUPPORTED sonucu ve aggregate physical quota açık kalır. Setup yalnız disposable GitHub-hosted runner içindir. CI evidence validator bütün beş hostile scenario'yu ayrıca mandatory yapar; parent PASS alt senaryo SKIP'ini kapatamaz.
